//go:build linux && cgo && pam

package auth

// Uses github.com/msteinert/pam/v2 (BSD-2-Clause) for the libpam binding.

import (
	"context"
	"errors"
	"fmt"

	"github.com/msteinert/pam/v2"
)

// PAMAvailable reports whether this binary was built with PAM support (-tags pam, cgo).
const PAMAvailable = true

// PAM authenticates against the PAM service (default "virtomir", installed as
// /etc/pam.d/virtomir; it includes the system's common-auth/common-account).
type PAM struct{ Service string }

func (p PAM) Authenticate(_ context.Context, user, password string) error {
	service := p.Service
	if service == "" {
		service = "virtomir"
	}
	t, err := pam.StartFunc(service, user, func(s pam.Style, msg string) (string, error) {
		switch s {
		case pam.PromptEchoOff, pam.PromptEchoOn:
			return password, nil
		case pam.ErrorMsg, pam.TextInfo:
			return "", nil
		}
		return "", errors.New("unsupported PAM conversation")
	})
	if err != nil {
		return fmt.Errorf("start PAM: %w", err)
	}
	defer func() { _ = t.End() }()
	if err := t.Authenticate(pam.DisallowNullAuthtok); err != nil {
		return ErrBadCredentials
	}
	// Expired or locked accounts must not get in either.
	if err := t.AcctMgmt(pam.DisallowNullAuthtok); err != nil {
		return ErrBadCredentials
	}
	return nil
}
