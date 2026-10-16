package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/onur/vm/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v0.3.0","name":"VirtoMir v0.3.0","html_url":"https://x/rel","assets":[
			{"name":"virtomir_0.3.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
			{"name":"virtomir_0.3.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"},
			{"name":"virtomir_v0.3.0_linux_%[1]s.tar.gz","browser_download_url":"%[2]s/tgz"}]}`, debArch(), srv.URL)
	}))
	defer srv.Close()
	c := New("onur/vm", "0.2.0", "")
	c.API = srv.URL
	c.Installed = func() (bool, string) { return true, "" }
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
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/v/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":"virtomir_9.0.0_%[1]s.deb","browser_download_url":"%[2]s/deb"},
				{"name":"virtomir_9.0.0_%[1]s.deb.sha256","browser_download_url":"%[2]s/sha"}]}`, debArch(), srv.URL)
		case "/sha":
			fmt.Fprint(w, strings.Repeat("0", 64)+"  virtomir.deb\n")
		case "/deb":
			fmt.Fprint(w, "not the package")
		}
	}))
	defer srv.Close()
	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Installed = func() (bool, string) { return true, "" }
	_, err := c.Install(context.Background(), t.TempDir(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotInstallable(t *testing.T) {
	c := New("", "0.2.0", "")
	c.Installed = func() (bool, string) { return false, "not from the package" }
	st := c.Check(context.Background(), true)
	if st.Installable || st.Error == "" {
		t.Errorf("status = %+v", st)
	}
}
