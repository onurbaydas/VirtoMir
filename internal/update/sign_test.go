package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/onurbaydas/virtomir/internal/signature"
)

// testSigner stands in for the release key: Keys verifies what wrap signs.
type testSigner struct {
	e    *openpgp.Entity
	Keys *signature.Keyring
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	e, err := openpgp.NewEntity("release", "", "release@example.org", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var pub bytes.Buffer
	w, _ := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	keys, err := signature.NewKeyring("test release", pub.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{e: e, Keys: keys}
}

// wrap serves /sig: the signature of whatever h serves at /sha.
// edgePublished: the fake GitHub has a build of main (tests of the main channel set it).
var edgePublished bool

func (s *testSigner) wrap(t *testing.T, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/tags/"+EdgeTag) && !edgePublished {
			http.NotFound(w, r) // like GitHub before CI published a build of main
			return
		}
		if r.URL.Path != "/sig" {
			h(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/sha", nil))
		if err := openpgp.ArmoredDetachSign(w, s.e, bytes.NewReader(rec.Body.Bytes()), nil); err != nil {
			t.Error(err)
		}
	}
}

func TestInstallRefusesUnsignedOrForeignChecksum(t *testing.T) {
	sum := strings.Repeat("a", 64) + "  virtomir.deb\n"
	for _, tc := range []struct {
		name, assets, want string
		sig                func(*testSigner) string
	}{
		{"no signature", "", "not signed", nil},
		{"another key", `,{"name":"virtomir_9.0.0_ARCH.deb.sha256.asc","browser_download_url":"URL/sig"}`, "signature does not match",
			func(*testSigner) string {
				other := newTestSigner(t)
				var b bytes.Buffer
				openpgp.ArmoredDetachSign(&b, other.e, strings.NewReader(sum), nil)
				return b.String()
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSigner(t)
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/v/releases/latest":
					extra := strings.NewReplacer("ARCH", debArch(), "URL", srv.URL).Replace(tc.assets)
					w.Write([]byte(strings.NewReplacer("ARCH", debArch(), "URL", srv.URL).Replace(
						`{"tag_name":"v9.0.0","assets":[{"name":"virtomir_9.0.0_ARCH.deb","browser_download_url":"URL/deb"},
						{"name":"virtomir_9.0.0_ARCH.deb.sha256","browser_download_url":"URL/sha"}` + extra + `]}`)))
				case "/sha":
					w.Write([]byte(sum))
				case "/sig":
					w.Write([]byte(tc.sig(s)))
				case "/deb":
					t.Error("the package was downloaded before its checksum was proven signed")
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := New("o/v", "0.2.0", "")
			c.API, c.Keys = srv.URL, s.Keys
			c.Installed = func() (bool, string) { return true, "" }
			c.Method = func() string { return MethodDeb }
			c.RunInstaller = func(context.Context, string) (string, error) { t.Fatal("installer ran"); return "", nil }
			if _, err := c.Install(t.Context(), t.TempDir(), func(string) {}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// An older release, its package and its validly signed checksum offered as a newer version:
// the signature is good and the package matches its checksum, but the signed line names the
// old package. Nothing is installed, and the package is not even downloaded.
func TestInstallRefusesAnOlderSignedPackageOfferedAsNew(t *testing.T) {
	for _, tc := range []struct{ name, signed string }{
		{"an older release", "virtomir_1.0.0_" + debArch() + ".deb"},
		{"another architecture", "virtomir_9.0.0_s390x.deb"},
		{"another file", "virtomir_v9.0.0_linux_amd64.tar.gz"},
		{"no file name", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSigner(t)
			pkg := "the old package"
			sum := sha256.Sum256([]byte(pkg))
			var srv *httptest.Server
			srv = httptest.NewServer(s.wrap(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/v/releases/latest":
					w.Write([]byte(strings.NewReplacer("ARCH", debArch(), "URL", srv.URL).Replace(
						`{"tag_name":"v9.0.0","assets":[{"name":"virtomir_9.0.0_ARCH.deb","browser_download_url":"URL/deb"},
						{"name":"virtomir_9.0.0_ARCH.deb.sha256","browser_download_url":"URL/sha"},
						{"name":"virtomir_9.0.0_ARCH.deb.sha256.asc","browser_download_url":"URL/sig"}]}`)))
				case "/sha":
					fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), tc.signed)
				case "/deb":
					t.Error("the package was downloaded though its signed checksum is of another file")
					fmt.Fprint(w, pkg)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c := New("o/v", "0.2.0", "")
			c.API, c.Keys = srv.URL, s.Keys
			c.Installed = func() (bool, string) { return true, "" }
			c.Method = func() string { return MethodDeb }
			c.RunInstaller = func(context.Context, string) (string, error) { t.Fatal("installer ran"); return "", nil }
			if _, err := c.Install(t.Context(), t.TempDir(), func(string) {}); err == nil || !strings.Contains(err.Error(), "signed checksum is for") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A build of main: the checksum file keeps "+", GitHub may list the package with ".".
func TestSameAsset(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"virtomir_1.1.1_amd64.deb", "virtomir_1.1.1_amd64.deb", true},
		{"virtomir_1.1.1+33.g2b9292e_amd64.deb", "virtomir_1.1.1.33.g2b9292e_amd64.deb", true},
		{"virtomir_1.1.0_amd64.deb", "virtomir_1.1.1_amd64.deb", false},
		{"", "virtomir_1.1.1_amd64.deb", false},
		{"", "", false},
	} {
		if got := sameAsset(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAsset(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
	if sum, name := sumFields("ABC  *dir/virtomir_1.1.1_amd64.deb\n"); sum != "abc" || name != "virtomir_1.1.1_amd64.deb" {
		t.Errorf("sumFields = %q %q", sum, name)
	}
}
