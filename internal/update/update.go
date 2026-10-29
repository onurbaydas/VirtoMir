// Package update finds newer VirtoMir releases on GitHub and installs them (ADR-0036): it
// downloads the release file that matches how this host installed VirtoMir and checks it
// against the published SHA-256, whose signature by the release key (.sha256.asc) it checks
// first. A .deb goes to apt and an .rpm to dnf in a transient systemd
// unit, so the upgrade survives the restart of virtomird itself; any other install (tarball,
// install.sh, a binary run from anywhere) gets the binary from the release tarball swapped in
// place and the daemon restarted.
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/atomicfile"
	"github.com/onurbaydas/virtomir/internal/signature"
)

// Release is a published VirtoMir release.
type Release struct {
	Tag       string    `json:"tag"`
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	URL       string    `json:"url"` // release page
	Published time.Time `json:"published"`
	// Package is the file this host installs (.deb, .rpm or tarball; "" when the release has none).
	Package string `json:"package,omitempty"`
	pkgURL  string
	shaURL  string
	sigURL  string            // the checksum file's signature (.sha256.asc)
	assets  map[string]string // every release file name -> download URL
}

// pick selects the release file for an install method.
func (r *Release) pick(method string) {
	name := assetFor(method, r)
	r.Package, r.pkgURL, r.shaURL, r.sigURL = "", "", "", ""
	if _, ok := r.assets[name]; !ok {
		// GitHub renames some characters of uploaded files: a build of main's .deb
		// (virtomir_1.1.1+19.gabc1234_amd64.deb) may be listed with "." for "+".
		if alt := strings.ReplaceAll(name, "+", "."); r.assets[alt] != "" {
			name = alt
		}
	}
	if u, ok := r.assets[name]; ok {
		r.Package, r.pkgURL = name, u
	}
	r.shaURL = r.assets[name+".sha256"]
	r.sigURL = r.assets[name+".sha256.asc"]
}

// AutoConfig defines automatic background update behavior (ADR-0036).
type AutoConfig struct {
	Enabled       bool      `json:"enabled"`       // periodic background check enabled
	Channel       string    `json:"channel"`       // ChannelMain (default) or ChannelReleases
	AutoInstall   bool      `json:"autoInstall"`   // automatically install newer release
	IntervalHours int       `json:"intervalHours"` // interval between checks in hours (default 6)
	LastCheckedAt time.Time `json:"lastCheckedAt,omitempty"`
	LastUpdatedAt time.Time `json:"lastUpdatedAt,omitempty"`
	LastResult    string    `json:"lastResult,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}

// Status is what the Updates card shows.
type Status struct {
	Current   string   `json:"current"`
	Repo      string   `json:"repo"`
	Latest    *Release `json:"latest,omitempty"`
	Available bool     `json:"available"` // Latest is newer than Current
	// Method is how this host installed VirtoMir: deb, rpm or binary.
	Method string `json:"method"`
	// Installable: this host can update itself with its install method.
	Installable bool       `json:"installable"`
	Reason      string     `json:"reason,omitempty"` // why not installable
	CheckedAt   time.Time  `json:"checkedAt"`
	Error       string     `json:"error,omitempty"`
	Auto        AutoConfig `json:"auto"`
}

// Checker talks to the GitHub API.
type Checker struct {
	Repo    string // owner/name; "" disables updates
	Current string // running version
	Token   string // optional, for private repositories
	API     string // https://api.github.com (tests replace it)
	HTTP    *http.Client
	// Download fetches the release package: no total timeout (a large package on a slow
	// link takes longer than an API call), only connect/header limits and a stall check.
	// nil: HTTP.
	Download *http.Client
	// Busy reports whether work is running that a restart would kill (background jobs).
	// The auto-updater waits for it to clear before installing; nil: never busy.
	Busy func() bool
	// Installed reports whether this host can update itself; tests replace it.
	Installed func() (bool, string)
	// Method reports how VirtoMir was installed (MethodDeb, MethodRPM, MethodBinary); tests replace it.
	Method          func() string
	RunInstaller    func(ctx context.Context, debPath string) (string, error)
	RunRPMInstaller func(ctx context.Context, rpmPath string) (string, error)
	// Restart restarts the daemon after a binary update; tests replace it.
	Restart func(exe string) (string, error)
	// Keys sign the releases' checksums (nil: the VirtoMir release key); tests replace it.
	Keys *signature.Keyring

	mu       sync.Mutex
	cached   Status
	cfgMu    sync.RWMutex
	autoCfg  AutoConfig
	stateDir string
	wakeCh   chan struct{}
}

func defaultRunInstaller(ctx context.Context, debPath string) (string, error) {
	unit := fmt.Sprintf("virtomir-update-%d", time.Now().Unix())
	cmd := exec.CommandContext(ctx, "systemd-run", "--unit="+unit, "--collect", "--quiet",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confold", debPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start the upgrade: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return unit, nil
}

// New returns a checker for repo, running version current.
func New(repo, current, token string) *Checker {
	return &Checker{
		Repo:            repo,
		Current:         current,
		Token:           token,
		API:             "https://api.github.com",
		HTTP:            &http.Client{Timeout: 30 * time.Second},
		Download:        downloadClient(),
		Installed:       canInstall,
		Method:          detectMethod,
		RunInstaller:    defaultRunInstaller,
		RunRPMInstaller: defaultRunRPMInstaller,
		autoCfg: AutoConfig{
			Enabled:       true,
			Channel:       ChannelMain,
			AutoInstall:   false,
			IntervalHours: 6,
			LastResult:    "Auto-updater active (checking every 6h)",
		},
		wakeCh: make(chan struct{}, 1),
	}
}

const cacheFor = time.Hour

// Check asks GitHub for the latest release (at most once an hour unless force).
func (c *Checker) Check(ctx context.Context, force bool) Status {
	c.mu.Lock()
	if !force && !c.cached.CheckedAt.IsZero() && time.Since(c.cached.CheckedAt) < cacheFor {
		s := c.cached
		c.mu.Unlock()
		s.Auto = c.GetAutoConfig()
		return s
	}
	c.mu.Unlock()

	st := Status{Current: c.Current, Repo: c.Repo, CheckedAt: time.Now().UTC(), Method: c.method()}
	st.Installable, st.Reason = c.Installed()
	if c.Repo == "" {
		st.Error = "updates are turned off (-update-repo \"\")"
		st.Auto = c.GetAutoConfig()
		return st
	}
	rel, err := c.latest(ctx)
	if err != nil {
		st.Error = err.Error()
	} else {
		rel.pick(st.Method)
		st.Latest = rel
		st.Available = Newer(rel.Version, c.Current)
		if st.Available && rel.Package == "" && st.Installable {
			st.Installable, st.Reason = false, "this release has no "+kindOf(st.Method)+" for "+runtime.GOARCH
		}
	}
	c.mu.Lock()
	c.cached = st
	c.mu.Unlock()
	st.Auto = c.GetAutoConfig()
	return st
}

// downloadClient has no total timeout; connecting, TLS and waiting for the headers are
// limited, and Install aborts a body that stalls (stallTimeout).
func downloadClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	t.ResponseHeaderTimeout = time.Minute
	return &http.Client{Transport: t}
}

// stallTimeout: a download that receives nothing for this long is given up.
var stallTimeout = 2 * time.Minute

// stallReader cancels the download when no bytes arrive for stallTimeout.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(stallTimeout)
	}
	return n, err
}

func (c *Checker) get(ctx context.Context, url, accept string) (*http.Response, error) {
	return c.getWith(ctx, c.HTTP, url, accept)
}

func (c *Checker) getWith(ctx context.Context, client *http.Client, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "VirtoMir/"+c.Current)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, errNoRelease
		case http.StatusForbidden, http.StatusTooManyRequests:
			return nil, errors.New("GitHub refused the request (rate limit); try again later")
		}
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return resp, nil
}

var errNoRelease = errors.New("no published release found (a private repository needs VIRTOMIR_GITHUB_TOKEN in /etc/default/virtomird)")

// Update channels.
const (
	ChannelMain     = "main"     // every change merged to main (the rolling "edge" release CI publishes)
	ChannelReleases = "releases" // tagged releases only
	// EdgeTag is the release CI replaces with a build of every push to main.
	EdgeTag = "edge"
)

// latest is the newest build of the configured channel. Before CI has published a build of
// main, the main channel gets the latest release.
func (c *Checker) latest(ctx context.Context) (*Release, error) {
	if c.GetAutoConfig().Channel != ChannelReleases {
		rel, err := c.release(ctx, "tags/"+EdgeTag)
		if !errors.Is(err, errNoRelease) {
			return rel, err
		}
	}
	return c.release(ctx, "latest")
}

func (c *Checker) release(ctx context.Context, which string) (*Release, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/%s", strings.TrimRight(c.API, "/"), c.Repo, which), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"url"`                  // API URL (works with a token)
			Web  string `json:"browser_download_url"` // public URL
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("unexpected answer from GitHub: %w", err)
	}
	tag := r.TagName
	if tag == EdgeTag { // the build of main names its version: "VirtoMir v1.1.1-19-gabc1234"
		tag = strings.TrimSpace(strings.TrimPrefix(r.Name, "VirtoMir"))
	}
	rel := &Release{Tag: tag, Version: DebVersion(tag), Name: r.Name, Notes: r.Body, URL: r.HTMLURL, Published: r.PublishedAt,
		assets: map[string]string{}}
	for _, a := range r.Assets {
		u := a.Web
		if c.Token != "" {
			u = a.URL
		}
		rel.assets[a.Name] = u
	}
	rel.pick(MethodDeb)
	return rel, nil
}

// describeRE is the end of a build from main ("git describe"): N commits after the tag
// (-19-gabc1234), or its Debian spelling (+19.gabc1234).
var describeRE = regexp.MustCompile(`[-+](\d+)[-.](g[0-9a-f]+)(-dirty)?$`)

// DebVersion turns a tag (v0.2.0-rc1) into the package version (0.2.0~rc1): "~" sorts a
// candidate before its release. A build from main (v1.1.1-19-gabc1234) comes after its tag,
// so its commits are added with "+" (1.1.1+19.gabc1234).
func DebVersion(tag string) string {
	v, post := strings.TrimPrefix(tag, "v"), ""
	if m := describeRE.FindStringSubmatchIndex(v); m != nil {
		post = "+" + v[m[2]:m[3]] + "." + v[m[4]:m[5]]
		v = v[:m[0]]
	}
	return strings.ReplaceAll(v, "-", "~") + post
}

func debArch() string {
	if runtime.GOARCH == "arm" {
		return "armhf"
	}
	return runtime.GOARCH // amd64, arm64 are Debian names too
}

// Newer reports whether version a is newer than b ("0.2.0" > "0.2.0~rc1" > "0.1.9").
// A development build ("dev", a git hash) is never newer and everything is newer than it.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	switch {
	case !oka:
		return false
	case !okb:
		return true
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	if pa.pre != pb.pre {
		switch {
		case pa.pre == "":
			return true // release after its candidates
		case pb.pre == "":
			return false
		}
		return pa.pre > pb.pre
	}
	return pa.post > pb.post // more commits after the same tag
}

type ver struct {
	n    [3]int
	pre  string
	post int // commits after the tag (a build from main)
}

func parse(s string) (ver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v ver
	if m := describeRE.FindStringSubmatchIndex(s); m != nil {
		v.post, _ = strconv.Atoi(s[m[2]:m[3]])
		s = s[:m[0]]
	}
	main, pre, _ := strings.Cut(strings.ReplaceAll(s, "-", "~"), "~")
	v.pre = pre
	parts := strings.Split(main, ".")
	// Releases are always major.minor.patch; anything else (a commit hash like 1328635-pam) is a dev build.
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}

// Install downloads the latest release's package into dir, checks its signed SHA-256 and starts
// apt in a transient unit. It returns once apt has been started: virtomird is restarted by
// the package's postinst a few seconds later.
func (c *Checker) Install(ctx context.Context, dir string, report func(string)) (string, error) {
	return c.install(ctx, dir, report, nil)
}

// install is Install; beforeSwap, if set, runs after the download is verified and right
// before anything is replaced or restarted, and stops the install with its error.
func (c *Checker) install(ctx context.Context, dir string, report func(string), beforeSwap func() error) (string, error) {
	st := c.Check(ctx, true)
	switch {
	case st.Error != "":
		return "", errors.New(st.Error)
	case !st.Available:
		return "", fmt.Errorf("already up to date (%s)", c.Current)
	case !st.Installable:
		return "", errors.New(st.Reason)
	case st.Latest.shaURL == "":
		return "", errors.New("the release has no .sha256 next to its package; refusing to install it unchecked")
	case st.Latest.sigURL == "":
		return "", errors.New("the release's checksum is not signed (no .sha256.asc); refusing to install it")
	}
	rel := st.Latest
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	report("Downloading " + rel.Package)
	sumResp, err := c.get(ctx, rel.shaURL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	sumLine, _ := io.ReadAll(io.LimitReader(sumResp.Body, 4096))
	sumResp.Body.Close()
	sigResp, err := c.get(ctx, rel.sigURL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	keys := c.Keys
	if keys == nil {
		keys = signature.VirtoMir()
	}
	err = keys.Verify(bytes.NewReader(sumLine), sigResp.Body)
	sigResp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("%w; nothing was installed", err)
	}
	want := strings.ToLower(strings.Fields(string(sumLine) + " ")[0])
	if len(want) != 64 {
		return "", errors.New("the published checksum file is not a SHA-256 sum")
	}
	dl := c.Download
	if dl == nil {
		dl = c.HTTP
	}
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()
	resp, err := c.getWith(dlCtx, dl, rel.pkgURL, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	path := filepath.Join(dir, rel.Package)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	body := &stallReader{r: io.LimitReader(resp.Body, 512<<20), timer: stall}
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("download: %w", err)
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(path)
		return "", fmt.Errorf("checksum mismatch (got %s, release says %s); nothing was installed", got, want)
	}
	if beforeSwap != nil {
		if err := beforeSwap(); err != nil {
			os.Remove(path)
			return "", err
		}
	}
	report("Signature and checksum verified; installing")
	installer := c.RunInstaller
	switch st.Method {
	case MethodBinary:
		return c.installBinary(path, rel, report)
	case MethodRPM:
		installer = c.RunRPMInstaller
		if installer == nil {
			installer = defaultRunRPMInstaller
		}
	}
	if installer == nil {
		installer = defaultRunInstaller
	}
	unit, err := installer(ctx, path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Installing %s (unit %s); VirtoMir restarts in a few seconds — reload the page then", rel.Version, unit), nil
}

// InitConfig loads the persisted AutoConfig from stateDir or initializes defaults.
func (c *Checker) InitConfig(stateDir string) {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	c.stateDir = stateDir
	cfgFile := filepath.Join(stateDir, "updates", "config.json")
	data, err := os.ReadFile(cfgFile)
	if err == nil {
		var loaded AutoConfig
		if err := json.Unmarshal(data, &loaded); err == nil {
			if loaded.IntervalHours < 1 {
				loaded.IntervalHours = 6
			}
			if loaded.Channel != ChannelReleases { // settings saved before channels: main
				loaded.Channel = ChannelMain
			}
			c.autoCfg = loaded
			return
		}
	}
	c.autoCfg = AutoConfig{
		Enabled:       true,
		Channel:       ChannelMain,
		AutoInstall:   false,
		IntervalHours: 6,
		LastResult:    "Auto-updater active (checking every 6h)",
	}
}

// GetAutoConfig returns the current auto-update configuration.
func (c *Checker) GetAutoConfig() AutoConfig {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.autoCfg
}

// SetAutoConfig updates the auto-update settings and persists them to stateDir.
func (c *Checker) SetAutoConfig(cfg AutoConfig) error {
	c.cfgMu.Lock()
	if cfg.IntervalHours < 1 {
		cfg.IntervalHours = 1
	}
	c.autoCfg.Enabled = cfg.Enabled
	if cfg.Channel != ChannelReleases {
		cfg.Channel = ChannelMain
	}
	if cfg.Channel != c.autoCfg.Channel {
		c.mu.Lock()
		c.cached = Status{} // the next check asks the other channel
		c.mu.Unlock()
	}
	c.autoCfg.Channel = cfg.Channel
	c.autoCfg.AutoInstall = cfg.AutoInstall
	c.autoCfg.IntervalHours = cfg.IntervalHours
	cur := c.autoCfg
	dir := c.stateDir
	c.cfgMu.Unlock()

	var err error
	if dir != "" {
		err = saveAutoConfig(dir, cur)
	}

	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
	return err
}

// saveAutoConfig writes <stateDir>/updates/config.json atomically.
func saveAutoConfig(stateDir string, cfg AutoConfig) error {
	upDir := filepath.Join(stateDir, "updates")
	if err := os.MkdirAll(upDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(upDir, "config.json"), data, 0o600)
}

// autoPass is one background check, and an install when AutoInstall allows it. It holds
// cfgMu only to record the outcome: Check and Install read the config themselves.
func (c *Checker) autoPass(ctx context.Context, cfg AutoConfig, stateDir string, onActivity func(action, details string), log *slog.Logger) {
	st := c.Check(ctx, true)
	var result, lastErr string
	var installed bool
	switch {
	case st.Error != "":
		result, lastErr = "Check failed: "+st.Error, st.Error
	case !st.Available:
		result = fmt.Sprintf("Up to date (%s)", c.Current)
	case !cfg.AutoInstall || !st.Installable:
		result = fmt.Sprintf("New release %s available", st.Latest.Version)
	case !c.waitIdle(ctx):
		result = fmt.Sprintf("New release %s available; installing it waits until no background jobs run", st.Latest.Version)
		if log != nil {
			log.Info("auto-update: jobs are running, installing at a later check", "version", st.Latest.Version)
		}
	default:
		if log != nil {
			log.Info("auto-update: installing the new release", "version", st.Latest.Version, "current", c.Current)
		}
		msg, err := c.install(ctx, filepath.Join(stateDir, "updates"), func(step string) {
			if log != nil {
				log.Info("auto-update", "status", step)
			}
		}, func() error {
			if c.Busy != nil && c.Busy() {
				return errBusy
			}
			return nil
		})
		switch {
		case errors.Is(err, errBusy):
			result = fmt.Sprintf("New release %s available; installing it waits until no background jobs run", st.Latest.Version)
		case err != nil:
			result, lastErr = fmt.Sprintf("Auto-install failed: %v", err), err.Error()
			if onActivity != nil {
				onActivity("update.auto_failed", fmt.Sprintf("Failed to auto-install %s: %v", st.Latest.Version, err))
			}
		default:
			result, installed = fmt.Sprintf("Auto-installed %s: %s", st.Latest.Version, msg), true
			if onActivity != nil {
				onActivity("update.auto_success", fmt.Sprintf("Installed %s automatically", st.Latest.Version))
			}
		}
	}

	c.cfgMu.Lock()
	now := time.Now().UTC()
	c.autoCfg.LastCheckedAt, c.autoCfg.LastResult, c.autoCfg.LastError = now, result, lastErr
	if installed {
		c.autoCfg.LastUpdatedAt = now
	}
	cur := c.autoCfg
	c.cfgMu.Unlock()

	if err := saveAutoConfig(stateDir, cur); err != nil && log != nil {
		log.Warn("auto-update: save settings", "err", err)
	}
}

var errBusy = errors.New("background jobs are running")

// busyPoll and busyWait: how often and how long the auto-updater waits for running jobs
// to finish before it postpones an install to the next check.
var (
	busyPoll = 30 * time.Second
	busyWait = 30 * time.Minute
)

// waitIdle waits until Busy reports false (true) or busyWait passes or ctx ends (false).
func (c *Checker) waitIdle(ctx context.Context) bool {
	if c.Busy == nil {
		return true
	}
	deadline := time.Now().Add(busyWait)
	for c.Busy() {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(busyPoll):
		}
	}
	return true
}

// StartAutoUpdater runs the periodic GitHub update poller in the background.
// If AutoInstall is enabled and a new version is detected, it automatically downloads
// and triggers the upgrade. Call InitConfig first, so settings saved before the first
// check are not overwritten.
func (c *Checker) StartAutoUpdater(ctx context.Context, stateDir string, onActivity func(action, details string), log *slog.Logger) {
	if c.Repo == "" {
		return
	}

	// Delay initial check by 15s to allow daemon startup to settle
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}

	for {
		cfg := c.GetAutoConfig()
		if cfg.Enabled {
			c.autoPass(ctx, cfg, stateDir, onActivity, log)
		}

		interval := time.Duration(cfg.IntervalHours) * time.Hour
		if interval < time.Hour {
			interval = time.Hour
		}

		select {
		case <-ctx.Done():
			return
		case <-c.wakeCh:
		case <-time.After(interval):
		}
	}
}
