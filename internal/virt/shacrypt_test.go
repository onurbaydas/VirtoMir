package virt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSHA512Crypt(t *testing.T) {
	// The test vectors of Drepper's specification (default rounds).
	for _, c := range []struct {
		key, salt string
		rounds    int
		want      string
	}{
		{"Hello world!", "saltstring", 5000, "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		// rounds=5000 spelled out in the spec; the salt is cut to 16 characters.
		{"This is just a test", "toolongsaltstring", 5000, "$6$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0"},
		// The spec's rounds=10000 vector.
		{"Hello world!", "saltstringsaltstring", 10000, "$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
		// What VirtoMir writes, checked against libxcrypt's crypt(3).
		{"pw", "abcdefghijklmnop", cryptRounds, "$6$rounds=656000$abcdefghijklmnop$vlzqoGD.PPCRIAJoqyiLJycRN2d4gUQcXq8MV9X6K22FhTrqrthsvWGIA77qA1UOBLYAvaDQHTA5oE/Sa3kHT0"},
	} {
		if got := sha512Crypt([]byte(c.key), []byte(c.salt), c.rounds); got != c.want {
			t.Errorf("sha512Crypt(%q, %q) = %s", c.key, c.salt, got)
		}
	}
}

func TestSetRootPassword(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	shadow := filepath.Join(root, "etc", "shadow")
	os.WriteFile(shadow, []byte("root:*:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\n"), 0o640)
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := setRootPassword(r, "virtomir-test"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(shadow)
	lines := strings.Split(string(raw), "\n")
	if !strings.HasPrefix(lines[0], "root:$6$") || !strings.HasSuffix(lines[0], ":19000:0:99999:7:::") || lines[1] != "daemon:*:19000:0:99999:7:::" {
		t.Errorf("shadow = %q", raw)
	}
}

func TestConsoleOnlyInittab(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p := filepath.Join(dir, "inittab")
	os.WriteFile(p, []byte("::respawn:/sbin/getty 38400 console\ntty1::respawn:/sbin/getty 38400 tty1\n::shutdown:/sbin/openrc shutdown\n"), 0o644)
	if err := consoleOnly(r, "inittab"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	l := strings.Split(string(raw), "\n")
	if l[0] != "::respawn:/sbin/getty 38400 console" || !strings.HasPrefix(l[1], "#tty1::") || l[2] != "::shutdown:/sbin/openrc shutdown" {
		t.Errorf("inittab = %q", raw)
	}
	if err := consoleOnly(r, "missing"); err != nil {
		t.Error(err)
	}
}

// An image whose etc/shadow is a symlink out of its rootfs must not reach the host's file.
func TestSetRootPasswordStaysInRootfs(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "shadow")
	os.WriteFile(outside, []byte("root:*:19000:0:99999:7:::\n"), 0o640)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "etc", "shadow")); err != nil {
		t.Skip(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := setRootPassword(r, "x"); err == nil {
		t.Error("followed a symlink out of the rootfs")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "root:*:19000:0:99999:7:::\n" {
		t.Errorf("the file outside changed: %q", raw)
	}
}
