package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// consoleBackend adds a fake VNC server and serial console to fakeBackend.
type consoleBackend struct {
	*fakeBackend
	vncAddr string
}

func (c consoleBackend) ConsoleInfo(string) (virt.ConsoleInfo, error) {
	return virt.ConsoleInfo{Running: true, VNC: &virt.VNCInfo{}, Serial: []string{"serial0"}}, nil
}

func (c consoleBackend) DialVNC(ctx context.Context, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", c.vncAddr)
}

func (c consoleBackend) DialVNCHead(ctx context.Context, uuid string, _ int) (net.Conn, error) {
	return c.DialVNC(ctx, uuid)
}

func (c consoleBackend) SerialConsole(ctx context.Context, _, _ string, in io.Reader, out io.Writer) error {
	_, _ = out.Write([]byte("login: "))
	buf := make([]byte, 64)
	n, err := in.Read(buf)
	if err != nil {
		return err
	}
	_, _ = out.Write(bytes.ToUpper(buf[:n])) // "guest" echoes in upper case
	<-ctx.Done()
	return ctx.Err()
}

func TestVNCBridge(t *testing.T) {
	// Fake VNC server: greets with the RFB banner, then echoes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("RFB 003.008\n"))
		_, _ = io.Copy(c, c)
	}()

	srv := httptest.NewServer(NewServer(Options{
		Backend: consoleBackend{&fakeBackend{}, ln.Addr().String()}, Live: &fakeLive{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/domains/u1/console/vnc",
		&websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	if resp.Header.Get("Sec-WebSocket-Protocol") != "binary" {
		t.Errorf("subprotocol = %q (noVNC needs binary)", resp.Header.Get("Sec-WebSocket-Protocol"))
	}
	_, msg, err := ws.Read(ctx)
	if err != nil || string(msg) != "RFB 003.008\n" {
		t.Fatalf("banner = %q %v", msg, err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	_, msg, err = ws.Read(ctx)
	if err != nil || !bytes.Equal(msg, []byte{1, 2, 3}) {
		t.Errorf("echo = %v %v", msg, err)
	}
}

func TestSerialBridge(t *testing.T) {
	srv := httptest.NewServer(NewServer(Options{
		Backend: consoleBackend{&fakeBackend{}, ""}, Live: &fakeLive{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/domains/u1/console/serial", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	_, msg, _ := ws.Read(ctx)
	if string(msg) != "login: " {
		t.Fatalf("prompt = %q", msg)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("root"))
	if _, msg, _ = ws.Read(ctx); string(msg) != "ROOT" {
		t.Errorf("echo = %q", msg)
	}
}

func TestConsoleCrossOriginRefused(t *testing.T) {
	srv := httptest.NewServer(NewServer(Options{
		Backend: consoleBackend{&fakeBackend{}, "127.0.0.1:1"}, Live: &fakeLive{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Origin", "https://evil.example")
	_, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/domains/u1/console/serial",
		&websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		t.Error("cross-origin console must be refused")
	}
}
