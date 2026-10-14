package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket" // ISC

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Consoler is the console part of the backend (implemented by *virt.Client).
type Consoler interface {
	ConsoleInfo(uuid string) (virt.ConsoleInfo, error)
	DialVNC(ctx context.Context, uuid string) (net.Conn, error)
	SerialConsole(ctx context.Context, uuid, device string, in io.Reader, out io.Writer) error
}

func (s *Server) consoleRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/info", s.consoleInfo)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/vnc", s.consoleVNC)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/serial", s.consoleSerial)
}

func (s *Server) consoler() (Consoler, bool) {
	c, ok := s.backend.(Consoler)
	return c, ok
}

func (s *Server) consoleInfo(w http.ResponseWriter, r *http.Request) {
	c, ok := s.consoler()
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "consoles are not available"})
		return
	}
	info, err := c.ConsoleInfo(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, info)
}

// consoleVNC bridges a WebSocket (noVNC, subprotocol "binary") to the machine's local VNC
// server. VNC itself never has to be exposed beyond the host.
func (s *Server) consoleVNC(w http.ResponseWriter, r *http.Request) {
	c, ok := s.consoler()
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "consoles are not available"})
		return
	}
	uuid := r.PathValue("uuid")
	vnc, err := c.DialVNC(r.Context(), uuid)
	if err != nil {
		s.writeError(w, err)
		return
	}
	defer vnc.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return // Accept has answered (e.g. cross-origin refused)
	}
	ws.SetReadLimit(-1)
	s.recordConsole(r, uuid, "vnc")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.watchSession(ctx, cancel, r)
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer conn.Close()

	errc := make(chan error, 2)
	go func() { _, err := io.Copy(vnc, conn); errc <- err }()
	go func() { _, err := io.Copy(conn, vnc); errc <- err }()
	select {
	case <-errc:
	case <-ctx.Done():
	}
}

// consoleSerial bridges a WebSocket (xterm.js) to a text console. ?device=<alias> picks one.
func (s *Server) consoleSerial(w http.ResponseWriter, r *http.Request) {
	c, ok := s.consoler()
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "consoles are not available"})
		return
	}
	uuid := r.PathValue("uuid")
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	s.recordConsole(r, uuid, "serial")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.watchSession(ctx, cancel, r)
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer conn.Close()

	err = c.SerialConsole(ctx, uuid, r.URL.Query().Get("device"), conn, conn)
	if err != nil && !errors.Is(err, context.Canceled) {
		msg := err.Error()
		if len(msg) > 120 {
			msg = msg[:120]
		}
		_ = ws.Close(websocket.StatusTryAgainLater, msg)
	}
}

// watchSession ends a long console connection when the browser session expires or the
// user signs out (token connections live as long as the token is valid).
func (s *Server) watchSession(ctx context.Context, cancel context.CancelFunc, r *http.Request) {
	if id := identity(r); s.auth == nil || id.Token != "" || id.Via != "" {
		return
	}
	token := auth.SessionToken(r)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.auth.Valid(token) {
				cancel()
				return
			}
		}
	}
}

func (s *Server) recordConsole(r *http.Request, uuid, kind string) {
	id := identity(r)
	s.record(activity.Entry{User: id.Name, Token: id.Token, Remote: r.RemoteAddr, Method: "GET",
		Path: "/api/v1/domains/" + uuid + "/console/" + kind, Status: http.StatusSwitchingProtocols})
}
