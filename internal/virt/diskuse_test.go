package virt

import (
	"errors"
	"strings"
	"testing"
)

func TestDiskConflict(t *testing.T) {
	users := map[string][]string{
		"/img/a.qcow2":    {"web"},
		"/img/base.qcow2": {"clone1 (as backing image)"},
		"/img/self.qcow2": {"me", "me (as backing image)"},
	}
	if err := diskConflict("/img/free.qcow2", "", users); err != nil {
		t.Fatalf("free disk refused: %v", err)
	}
	if err := diskConflict("/img/self.qcow2", "me", users); err != nil {
		t.Fatalf("own disk refused: %v", err)
	}
	err := diskConflict("/img/a.qcow2", "db", users)
	var op *OperationError
	if !errors.As(err, &op) || !strings.Contains(op.Message, "already used by web") {
		t.Fatalf("err = %v", err)
	}
	if err := diskConflict("/img/base.qcow2", "new", users); err == nil {
		t.Fatal("a backing image of another machine must not be written")
	}
}
