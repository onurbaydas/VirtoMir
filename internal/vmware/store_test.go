package vmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vmware.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func set(t *testing.T, s *Store, c Connection) {
	t.Helper()
	if err := s.SetConfig(c); err != nil {
		t.Fatalf("SetConfig(%v): %v", c.URL, err)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	s, _ := open(t)
	if s.Configured() {
		t.Fatal("a missing file must be an empty configuration, not an error")
	}
	if c := s.Config(); c.URL != "" || c.Password != "" {
		t.Fatalf("got %+v", c)
	}
}

func TestRoundTripKeepsPassword(t *testing.T) {
	s, path := open(t)
	c := Connection{URL: "https://vcenter.example.com", Username: "administrator@vsphere.local", Password: "hunter2"}
	set(t, s, c)

	// The API only ever sees the mask.
	got := s.Config()
	if got.Password != Secret {
		t.Fatalf("Config().Password = %q, want the mask", got.Password)
	}

	// Saving the masked form back (echo) keeps the stored password…
	set(t, s, got)
	// …and it survives a reload from disk.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stored := s2.current()
	if stored != (Connection{URL: c.URL, Username: c.Username, Password: "hunter2"}) {
		t.Fatalf("after masked re-save: %+v", stored)
	}
	if !s2.Configured() {
		t.Fatal("Configured() = false after storing")
	}
	if runtime.GOOS == "linux" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode is %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestInvalidURLsRefused(t *testing.T) {
	s, _ := open(t)
	set(t, s, Connection{URL: "https://vcenter.example.com", Username: "root", Password: "p"})
	for _, url := range []string{
		"",                    // empty (also via Validate)
		"vcenter.example.com", // no scheme
		"http://vcenter.example.com",
		"https://root:pw@vcenter.example.com",
		"https://vcenter.example.com/sdk",
		"https://vcenter.example.com?x=1",
		"://bad",
	} {
		if err := s.Validate(Connection{URL: url, Username: "root", Password: "p"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(URL:%q) = %v, want ErrInvalid", url, err)
		}
		if url != "" {
			err := s.SetConfig(Connection{URL: url, Username: "root", Password: "p"})
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("SetConfig(URL:%q) = %v, want ErrInvalid", url, err)
			}
		}
	}
	if s.current().URL != "https://vcenter.example.com" {
		t.Errorf("a refused save changed the stored config: %+v", s.current())
	}
}

func TestThumbprint(t *testing.T) {
	s, _ := open(t)
	sha1 := strings.Repeat("ab", 20)
	sha1Colons := strings.Repeat("AB:", 19) + "AB"
	sha256 := strings.Repeat("cd", 32)
	for _, tp := range []string{"", sha1, sha1Colons, sha256} {
		set(t, s, Connection{URL: "https://esxi.example.com", Username: "root", Password: "p", Thumbprint: tp, Insecure: tp != ""})
	}
	for _, tp := range []string{"ab", "zz", strings.Repeat("ab", 20) + "f", strings.Repeat("ab", 31), "AB:CD:EF"} {
		err := s.SetConfig(Connection{URL: "https://esxi.example.com", Username: "root", Password: "p", Thumbprint: tp})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("thumbprint %q accepted, want ErrInvalid", tp)
		}
	}
	// Insecure without a thumbprint is refused: no skipping verification by trust alone.
	err := s.SetConfig(Connection{URL: "https://esxi.example.com", Username: "root", Password: "p", Insecure: true})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Insecure without thumbprint = %v, want ErrInvalid", err)
	}
}

func TestEmptyPasswordOverExisting(t *testing.T) {
	s, _ := open(t)
	set(t, s, Connection{URL: "https://vcenter.example.com", Username: "root", Password: "hunter2"})
	// An empty password sent over a stored one (not the Secret echo) is refused…
	err := s.SetConfig(Connection{URL: "https://vcenter.example.com", Username: "root"})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("empty password over existing = %v, want ErrInvalid", err)
	}
	err = s.SetConfig(Connection{URL: "https://vcenter.example.com", Username: "root", Password: ""})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("explicit empty password over existing = %v, want ErrInvalid", err)
	}
	if s.current().Password != "hunter2" {
		t.Errorf("refused save changed the password to %q", s.current().Password)
	}
	// …and a fresh store demands one too.
	s2, _ := open(t)
	if err := s2.SetConfig(Connection{URL: "https://vcenter.example.com", Username: "root"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no password = %v, want ErrInvalid", err)
	}
}

func TestClearWithEmptyURL(t *testing.T) {
	s, _ := open(t)
	set(t, s, Connection{URL: "https://vcenter.example.com", Username: "root", Password: "hunter2"})
	// An explicit empty URL clears the whole configuration…
	set(t, s, Connection{})
	if s.Configured() || s.current().Password != "" {
		t.Fatalf("clearing left %+v behind", s.current())
	}
	// …but Validate still refuses an empty URL.
	if err := s.Validate(Connection{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate(empty) = %v, want ErrInvalid", err)
	}
}

func TestStringRedactsPassword(t *testing.T) {
	c := Connection{URL: "https://vcenter.example.com", Username: "root", Password: "hunter2", Insecure: true}
	out := fmt.Sprint(c)
	if strings.Contains(out, "hunter2") {
		t.Errorf("String() leaks the password: %s", out)
	}
	if !strings.Contains(out, Secret) {
		t.Errorf("String() does not show the mask: %s", out)
	}
}

func TestOpenCorruptedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not-valid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err == nil {
		t.Fatal("expected error opening corrupted JSON, got nil")
	}
	if store != nil {
		t.Fatalf("expected nil Store on error, got %+v", store)
	}
}
