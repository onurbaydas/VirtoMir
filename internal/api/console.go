package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	DialVNCHead(ctx context.Context, uuid string, head int) (net.Conn, error)
	SerialConsole(ctx context.Context, uuid, device string, in io.Reader, out io.Writer) error
}

func (s *Server) consoleRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/info", s.consoleInfo)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/vnc", s.consoleVNC)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/serial", s.consoleSerial)
	mux.HandleFunc("GET /api/v1/domains/{uuid}/console/spice", s.consoleSPICE)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/display", s.putDisplay)
}

// Spicer is the SPICE part of the backend (ADR-0046); a backend without it keeps VNC and
// the serial console.
type Spicer interface {
	DialSPICE(ctx context.Context, uuid string) (net.Conn, error)
	SetDisplay(uuid, protocol string) error
}

func (s *Server) spicer(w http.ResponseWriter) (Spicer, bool) {
	c, ok := s.backend.(Spicer)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "SPICE is not available"})
	}
	return c, ok
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
	head, _ := strconv.Atoi(r.URL.Query().Get("head")) // ?head=1 for the second monitor
	vnc, err := c.DialVNCHead(r.Context(), uuid, head)
	if err != nil {
		s.writeError(w, err)
		return
	}
	defer vnc.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return // Accept has answered (e.g. cross-origin refused)
	}
	ws.SetReadLimit(consoleReadLimit)
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

// consoleSPICE bridges one WebSocket of spice-html5 (subprotocol "binary") to the machine's
// local SPICE server. spice-html5 opens one per SPICE channel (main, display, inputs,
// cursor, playback); only the main one, which comes first, is written to the activity log.
func (s *Server) consoleSPICE(w http.ResponseWriter, r *http.Request) {
	c, ok := s.spicer(w)
	if !ok {
		return
	}
	uuid := r.PathValue("uuid")
	sp, err := c.DialSPICE(r.Context(), uuid)
	if err != nil {
		s.writeError(w, err)
		return
	}
	defer sp.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	ws.SetReadLimit(consoleReadLimit)
	if s.firstSpiceChannel(identity(r).Name, uuid) {
		s.recordConsole(r, uuid, "spice")
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.watchSession(ctx, cancel, r)
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer conn.Close()

	errc := make(chan error, 2)
	go func() { _, err := io.Copy(sp, conn); errc <- err }()
	go func() { _, err := io.Copy(conn, sp); errc <- err }()
	select {
	case <-errc:
	case <-ctx.Done():
	}
}

var (
	spiceSeenMu sync.Mutex
	spiceSeen   = map[string]time.Time{}
)

// firstSpiceChannel: the first channel of a SPICE session by user on uuid (others follow
// within seconds).
func (s *Server) firstSpiceChannel(user, uuid string) bool {
	spiceSeenMu.Lock()
	defer spiceSeenMu.Unlock()
	k, now := user+"/"+uuid, time.Now()
	for key, t := range spiceSeen {
		if now.Sub(t) > time.Minute {
			delete(spiceSeen, key)
		}
	}
	first := now.Sub(spiceSeen[k]) > 10*time.Second
	spiceSeen[k] = now
	return first
}

// putDisplay switches a machine's console between VNC and SPICE (from its next start).
func (s *Server) putDisplay(w http.ResponseWriter, r *http.Request) {
	c, ok := s.spicer(w)
	if !ok {
		return
	}
	var body struct {
		Protocol string `json:"protocol"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := c.SetDisplay(r.PathValue("uuid"), body.Protocol); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
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
	ws.SetReadLimit(consoleReadLimit)
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

// sessionWatchEvery is how often an open console re-checks its session or token (a var for tests).
var sessionWatchEvery = 30 * time.Second

// Browsers only send small input messages (keys, pointer, clipboard text), so a console
// socket never needs to accept more than this per message.
const consoleReadLimit = 1 << 20

// watchSession ends a long console connection when the browser session expires or the
// user signs out, or when the API token that opened it is revoked or expires.
func (s *Server) watchSession(ctx context.Context, cancel context.CancelFunc, r *http.Request) {
	id := identity(r)
	if s.auth == nil || id.Via != "" {
		return
	}
	token := auth.SessionToken(r)
	valid := func() bool { return s.auth.Valid(token) }
	if id.Token != "" {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		valid = func() bool { _, ok := s.authenticate(r, bearer, true); return ok }
	}
	t := time.NewTicker(sessionWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !valid() {
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
