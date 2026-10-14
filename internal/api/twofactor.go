package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/skip2/go-qrcode" // MIT

	"github.com/onurbaydas/virtomir/internal/auth"
)

// Two-factor sign-in: everyone manages their own under /api/v1/account/2fa; admins can see
// who has it and reset it for someone who lost their phone (/api/v1/twofactor/users).

func (s *Server) twoFactorRoutes(mux *routeMux) {
	mux.HandleFunc("POST /api/v1/auth/2fa", s.loginCode)
	mux.HandleFunc("GET /api/v1/account/2fa", s.twoFactorStatus)
	mux.HandleFunc("POST /api/v1/account/2fa/setup", s.twoFactorSetup)
	mux.HandleFunc("POST /api/v1/account/2fa/confirm", s.twoFactorConfirm)
	mux.HandleFunc("POST /api/v1/account/2fa/disable", s.twoFactorDisable)
	mux.HandleFunc("GET /api/v1/twofactor/users", s.twoFactorUsers)
	mux.HandleFunc("DELETE /api/v1/twofactor/users/{user}", s.twoFactorReset)
}

// twoFactor is the store, or nil with a 409 already written (sign-in disabled).
func (s *Server) twoFactor(w http.ResponseWriter) *auth.TwoFactor {
	if s.auth == nil || s.auth.TwoFactor() == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "two-factor sign-in needs sign-in to be enabled"})
		return nil
	}
	return s.auth.TwoFactor()
}

// ownAccount refuses API tokens and forwarded requests: second factors are managed in the
// browser, on the host one signed in to.
func (s *Server) ownAccount(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id := identity(r)
	if id.Token != "" || id.Via != "" {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "manage two-factor sign-in in the web UI of the host you sign in to"})
		return id, false
	}
	if strings.HasSuffix(id.Name, auth.SuffixSSO) {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "single sign-on accounts use the second factor of their identity provider"})
		return id, false
	}
	return id, true
}

func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "sign-in is disabled"})
		return
	}
	var b struct {
		Pending string `json:"pending"`
		Code    string `json:"code"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	id, token, err := s.auth.CompleteLogin(r.RemoteAddr, b.Pending, b.Code)
	if err != nil {
		s.recordSignIn(id.Name, r.RemoteAddr, http.StatusUnauthorized, "second factor: "+err.Error())
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrTooManyAttempts) {
			status = http.StatusTooManyRequests
		}
		s.writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	s.recordSignIn(id.Name, r.RemoteAddr, http.StatusOK, "")
	auth.SetCookie(w, r, token)
	s.writeJSON(w, http.StatusOK, sessionInfo{Mode: s.mode(), User: id.Name, Role: id.Role})
}

func (s *Server) twoFactorStatus(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || s.auth.TwoFactor() == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	id := identity(r)
	st := s.auth.TwoFactor().Status(id.Name)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"available": !strings.HasSuffix(id.Name, auth.SuffixSSO),
		"enabled":   st.Enabled, "since": st.Since, "recoveryLeft": st.RecoveryLeft, "pendingSetup": st.PendingSetup,
	})
}

func (s *Server) twoFactorSetup(w http.ResponseWriter, r *http.Request) {
	tf := s.twoFactor(w)
	if tf == nil {
		return
	}
	id, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	issuer := "VirtoMir"
	if s.hostName != "" {
		issuer += " " + s.hostName
	}
	secret, uri, err := tf.Begin(id.Name, issuer)
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret, "uri": uri, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

type codeBody struct {
	Code string `json:"code"`
}

func (s *Server) twoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	tf := s.twoFactor(w)
	if tf == nil {
		return
	}
	id, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	var b codeBody
	if !s.decode(w, r, &b) {
		return
	}
	codes, err := tf.Confirm(id.Name, b.Code)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"recovery": codes})
}

// twoFactorDisable needs a current code (or a recovery code): a session left open on someone
// else's screen must not be enough to weaken the account.
func (s *Server) twoFactorDisable(w http.ResponseWriter, r *http.Request) {
	tf := s.twoFactor(w)
	if tf == nil {
		return
	}
	id, ok := s.ownAccount(w, r)
	if !ok {
		return
	}
	var b codeBody
	if !s.decode(w, r, &b) {
		return
	}
	if tf.Status(id.Name).Enabled {
		if err := tf.Verify(id.Name, b.Code); err != nil {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := tf.Disable(id.Name); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) twoFactorUsers(w http.ResponseWriter, _ *http.Request) {
	users := []string{}
	if s.auth != nil && s.auth.TwoFactor() != nil {
		users = append(users, s.auth.TwoFactor().Users()...)
	}
	slices.Sort(users)
	s.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// twoFactorReset: an admin turns it off for someone who lost their authenticator.
func (s *Server) twoFactorReset(w http.ResponseWriter, r *http.Request) {
	tf := s.twoFactor(w)
	if tf == nil {
		return
	}
	if err := tf.Disable(r.PathValue("user")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
