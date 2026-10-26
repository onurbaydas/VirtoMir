package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestWriteErrorBusy(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	s.writeError(rec, fmt.Errorf("start: %w", &virt.BusyError{Op: "backup"}))
	if rec.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Another operation (backup) is running on this machine.") {
		t.Errorf("body = %s", rec.Body.String())
	}
}
