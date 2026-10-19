package api

import (
	"net/http"

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
