package k8s

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// PKI is what kubeadm would otherwise make on the first control plane: the three CAs and
// the service-account key pair. Every control plane gets the same files, so they join
// without kubeadm's uploaded-certificates step (which expires after two hours).
type PKI struct {
	CACert, CAKey                   string `json:",omitempty"`
	FrontProxyCACert, FrontProxyKey string `json:",omitempty"`
	EtcdCACert, EtcdCAKey           string `json:",omitempty"`
	SAKey, SAPub                    string `json:",omitempty"`
	// Token is a kubeadm bootstrap token (id.secret) for the nodes made with the cluster.
	Token string `json:",omitempty"`
}

// NewPKI makes the CAs (ten years, like kubeadm) and a bootstrap token.
func NewPKI(now time.Time) (PKI, error) {
	var p PKI
	var err error
	if p.CACert, p.CAKey, err = newCA("kubernetes", now); err != nil {
		return p, err
	}
	if p.FrontProxyCACert, p.FrontProxyKey, err = newCA("front-proxy-ca", now); err != nil {
		return p, err
	}
	if p.EtcdCACert, p.EtcdCAKey, err = newCA("etcd-ca", now); err != nil {
		return p, err
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return p, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return p, err
	}
	p.SAKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	p.SAPub = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	p.Token, err = NewToken()
	return p, err
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
}

func newCA(cn string, now time.Time) (certPEM, keyPEM string, err error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	sn, err := serial()
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn, Subject: pkix.Name{CommonName: cn},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})), nil
}

func parseCA(certPEM, keyPEM string) (*x509.Certificate, *rsa.PrivateKey, error) {
	cb, _ := pem.Decode([]byte(certPEM))
	kb, _ := pem.Decode([]byte(keyPEM))
	if cb == nil || kb == nil {
		return nil, nil, errors.New("the cluster's CA is missing")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	return cert, key, err
}

// CAHash is kubeadm's --discovery-token-ca-cert-hash of the cluster CA.
func (p PKI) CAHash() (string, error) {
	b, _ := pem.Decode([]byte(p.CACert))
	if b == nil {
		return "", errors.New("the cluster's CA is missing")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// NewToken is a kubeadm bootstrap token: [a-z0-9]{6}.[a-z0-9]{16}.
func NewToken() (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 22)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return string(b[:6]) + "." + string(b[6:]), nil
}

// Kubeconfig is an admin kubeconfig for endpoint: a client certificate signed by the
// cluster CA for user (group kubeadm:cluster-admins, which kubeadm binds to cluster-admin),
// valid for a year. Each download is a new certificate.
func (p PKI) Kubeconfig(cluster, endpoint, user string, now time.Time) ([]byte, error) {
	ca, caKey, err := parseCA(p.CACert, p.CAKey)
	if err != nil {
		return nil, err
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn, Subject: pkix.Name{CommonName: user, Organization: []string{"kubeadm:cluster-admins"}},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &k.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	b64 := func(s []byte) string { return base64.StdEncoding.EncodeToString(s) }
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	ctx := user + "@" + cluster
	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster:
    server: %[2]s
    certificate-authority-data: %[3]s
users:
- name: %[4]s
  user:
    client-certificate-data: %[5]s
    client-key-data: %[6]s
contexts:
- name: %[7]s
  context:
    cluster: %[1]s
    user: %[4]s
current-context: %[7]s
`, cluster, endpoint, b64([]byte(p.CACert)), user, b64(cert), b64(key), ctx), nil
}
