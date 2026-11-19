package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrPassword marks a password the target did not accept.
var ErrPassword = errors.New("password not accepted")

// CopyKey signs in to host as root with password, once, and adds key to root's
// authorized_keys there (what ssh-copy-id does). The password is used for this one sign-in
// and kept nowhere. A host whose SSH identity is already in root's known_hosts must still
// present it: a changed one is refused.
func (s *Setup) CopyKey(ctx context.Context, host, password, key string) error {
	key = strings.TrimSpace(key)
	if !pubKeyRE.MatchString(key) {
		return fmt.Errorf("%w: that is not an SSH public key", ErrInvalid)
	}
	if password == "" {
		return fmt.Errorf("%w: the password is empty", ErrInvalid)
	}
	check, err := s.hostKeyCheck()
	if err != nil {
		return err
	}
	answer := func(_, _ string, qs []string, _ []bool) ([]string, error) {
		out := make([]string, len(qs))
		for i := range qs {
			out[i] = password
		}
		return out, nil
	}
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, "22")
	}
	// The password method first, keyboard-interactive (PAM) on a fresh connection after: some
	// servers answer the second method in a way the client takes for a broken handshake.
	var client *ssh.Client
	for _, auth := range []ssh.AuthMethod{ssh.Password(password), ssh.KeyboardInteractive(answer)} {
		client, err = signIn(ctx, addr, &ssh.ClientConfig{User: "root", Auth: []ssh.AuthMethod{auth}, HostKeyCallback: check, Timeout: 15 * time.Second})
		var mismatch *knownhosts.KeyError
		switch {
		case err == nil:
		case errors.As(err, &mismatch) && len(mismatch.Want) > 0:
			return fmt.Errorf("%w: %s presents a different SSH identity than the one this host knows; someone may be in between", ErrInvalid, host)
		case errors.Is(err, errDial):
			return fmt.Errorf("cannot reach %s over SSH: %w", host, err)
		default:
			continue
		}
		break
	}
	if client == nil {
		return fmt.Errorf("%w: %s did not accept root's password (or does not let root sign in with one)", ErrPassword, host)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	// key matches pubKeyRE (no quotes, no shell characters), so single quotes are safe.
	cmd := "umask 077; mkdir -p ~/.ssh && touch ~/.ssh/authorized_keys && " +
		"(grep -qF '" + strings.Fields(key)[1] + "' ~/.ssh/authorized_keys || echo '" + key + "' >> ~/.ssh/authorized_keys) && " +
		"(command -v restorecon >/dev/null && restorecon -R ~/.ssh || true)"
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("add the key on %s: %s", host, lastLines(strings.TrimSpace(string(out))+" "+err.Error(), 3))
	}
	return nil
}

var errDial = errors.New("unreachable")

// signIn opens an SSH connection to addr; a connection that cannot be made wraps errDial.
func signIn(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDial, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// hostKeyCheck accepts a host it has not met (the ssh that follows records it) and refuses
// one whose identity changed.
func (s *Setup) hostKeyCheck() (ssh.HostKeyCallback, error) {
	p := s.path("/root/.ssh/known_hosts")
	if !fileExists(p) {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	known, err := knownhosts.New(p)
	if err != nil {
		return nil, fmt.Errorf("read root's known_hosts: %w", err)
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := known(host, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			return nil // never met: trust it now, like StrictHostKeyChecking=accept-new
		}
		return err
	}, nil
}
