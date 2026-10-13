//go:build !(linux && cgo && pam)

package auth

import (
	"context"
	"errors"
)

// PAMAvailable reports whether this binary was built with PAM support (-tags pam, cgo).
const PAMAvailable = false

// PAM is unavailable in this build; see pam.go.
type PAM struct{ Service string }

func (PAM) Authenticate(context.Context, string, string) error {
	return errors.New("this virtomird was built without PAM support (build with CGO_ENABLED=1 -tags pam)")
}
