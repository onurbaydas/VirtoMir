package hostsetup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// sshServer accepts root with password "secret" and records the commands it is sent.
type sshServer struct {
	addr string
	key  ssh.PublicKey
	mu   sync.Mutex
	cmds []string
}

func newSSHServer(t *testing.T) *sshServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if m.User() == "root" && string(p) == "secret" {
			return nil, nil
		}
		return nil, errors.New("no")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &sshServer{addr: ln.Addr().String(), key: signer.PublicKey()}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(nc, cfg)
		}
	}()
	return srv
}

func (srv *sshServer) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, in, _ := nch.Accept()
		go func() {
			for r := range in {
				if r.Type == "exec" {
					var p struct{ Cmd string }
					_ = ssh.Unmarshal(r.Payload, &p)
					srv.mu.Lock()
					srv.cmds = append(srv.cmds, p.Cmd)
					srv.mu.Unlock()
					_ = r.Reply(true, nil)
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					ch.Close()
					return
				}
				_ = r.Reply(false, nil)
			}
		}()
	}
}

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoKey virtomir@hv-01"

func TestCopyKeyWithPassword(t *testing.T) {
	srv := newSSHServer(t)
	s, _ := newSetup(t)
	ctx := context.Background()

	if err := s.CopyKey(ctx, srv.addr, "wrong", testKey); !errors.Is(err, ErrPassword) {
		t.Errorf("wrong password: %v", err)
	}
	if err := s.CopyKey(ctx, srv.addr, "secret", testKey); err != nil {
		t.Fatal(err)
	}
	if len(srv.cmds) != 1 || !strings.Contains(srv.cmds[0], "'"+testKey+"' >> ~/.ssh/authorized_keys") || !strings.Contains(srv.cmds[0], "grep -qF 'AAAAC3NzaC1lZDI1NTE5AAAAIDemoKey'") {
		t.Errorf("commands %q", srv.cmds)
	}
	for _, bad := range []string{"ssh-ed25519 AAAA'; reboot; '", "command=\"x\" " + testKey} {
		if err := s.CopyKey(ctx, srv.addr, "secret", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("key %q: %v", bad, err)
		}
	}
}

func TestCopyKeyRefusesAChangedHostKey(t *testing.T) {
	srv := newSSHServer(t)
	s, _ := newSetup(t)
	// known_hosts has another key for this address.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	write(t, s, "/root/.ssh/known_hosts", knownhosts.Line([]string{knownhosts.Normalize(srv.addr)}, otherSigner.PublicKey())+"\n")
	if err := s.CopyKey(context.Background(), srv.addr, "secret", testKey); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "different SSH identity") {
		t.Errorf("changed host key: %v", err)
	}
	if len(srv.cmds) != 0 {
		t.Errorf("sent commands to an impostor: %q", srv.cmds)
	}
	// The right key there: fine.
	write(t, s, "/root/.ssh/known_hosts", knownhosts.Line([]string{knownhosts.Normalize(srv.addr)}, srv.key)+"\n")
	if err := s.CopyKey(context.Background(), srv.addr, "secret", testKey); err != nil {
		t.Errorf("known host: %v", err)
	}
}
