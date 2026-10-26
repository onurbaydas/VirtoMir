package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/auth"
)

func TestCorruptSigninFileMessage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "signin.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := auth.OpenExternal(p)
	if err == nil {
		t.Fatal("a corrupt signin.json must be refused")
	}
	msg := refuseSecurityFile("sign-in settings", err).Error()
	for _, want := range []string{"signin.json", "refusing to start", "silently", "move it away", "systemctl restart virtomird"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
}
