package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// GuestAgent talks to the guest agent inside a running machine.
type GuestAgent interface {
	GuestInfo(uuid string) (virt.GuestInfo, error)
	SetGuestPassword(uuid, user, password string) error
}

func (s *Server) guestAgent(w http.ResponseWriter) (GuestAgent, bool) {
	g, ok := s.backend.(GuestAgent)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the guest agent is not available"})
	}
	return g, ok
}

// getGuestInfo is separate from the machine page so a slow agent never delays the page.
func (s *Server) getGuestInfo(w http.ResponseWriter, r *http.Request) {
	g, ok := s.guestAgent(w)
	if !ok {
		return
	}
	info, err := g.GuestInfo(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, info)
}

// setGuestPassword: the password is never logged (the activity log keeps no request bodies).
func (s *Server) setGuestPassword(w http.ResponseWriter, r *http.Request) {
	g, ok := s.guestAgent(w)
	if !ok {
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := g.SetGuestPassword(r.PathValue("uuid"), body.User, body.Password); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// GuestExecer runs a program inside a running machine through its guest agent.
type GuestExecer interface {
	GuestExec(ctx context.Context, uuid string, argv []string) (virt.GuestExecResult, error)
}

// guestExec is for admins only (the default for writes): it runs anything as root inside the
// machine. It waits at most 25 seconds, under the 30 seconds of a call between members;
// longer work is started detached (systemd-run) and followed with further calls.
func (s *Server) guestExec(w http.ResponseWriter, r *http.Request) {
	g, ok := s.backend.(GuestExecer)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "running programs in machines is not available"})
		return
	}
	var body struct {
		Argv []string `json:"argv"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := g.GuestExec(ctx, r.PathValue("uuid"), body.Argv)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			s.writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "the program did not end within 25 seconds; it keeps running inside the machine"})
			return
		}
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}
