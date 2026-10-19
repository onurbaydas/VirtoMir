// Package signature checks detached OpenPGP signatures of what VirtoMir downloads and
// trusts: its own updates (signed with the repository key, ADR-0048) and container images
// (signed by the Linux Containers image server). A SHA-256 published next to a file only
// proves the download is intact; whoever can replace the file can replace its checksum
// too. The signature proves who published the checksum.
package signature

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp"
)

var (
	//go:embed keys/virtomir.asc
	virtomirKey []byte
	//go:embed keys/lxc-images.asc
	lxcKey []byte
)

// ErrBadSignature: the data is not signed by the expected key, or was changed after signing.
var ErrBadSignature = errors.New("the signature does not match")

// Keyring holds the public keys a signature may come from.
type Keyring struct {
	name string
	keys openpgp.EntityList
}

// VirtoMir is the key VirtoMir's releases and package repositories are signed with.
func VirtoMir() *Keyring { return mustKeyring("VirtoMir release", virtomirKey) }

// LXCImages is the key of the Linux Containers image server (images.linuxcontainers.org).
func LXCImages() *Keyring { return mustKeyring("Linux Containers image", lxcKey) }

// NewKeyring reads armored public keys.
func NewKeyring(name string, armored []byte) (*Keyring, error) {
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("%s key: %w", name, err)
	}
	return &Keyring{name: name, keys: keys}, nil
}

func mustKeyring(name string, armored []byte) *Keyring {
	k, err := NewKeyring(name, armored)
	if err != nil {
		panic(err) // the embedded keys are tested
	}
	return k
}

// Verify checks that sig, an armored or binary detached signature, signs data.
func (k *Keyring) Verify(data, sig io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(sig, 64<<10))
	if err != nil {
		return err
	}
	check := openpgp.CheckDetachedSignature
	if bytes.Contains(raw, []byte("-----BEGIN PGP SIGNATURE-----")) {
		check = openpgp.CheckArmoredDetachedSignature
	}
	if _, err := check(k.keys, data, bytes.NewReader(raw), nil); err != nil {
		return fmt.Errorf("%w: not signed with the %s key (%v)", ErrBadSignature, k.name, err)
	}
	return nil
}
