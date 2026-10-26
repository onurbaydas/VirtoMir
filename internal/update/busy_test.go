package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// autoChecker is a Checker against a fake GitHub with a new .deb release and AutoInstall on.
func autoChecker(t *testing.T) (*Checker, *atomic.Int32, string) {
	t.Helper()
	pkg := "the package"
	sum := sha256.Sum256([]byte(pkg))
	signer := newTestSigner(t)
	var srv *httptest.Server
	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/v/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":"virtomir_9.0.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
				{"name":"virtomir_9.0.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"},
				{"name":"virtomir_9.0.0_%[1]s.deb.sha256.asc","browser_download_url":"%[2]s/sig"}]}`, debArch(), srv.URL)
		case "/sha":
			fmt.Fprint(w, hex.EncodeToString(sum[:])+"  virtomir.deb\n")
		case "/deb":
			fmt.Fprint(w, pkg)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodDeb }
	var ran atomic.Int32
	c.RunInstaller = func(context.Context, string) (string, error) { ran.Add(1); return "unit", nil }
	dir := t.TempDir()
	c.InitConfig(dir)
	cfg := c.GetAutoConfig()
	cfg.AutoInstall = true
	if err := c.SetAutoConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return c, &ran, dir
}

func TestAutoInstallWaitsForJobs(t *testing.T) {
	busyPoll, busyWait = time.Millisecond, time.Second
	t.Cleanup(func() { busyPoll, busyWait = 30*time.Second, 30*time.Minute })
	c, ran, dir := autoChecker(t)
	var polls atomic.Int32
	c.Busy = func() bool { return polls.Add(1) < 5 } // busy for the first few looks
	c.autoPass(context.Background(), c.GetAutoConfig(), dir, nil, nil)
	if ran.Load() != 1 || polls.Load() < 5 {
		t.Fatalf("installer ran %d times after %d polls", ran.Load(), polls.Load())
	}
}

func TestAutoInstallPostponedWhileBusy(t *testing.T) {
	busyPoll, busyWait = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { busyPoll, busyWait = 30*time.Second, 30*time.Minute })
	c, ran, dir := autoChecker(t)
	c.Busy = func() bool { return true }
	c.autoPass(context.Background(), c.GetAutoConfig(), dir, nil, nil)
	got := c.GetAutoConfig()
	if ran.Load() != 0 || !strings.Contains(got.LastResult, "waits until no background jobs run") || got.LastError != "" {
		t.Fatalf("ran %d, config %+v", ran.Load(), got)
	}
}

func TestSwapBinaryKeepsOld(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "r.tar.gz")
	os.WriteFile(tarPath, releaseTarball(t, "#!new"), 0o600)
	exe := filepath.Join(dir, "virtomird")
	os.WriteFile(exe, []byte("#!old"), 0o755)
	os.WriteFile(exe+".old", []byte("#!older"), 0o755)
	if err := swapBinary(tarPath, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!new" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "#!old" {
		t.Fatalf("old = %q", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal(".new left behind")
	}
}
