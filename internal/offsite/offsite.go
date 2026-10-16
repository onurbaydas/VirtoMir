// Package offsite copies backups to another place with restic (ADR-0043): encrypted,
// deduplicated, to any restic repository — S3 and S3-compatible storage (MinIO, Wasabi,
// Ceph RGW …), Backblaze B2, Azure, Google Cloud Storage, SFTP, a REST server or a
// directory. A copy is a restic snapshot of one backup folder, tagged with the machine,
// so it can be brought back into the backup location and restored the usual way.
//
// The repository password and the storage credentials stay on the host
// (<state-dir>/offsite.json, 0600); the API only ever shows that they are set.
package offsite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalid = errors.New("invalid")
	ErrRefused = errors.New("refused")
)

// classed is a sentence for the user that errors.Is matches to its class.
type classed struct {
	class error
	msg   string
}

func (e classed) Error() string        { return e.msg }
func (e classed) Is(target error) bool { return target == e.class }

func invalidf(format string, a ...any) error {
	return classed{ErrInvalid, fmt.Sprintf(format, a...)}
}

func refusedf(format string, a ...any) error {
	return classed{ErrRefused, fmt.Sprintf(format, a...)}
}

// Secret is what the API shows instead of a stored secret; sending it back keeps it.
const Secret = "••••••••"

// Config is the off-site target.
type Config struct {
	// Repository is a restic repository: s3:https://s3.example.com/bucket/virtomir,
	// b2:bucket:virtomir, sftp:backup@host:/srv/restic, rest:https://host:8000/,
	// azure:container:/path, gs:bucket:/path, or /mnt/usb/restic.
	Repository string `json:"repository"`
	Password   string `json:"password,omitempty"`
	// Env holds the storage credentials, e.g. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
	// (also for S3-compatible storage), B2_ACCOUNT_ID / B2_ACCOUNT_KEY, AZURE_ACCOUNT_NAME /
	// AZURE_ACCOUNT_KEY, GOOGLE_APPLICATION_CREDENTIALS.
	Env map[string]string `json:"env,omitempty"`
	// AfterScheduled copies every backup a schedule makes, right after it.
	AfterScheduled bool `json:"afterScheduled"`
	// Retention per machine, applied after each copy; all zero keeps everything.
	KeepLast    int `json:"keepLast,omitempty"`
	KeepDaily   int `json:"keepDaily,omitempty"`
	KeepWeekly  int `json:"keepWeekly,omitempty"`
	KeepMonthly int `json:"keepMonthly,omitempty"`
	// LimitUploadKiB caps the upload rate (KiB/s); 0 is unlimited.
	LimitUploadKiB int `json:"limitUploadKiB,omitempty"`
}

func (c Config) Configured() bool { return c.Repository != "" && c.Password != "" }

var (
	envKeyRE  = regexp.MustCompile(`^(AWS|B2|AZURE|GOOGLE|OS|ST|RCLONE|RESTIC)_[A-Z0-9_]+$`)
	repoRE    = regexp.MustCompile(`^(s3|b2|sftp|rest|azure|gs|swift|rclone):\S+$|^/\S*$`)
	backupRE  = regexp.MustCompile(`^[^/\\]+/[^/\\]+$`)
	snapIDRE  = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	uuidTagRE = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
)

func (c Config) validate() error {
	if c.Repository != "" && !repoRE.MatchString(c.Repository) {
		return invalidf("The repository is a restic location like s3:https://s3.example.com/bucket/virtomir, b2:bucket:path, sftp:user@host:/path, rest:https://host:8000/ or an absolute path.")
	}
	for k := range c.Env {
		if !envKeyRE.MatchString(k) || strings.HasSuffix(k, "_COMMAND") || k == "RESTIC_REPOSITORY" || k == "RESTIC_PASSWORD" || k == "RESTIC_PASSWORD_FILE" {
			return invalidf("%s is not a storage credential VirtoMir passes on (AWS_…, B2_…, AZURE_…, GOOGLE_…, OS_…, ST_…, RCLONE_…).", k)
		}
	}
	for _, n := range []int{c.KeepLast, c.KeepDaily, c.KeepWeekly, c.KeepMonthly, c.LimitUploadKiB} {
		if n < 0 {
			return invalidf("Numbers must not be negative.")
		}
	}
	return nil
}

// Exec runs restic with the given environment; lines of stdout go to onLine as they come.
type Exec func(ctx context.Context, env []string, args []string, onLine func(string)) (stdout []byte, err error)

// Store holds the configuration and talks to restic.
type Store struct {
	path     string
	cacheDir string
	host     string
	exec     Exec
	look     func(string) (string, error)

	mu  sync.Mutex
	cfg Config
	// one restic at a time: the repository is locked anyway, and uploads compete for the link
	busy sync.Mutex
}

// Open loads <path>. host names this host in the snapshots.
func Open(path, cacheDir, host string) (*Store, error) {
	s := &Store{path: path, cacheDir: cacheDir, host: host, exec: execRestic, look: exec.LookPath}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s.cfg)
}

// Config returns the configuration with secrets replaced by Secret.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	if c.Password != "" {
		c.Password = Secret
	}
	c.Env = map[string]string{}
	for k := range s.cfg.Env {
		c.Env[k] = Secret
	}
	return c
}

// SetConfig stores a configuration. Secrets sent back as Secret keep their value; an env
// key left out is removed.
func (s *Store) SetConfig(c Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Password == Secret {
		c.Password = s.cfg.Password
	}
	env := map[string]string{}
	for k, v := range c.Env {
		if v == Secret {
			v = s.cfg.Env[k]
		}
		env[k] = v
	}
	c.Env = env
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.cfg = c
	return nil
}

func (s *Store) current() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.Env = maps.Clone(s.cfg.Env)
	return c
}

// AfterScheduled reports whether scheduled backups are copied.
func (s *Store) AfterScheduled() bool {
	c := s.current()
	return c.Configured() && c.AfterScheduled
}

// Status is restic's presence and the state of the repository.
type Status struct {
	Restic     string `json:"restic,omitempty"` // version, "" if not installed
	Configured bool   `json:"configured"`
}

func (s *Store) Status(ctx context.Context) Status {
	st := Status{Configured: s.current().Configured()}
	if _, err := s.look("restic"); err != nil {
		return st
	}
	if out, err := s.exec(ctx, nil, []string{"version"}, nil); err == nil {
		st.Restic = strings.TrimSpace(string(out))
	}
	return st
}

func (s *Store) env(c Config) []string {
	env := []string{"RESTIC_REPOSITORY=" + c.Repository, "RESTIC_PASSWORD=" + c.Password, "RESTIC_CACHE_DIR=" + s.cacheDir}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		env = append(env, k+"="+c.Env[k])
	}
	return env
}

func (s *Store) run(ctx context.Context, args []string, onLine func(string)) ([]byte, error) {
	c := s.current()
	if !c.Configured() {
		return nil, refusedf("Set up an off-site repository first (Backups → Off-site).")
	}
	if _, err := s.look("restic"); err != nil {
		return nil, refusedf("restic is not installed on this host (e.g. apt install restic).")
	}
	s.busy.Lock()
	defer s.busy.Unlock()
	return s.exec(ctx, s.env(c), args, onLine)
}

// Check opens the repository; with init, a missing repository is created.
func (s *Store) Check(ctx context.Context, init bool) (string, error) {
	_, err := s.run(ctx, []string{"cat", "config", "--no-lock"}, nil)
	if err == nil {
		return "The repository is reachable and the password is right.", nil
	}
	missing := strings.Contains(err.Error(), "Is there a repository at the following location?") ||
		strings.Contains(err.Error(), "repository does not exist") || strings.Contains(err.Error(), "unable to open config file")
	switch {
	case missing && init:
		if _, err := s.run(ctx, []string{"init"}, nil); err != nil {
			return "", err
		}
		return "A new, encrypted repository was created.", nil
	case missing:
		return "", refusedf("There is no restic repository there yet; create one with \"Create repository\".")
	case strings.Contains(err.Error(), "wrong password"):
		return "", refusedf("The repository exists, but the password does not open it.")
	}
	return "", err
}

// Snapshot is one off-site copy of a backup.
type Snapshot struct {
	ID         string    `json:"id"`
	ShortID    string    `json:"shortId"`
	Time       time.Time `json:"time"`
	Host       string    `json:"host"`
	Machine    string    `json:"machine"`    // name at the time of the backup
	DomainUUID string    `json:"domainUuid"` // machine:<uuid> tag
	Backup     string    `json:"backup"`     // the backup's ID: <machine folder>/<timestamp>
	Path       string    `json:"path"`       // where it was on the host
	SizeBytes  uint64    `json:"sizeBytes,omitempty"`
}

type resticSnapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Summary  *struct {
		TotalBytesProcessed uint64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

// Snapshots lists the off-site copies, newest first.
func (s *Store) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := s.run(ctx, []string{"snapshots", "--json", "--no-lock", "--tag", "virtomir"}, nil)
	if err != nil {
		return nil, err
	}
	var raw []resticSnapshot
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	list := make([]Snapshot, 0, len(raw))
	for _, r := range raw {
		sn := Snapshot{ID: r.ID, ShortID: r.ShortID, Time: r.Time, Host: r.Hostname}
		if len(r.Paths) > 0 {
			sn.Path = r.Paths[0]
		}
		if r.Summary != nil {
			sn.SizeBytes = r.Summary.TotalBytesProcessed
		}
		for _, t := range r.Tags {
			k, v, _ := strings.Cut(t, ":")
			switch k {
			case "machine":
				sn.DomainUUID = v
			case "name":
				sn.Machine = v
			case "backup":
				sn.Backup = v
			}
		}
		list = append(list, sn)
	}
	slices.SortFunc(list, func(a, b Snapshot) int { return b.Time.Compare(a.Time) })
	return list, nil
}

// Upload copies the backup folder <dir>/<backupID> and applies the retention for its
// machine. It returns the new snapshot's short ID.
func (s *Store) Upload(ctx context.Context, dir, backupID, domainUUID, machine string, report func(string)) (string, error) {
	if !backupRE.MatchString(backupID) || strings.Contains(backupID, "..") {
		return "", invalidf("No such backup %q.", backupID)
	}
	if !uuidTagRE.MatchString(domainUUID) {
		return "", invalidf("Bad machine ID %q.", domainUUID)
	}
	path := filepath.Join(dir, filepath.FromSlash(backupID))
	if _, err := os.Stat(filepath.Join(path, "manifest.json")); err != nil {
		return "", invalidf("No such backup %q.", backupID)
	}
	c := s.current()
	args := []string{"backup", "--json", "--host", s.host, "--tag", "virtomir", "--tag", "machine:" + domainUUID,
		"--tag", "backup:" + backupID, "--tag", "name:" + tagSafe(machine)}
	if c.LimitUploadKiB > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(c.LimitUploadKiB))
	}
	args = append(args, path)
	snap := ""
	last := -1
	_, err := s.run(ctx, args, func(line string) {
		var m struct {
			Type       string  `json:"message_type"`
			Percent    float64 `json:"percent_done"`
			BytesDone  uint64  `json:"bytes_done"`
			TotalBytes uint64  `json:"total_bytes"`
			Snapshot   string  `json:"snapshot_id"`
		}
		if json.Unmarshal([]byte(line), &m) != nil {
			return
		}
		switch m.Type {
		case "status":
			if p := int(m.Percent * 100); p != last && report != nil {
				last = p
				report(fmt.Sprintf("Copying off-site… %d%%", p))
			}
		case "summary":
			snap = m.Snapshot
		}
	})
	if err != nil {
		return "", err
	}
	if len(snap) > 8 {
		snap = snap[:8]
	}
	if c.KeepLast+c.KeepDaily+c.KeepWeekly+c.KeepMonthly > 0 {
		if report != nil {
			report("Applying the off-site retention…")
		}
		if err := s.Forget(ctx, domainUUID); err != nil {
			return snap, fmt.Errorf("copied as %s, but the retention failed: %w", snap, err)
		}
	}
	return snap, nil
}

// tagSafe: restic separates tags with commas.
func tagSafe(s string) string { return strings.NewReplacer(",", "_", " ", "_").Replace(s) }

// Forget applies the retention to one machine's copies from this host and prunes.
func (s *Store) Forget(ctx context.Context, domainUUID string) error {
	c := s.current()
	args := []string{"forget", "--tag", "virtomir,machine:" + domainUUID, "--host", s.host, "--group-by", "host", "--prune"}
	for _, k := range []struct {
		flag string
		n    int
	}{{"--keep-last", c.KeepLast}, {"--keep-daily", c.KeepDaily}, {"--keep-weekly", c.KeepWeekly}, {"--keep-monthly", c.KeepMonthly}} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(k.n))
		}
	}
	_, err := s.run(ctx, args, nil)
	return err
}

// Restore brings a copy back into <dir> under its backup ID, so it appears among the
// local backups (and can be restored as a machine from there).
func (s *Store) Restore(ctx context.Context, dir, snapshotID string, report func(string)) (string, error) {
	if !snapIDRE.MatchString(snapshotID) {
		return "", invalidf("Bad snapshot ID.")
	}
	list, err := s.Snapshots(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(list, func(sn Snapshot) bool { return sn.ID == snapshotID || sn.ShortID == snapshotID })
	if i < 0 {
		return "", invalidf("No off-site copy %s.", snapshotID)
	}
	sn := list[i]
	if !backupRE.MatchString(sn.Backup) || strings.Contains(sn.Backup, "..") || sn.Path == "" {
		return "", refusedf("Copy %s was not made by VirtoMir.", sn.ShortID)
	}
	target := filepath.Join(dir, filepath.FromSlash(sn.Backup))
	if _, err := os.Stat(target); err == nil {
		return "", refusedf("Backup %s is already in the backup location.", sn.Backup)
	}
	if report != nil {
		report("Downloading " + sn.Backup + "…")
	}
	// <snapshot>:<path> restores the folder's contents straight into the target.
	if _, err := s.run(ctx, []string{"restore", sn.ID + ":" + sn.Path, "--target", target}, nil); err != nil {
		_ = os.RemoveAll(target)
		return "", err
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		_ = os.RemoveAll(target)
		return "", fmt.Errorf("the copy came back without its manifest")
	}
	return sn.Backup, nil
}

// execRestic runs the real restic binary.
func execRestic(ctx context.Context, env []string, args []string, onLine func(string)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(stdout, &buf))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "Fatal: "); i >= 0 {
			msg = msg[i:]
		}
		return buf.Bytes(), fmt.Errorf("restic %s: %s", args[0], firstLine(msg, err.Error()))
	}
	return buf.Bytes(), nil
}

func firstLine(s, fallback string) string {
	if s == "" {
		return fallback
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}
