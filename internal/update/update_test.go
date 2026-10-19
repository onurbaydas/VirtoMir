package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.2.0", "0.2.0~rc1", true},
		{"0.2.0~rc2", "0.2.0~rc1", true},
		{"v0.2.1", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0~rc1", "0.2.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.2.0", "dev", true},
		{"dev", "0.2.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if DebVersion("v0.3.0-rc1") != "0.3.0~rc1" {
		t.Error(DebVersion("v0.3.0-rc1"))
	}
}

func TestCheckFindsThePackage(t *testing.T) {
	signer := newTestSigner(t)
	var srv *httptest.Server
	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/onur/vm/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v0.3.0","name":"VirtoMir v0.3.0","html_url":"https://x/rel","assets":[
			{"name":"virtomir_0.3.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
			{"name":"virtomir_0.3.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"},
				{"name":"virtomir_0.3.0_%[1]s.deb.sha256.asc","browser_download_url":"%[2]s/sig"},
			{"name":"virtomir_v0.3.0_linux_%[1]s.tar.gz","browser_download_url":"%[2]s/tgz"}]}`, debArch(), srv.URL)
	}))
	defer srv.Close()
	c := New("onur/vm", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodDeb }
	st := c.Check(context.Background(), false)
	if st.Error != "" || !st.Available || st.Latest.Package != "virtomir_0.3.0_"+debArch()+".deb" || !strings.HasSuffix(st.Latest.shaURL, "/sha") {
		t.Fatalf("status = %+v latest = %+v", st, st.Latest)
	}
	c.Current = "0.3.0"
	if c.Check(context.Background(), false).Available != true {
		t.Error("cached answer not used") // within the hour the old answer stands
	}
	if c.Check(context.Background(), true).Available {
		t.Error("same version reported as an update")
	}
}

func TestInstallRefusesBadChecksum(t *testing.T) {
	signer := newTestSigner(t)
	var srv *httptest.Server
	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/v/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":"virtomir_9.0.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
				{"name":"virtomir_9.0.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"},
				{"name":"virtomir_9.0.0_%[1]s.deb.sha256.asc","browser_download_url":"%[2]s/sig"}]}`, debArch(), srv.URL)
		case "/sha":
			fmt.Fprint(w, strings.Repeat("0", 64)+"  virtomir.deb\n")
		case "/deb":
			fmt.Fprint(w, "not the package")
		}
	}))
	defer srv.Close()
	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodDeb }
	_, err := c.Install(context.Background(), t.TempDir(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotInstallable(t *testing.T) {
	c := New("", "0.2.0", "")
	c.Installed = func() (bool, string) { return false, "not from the package" }
	c.Method = func() string { return MethodDeb }
	st := c.Check(context.Background(), true)
	if st.Installable || st.Error == "" {
		t.Errorf("status = %+v", st)
	}
}

func TestAutoUpdateConfigPersistence(t *testing.T) {
	tmp := t.TempDir()
	c := New("onur/vm", "0.2.0", "")
	c.InitConfig(tmp)

	cfg := c.GetAutoConfig()
	if !cfg.Enabled || cfg.AutoInstall || cfg.IntervalHours != 6 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	cfg.AutoInstall = true
	cfg.IntervalHours = 12
	if err := c.SetAutoConfig(cfg); err != nil {
		t.Fatalf("SetAutoConfig: %v", err)
	}

	// Verify persistence in a new Checker instance
	c2 := New("onur/vm", "0.2.0", "")
	c2.InitConfig(tmp)
	cfg2 := c2.GetAutoConfig()
	if !cfg2.Enabled || !cfg2.AutoInstall || cfg2.IntervalHours != 12 {
		t.Fatalf("persisted config mismatch: %+v", cfg2)
	}
}

func TestAutoUpdateFlow(t *testing.T) {
	signer := newTestSigner(t)
	var srv *httptest.Server
	// SHA256 of "sample deb package"
	// echo -n "sample deb package" | sha256sum -> 2062402135c3c0ef201fa16db326cf6f8899eb7d94e77fc9a1e687e8346bcad8
	const pkgBody = "sample deb package"
	const pkgSum = "185db913d774e8b489db7e103197e9f2bf75401ecc824e1f8c9867aca5e90dc3"

	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/onur/vm/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.3.0","name":"VirtoMir v0.3.0","html_url":"https://x/rel","assets":[
				{"name":"virtomir_0.3.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
				{"name":"virtomir_0.3.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"},
				{"name":"virtomir_0.3.0_%[1]s.deb.sha256.asc","browser_download_url":"%[2]s/sig"}]}`, debArch(), srv.URL)
		case "/sha":
			fmt.Fprintf(w, "%s  virtomir_0.3.0_%s.deb\n", pkgSum, debArch())
		case "/deb":
			fmt.Fprint(w, pkgBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	c := New("onur/vm", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodDeb }
	installedPath := ""
	c.RunInstaller = func(ctx context.Context, debPath string) (string, error) {
		installedPath = debPath
		return "virtomir-update-test-123", nil
	}

	c.InitConfig(tmp)
	if err := c.SetAutoConfig(AutoConfig{
		Enabled:       true,
		AutoInstall:   true,
		IntervalHours: 1,
	}); err != nil {
		t.Fatalf("SetAutoConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	activityRecorded := ""
	// Direct execution of single check with auto-install
	st := c.Check(ctx, true)
	if !st.Available || !st.Installable {
		t.Fatalf("expected available and installable update: %+v", st)
	}

	msg, err := c.Install(ctx, tmp, func(step string) {})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if !strings.Contains(msg, "virtomir-update-test-123") {
		t.Errorf("expected unit name in msg, got %s", msg)
	}
	if installedPath == "" {
		t.Errorf("expected mock installer to be invoked")
	}

	// Verify Status includes AutoConfig
	st2 := c.Check(ctx, false)
	if !st2.Auto.AutoInstall || st2.Auto.IntervalHours != 1 {
		t.Errorf("Status.Auto config mismatch: %+v", st2.Auto)
	}
	_ = activityRecorded
}

func TestAutoInstallDoesNotDeadlock(t *testing.T) {
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
	defer srv.Close()
	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodDeb }
	ran := false
	c.RunInstaller = func(context.Context, string) (string, error) { ran = true; return "unit", nil }
	dir := t.TempDir()
	c.InitConfig(dir)
	cfg := c.GetAutoConfig()
	cfg.AutoInstall = true
	_ = c.SetAutoConfig(cfg)

	done := make(chan struct{})
	go func() { c.autoPass(context.Background(), c.GetAutoConfig(), dir, nil, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-install deadlocked")
	}
	if got := c.GetAutoConfig(); !ran || got.LastUpdatedAt.IsZero() || got.LastError != "" {
		t.Errorf("ran = %v, config = %+v", ran, got)
	}
}
