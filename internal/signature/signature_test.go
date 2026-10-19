package signature

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestEmbeddedKeys(t *testing.T) {
	for name, k := range map[string]*Keyring{"virtomir": VirtoMir(), "lxc": LXCImages()} {
		if len(k.keys) != 1 {
			t.Errorf("%s: %d keys", name, len(k.keys))
		}
	}
	fpr := func(k *Keyring) string { return strings.ToUpper(hex.EncodeToString(k.keys[0].PrimaryKey.Fingerprint)) }
	if got := fpr(VirtoMir()); got != "F54B0716A240B22E947B8DC6E410753BFE33A9F0" {
		t.Errorf("VirtoMir key fingerprint %s", got)
	}
	if got := fpr(LXCImages()); got != "E7FB0CAEC8173D669066514CBAEFF88C22F6E216" {
		t.Errorf("LXC key fingerprint %s", got)
	}
}

// A real SHA256SUMS of images.linuxcontainers.org with its signature.
func TestLXCSignature(t *testing.T) {
	sums, err := os.ReadFile("testdata/lxc-SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile("testdata/lxc-SHA256SUMS.asc")
	if err != nil {
		t.Fatal(err)
	}
	if err := LXCImages().Verify(bytes.NewReader(sums), bytes.NewReader(sig)); err != nil {
		t.Fatalf("real image signature: %v", err)
	}
	changed := bytes.Replace(sums, []byte("c261"), []byte("0261"), 1)
	if err := LXCImages().Verify(bytes.NewReader(changed), bytes.NewReader(sig)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed checksum accepted: %v", err)
	}
	if err := VirtoMir().Verify(bytes.NewReader(sums), bytes.NewReader(sig)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("signature of another key accepted: %v", err)
	}
}

// An ed25519 key like VirtoMir's, made for the test: armored and binary signatures.
func TestEd25519(t *testing.T) {
	e, err := openpgp.NewEntity("test", "", "test@example.org", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var pub bytes.Buffer
	if err := e.Serialize(&pub); err != nil {
		t.Fatal(err)
	}
	ring := &Keyring{name: "test", keys: openpgp.EntityList{e}}
	data := []byte("3f2a…  virtomir_1.0.0_amd64.deb\n")
	var armored, binary bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&armored, e, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	if err := openpgp.DetachSign(&binary, e, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	for name, sig := range map[string][]byte{"armored": armored.Bytes(), "binary": binary.Bytes()} {
		if err := ring.Verify(bytes.NewReader(data), bytes.NewReader(sig)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if err := ring.Verify(strings.NewReader("other"), bytes.NewReader(sig)); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: other data accepted: %v", name, err)
		}
	}
}
