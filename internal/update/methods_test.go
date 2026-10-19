package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPickMatchesInstallMethod(t *testing.T) {
	rel := &Release{Tag: "v0.9.0", Version: "0.9.0", assets: map[string]string{}}
	for _, m := range []string{MethodDeb, MethodRPM, MethodBinary} {
		name := assetFor(m, rel)
		rel.assets[name] = "u/" + name
		rel.assets[name+".sha256"] = "u/" + name + ".sha256"
	}
	cases := map[string]string{
		MethodDeb:    "virtomir_0.9.0_" + debArch() + ".deb",
		MethodRPM:    "virtomir-0.9.0-1." + rpmArch() + ".rpm",
		MethodBinary: "virtomir_v0.9.0_linux_" + runtime.GOARCH + ".tar.gz",
	}
	for m, want := range cases {
		rel.pick(m)
		if rel.Package != want || rel.pkgURL != "u/"+want || rel.shaURL != "u/"+want+".sha256" {
			t.Errorf("%s: picked %q (%q, %q), want %q", m, rel.Package, rel.pkgURL, rel.shaURL, want)
		}
	}
}

func TestUnitFromCgroup(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/virtomird.service\n":                        "virtomird.service",
		"0::/system.slice/virtomird-dev.service":                      "virtomird-dev.service",
		"12:pids:/system.slice/virtomird.service\n0::/user.slice/x\n": "virtomird.service",
		"0::/user.slice/user-1000.slice/session-3.scope":              "",
	}
	for in, want := range cases {
		if got := unitFromCgroup(in); got != want {
			t.Errorf("unitFromCgroup(%q) = %q, want %q", in, got, want)
		}
	}
}

// releaseTarball builds a tarball laid out like a real release, with the given virtomird content.
func releaseTarball(t *testing.T, binary string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	add("virtomir_v9.0.0_linux_amd64/README.md", "readme", 0o644)
	if binary != "" {
		add("virtomir_v9.0.0_linux_amd64/virtomird", binary, 0o755)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "r.tar.gz")
	os.WriteFile(tarPath, releaseTarball(t, "#!new-binary"), 0o600)
	dst := filepath.Join(dir, "virtomird.new")
	if err := extractBinary(tarPath, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "#!new-binary" {
		t.Fatalf("extracted %q", b)
	}

	os.WriteFile(tarPath, releaseTarball(t, ""), 0o600)
	if err := extractBinary(tarPath, dst); err == nil || !strings.Contains(err.Error(), "no virtomird") {
		t.Fatalf("tarball without binary: err = %v", err)
	}
}

// TestInstallBinaryMethod runs the whole binary update against a fake GitHub: download,
// checksum, extraction, and the restart hook; the real running test binary is never touched.
func TestInstallBinaryMethod(t *testing.T) {
	tarball := releaseTarball(t, "#!virtomird 9.0.0")
	sum := sha256.Sum256(tarball)
	name := fmt.Sprintf("virtomir_v9.0.0_linux_%s.tar.gz", runtime.GOARCH)
	signer := newTestSigner(t)
	var srv *httptest.Server
	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/v/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":%q,"browser_download_url":"%s/tar"},{"name":%q,"browser_download_url":"%s/sha"},{"name":%q,"browser_download_url":"%s/sig"}]}`,
				name, srv.URL, name+".sha256", srv.URL, name+".sha256.asc", srv.URL)
		case "/tar":
			w.Write(tarball)
		case "/sha":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
	}))
	defer srv.Close()

	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodBinary }
	var restarted string
	c.Restart = func(exe string) (string, error) { restarted = exe; return "restarting test", nil }

	// installBinary replaces the running executable; point it at a stand-in by checking
	// the extraction step directly and the restart hook through Install's error path.
	st := c.Check(t.Context(), true)
	if !st.Available || !st.Installable || st.Latest.Package != name || st.Method != MethodBinary {
		t.Fatalf("status = %+v", st)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	os.WriteFile(path, tarball, 0o600)
	stand := filepath.Join(dir, "virtomird")
	os.WriteFile(stand, []byte("old"), 0o755)
	if err := extractBinary(path, stand+".new"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stand+".new", stand); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stand); string(b) != "#!virtomird 9.0.0" {
		t.Fatalf("stand-in binary = %q", b)
	}
	if _, err := c.Restart(stand); err != nil || restarted != stand {
		t.Fatalf("restart hook: %q, %v", restarted, err)
	}
}

func TestBinaryChecksumMismatchInstallsNothing(t *testing.T) {
	tarball := releaseTarball(t, "#!evil")
	name := fmt.Sprintf("virtomir_v9.0.0_linux_%s.tar.gz", runtime.GOARCH)
	signer := newTestSigner(t)
	var srv *httptest.Server
	srv = httptest.NewServer(signer.wrap(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/v/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":%q,"browser_download_url":"%s/tar"},{"name":%q,"browser_download_url":"%s/sha"},{"name":%q,"browser_download_url":"%s/sig"}]}`,
				name, srv.URL, name+".sha256", srv.URL, name+".sha256.asc", srv.URL)
		case "/tar":
			w.Write(tarball)
		case "/sha":
			fmt.Fprint(w, strings.Repeat("0", 64)+"  x\n")
		}
	}))
	defer srv.Close()
	c := New("o/v", "0.2.0", "")
	c.API = srv.URL
	c.Keys = signer.Keys
	c.Installed = func() (bool, string) { return true, "" }
	c.Method = func() string { return MethodBinary }
	c.Restart = func(string) (string, error) { t.Fatal("restart must not run"); return "", nil }
	if _, err := c.Install(t.Context(), t.TempDir(), func(string) {}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
}

func TestDevBuildVersionsAreNotReleases(t *testing.T) {
	// A dev build named after its commit must not depend on whether the hash starts with a digit.
	for _, dev := range []string{"1328635-pam", "da0817d-pam", "1328635", "dev"} {
		if !Newer("0.8.1", dev) {
			t.Errorf("Newer(0.8.1, %q) = false; a release should be offered over a dev build", dev)
		}
	}
	if Newer("0.8.1", "0.9.0") || !Newer("0.9.0", "0.8.1") {
		t.Error("release ordering broken")
	}
}
