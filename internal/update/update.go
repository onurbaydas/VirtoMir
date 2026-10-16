// Package update finds newer VirtoMir releases on GitHub and installs them (ADR-0036): it
// downloads the release's .deb for this architecture, checks it against the published
// SHA-256 and hands it to apt in a transient systemd unit, so the upgrade survives the
// restart of virtomird itself. Only hosts that installed VirtoMir from the .deb update this
// way; others get instructions.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is a published VirtoMir release.
type Release struct {
	Tag       string    `json:"tag"`
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	URL       string    `json:"url"` // release page
	Published time.Time `json:"published"`
	// Package is the .deb for this architecture ("" when the release has none).
	Package string `json:"package,omitempty"`
	pkgURL  string
	shaURL  string
}

// Status is what the Updates card shows.
type Status struct {
	Current   string   `json:"current"`
	Repo      string   `json:"repo"`
	Latest    *Release `json:"latest,omitempty"`
	Available bool     `json:"available"` // Latest is newer than Current
	// Installable: this host runs the .deb package, so it can update itself.
	Installable bool      `json:"installable"`
	Reason      string    `json:"reason,omitempty"` // why not installable
	CheckedAt   time.Time `json:"checkedAt"`
	Error       string    `json:"error,omitempty"`
}

// Checker talks to the GitHub API.
type Checker struct {
	Repo    string // owner/name; "" disables updates
	Current string // running version
	Token   string // optional, for private repositories
	API     string // https://api.github.com (tests replace it)
	HTTP    *http.Client
	// Installed reports whether the virtomir package is installed (dpkg); tests replace it.
	Installed func() (bool, string)

	mu     sync.Mutex
	cached Status
}

// New returns a checker for repo, running version current.
func New(repo, current, token string) *Checker {
	return &Checker{Repo: repo, Current: current, Token: token, API: "https://api.github.com",
		HTTP: &http.Client{Timeout: 30 * time.Second}, Installed: debInstalled}
}

const cacheFor = time.Hour

// Check asks GitHub for the latest release (at most once an hour unless force).
func (c *Checker) Check(ctx context.Context, force bool) Status {
	c.mu.Lock()
	if !force && !c.cached.CheckedAt.IsZero() && time.Since(c.cached.CheckedAt) < cacheFor {
		s := c.cached
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	st := Status{Current: c.Current, Repo: c.Repo, CheckedAt: time.Now().UTC()}
	st.Installable, st.Reason = c.Installed()
	if c.Repo == "" {
		st.Error = "updates are turned off (-update-repo \"\")"
		return st
	}
	rel, err := c.latest(ctx)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest = rel
		st.Available = Newer(rel.Version, c.Current)
		if st.Available && rel.Package == "" && st.Installable {
			st.Installable, st.Reason = false, "this release has no .deb for "+debArch()
		}
	}
	c.mu.Lock()
	c.cached = st
	c.mu.Unlock()
	return st
}

func (c *Checker) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "VirtoMir/"+c.Current)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, errors.New("no published release found (a private repository needs VIRTOMIR_GITHUB_TOKEN in /etc/default/virtomird)")
		case http.StatusForbidden, http.StatusTooManyRequests:
			return nil, errors.New("GitHub refused the request (rate limit); try again later")
		}
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return resp, nil
}

func (c *Checker) latest(ctx context.Context) (*Release, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.API, "/"), c.Repo), "application/vnd.github+json")
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
	rel := &Release{Tag: r.TagName, Version: DebVersion(r.TagName), Name: r.Name, Notes: r.Body, URL: r.HTMLURL, Published: r.PublishedAt}
	want := fmt.Sprintf("virtomir_%s_%s.deb", rel.Version, debArch())
	for _, a := range r.Assets {
		u := a.Web
		if c.Token != "" {
			u = a.URL
		}
		switch a.Name {
		case want:
			rel.Package, rel.pkgURL = a.Name, u
		case want + ".sha256":
			rel.shaURL = u
		}
	}
	return rel, nil
}

// DebVersion turns a tag (v0.2.0-rc1) into the package version (0.2.0~rc1).
func DebVersion(tag string) string {
	return strings.ReplaceAll(strings.TrimPrefix(tag, "v"), "-", "~")
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
	switch {
	case pa.pre == pb.pre:
		return false
	case pa.pre == "":
		return true // release after its candidates
	case pb.pre == "":
		return false
	}
	return pa.pre > pb.pre
}

type ver struct {
	n   [3]int
	pre string
}

func parse(s string) (ver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v ver
	main, pre, _ := strings.Cut(strings.ReplaceAll(s, "-", "~"), "~")
	v.pre = pre
	parts := strings.Split(main, ".")
	if len(parts) == 0 || len(parts) > 3 {
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

// debInstalled: the running binary is the packaged one and dpkg knows the package.
func debInstalled() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "updates install on Linux hosts only"
	}
	if exe, _ := os.Executable(); exe != "/usr/bin/virtomird" {
		return false, "this virtomird was not installed from the .deb package (it runs from " + exe + "); install the package or use install.sh --download"
	}
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", "virtomir").Output()
	if err != nil || !strings.Contains(string(out), "install ok installed") {
		return false, "the virtomir package is not installed (dpkg)"
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false, "systemd-run is missing"
	}
	return true, ""
}

// Install downloads the latest release's package into dir, checks its SHA-256 and starts
// apt in a transient unit. It returns once apt has been started: virtomird is restarted by
// the package's postinst a few seconds later.
func (c *Checker) Install(ctx context.Context, dir string, report func(string)) (string, error) {
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
	want := strings.ToLower(strings.Fields(string(sumLine) + " ")[0])
	if len(want) != 64 {
		return "", errors.New("the published checksum file is not a SHA-256 sum")
	}
	resp, err := c.get(ctx, rel.pkgURL, "application/octet-stream")
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
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20)); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("download: %w", err)
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(path)
		return "", fmt.Errorf("checksum mismatch (got %s, release says %s); nothing was installed", got, want)
	}
	report("Checksum verified; installing")
	unit := fmt.Sprintf("virtomir-update-%d", time.Now().Unix())
	cmd := exec.Command("systemd-run", "--unit="+unit, "--collect", "--quiet",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confold", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start the upgrade: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("Installing %s (unit %s); VirtoMir restarts in a few seconds — reload the page then", rel.Version, unit), nil
}
