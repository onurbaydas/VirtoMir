package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Headers a peer sets on forwarded requests. They are only believed together with a valid
// peer Authorization; on any other request they are ignored.
const (
	HeaderUser  = "X-VirtoMir-User"
	HeaderRole  = "X-VirtoMir-Role"
	HeaderToken = "X-VirtoMir-Token"
	HeaderVia   = "X-VirtoMir-Via"
	// AuthScheme prefixes the peer credential "<node id>.<key>".
	AuthScheme = "VirtoMirPeer "
)

// Code is what a join code carries: where the inviting node is, how to recognize it, and
// the one-time secret.
type Code struct {
	Address     string `json:"a"`
	Fingerprint string `json:"f"`
	Secret      string `json:"s"`
}

const codePrefix = "vmj1-"

func (c Code) String() string {
	b, _ := json.Marshal(c)
	return codePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseCode reads a join code as shown by "Add host" (surrounding whitespace is fine).
func ParseCode(s string) (Code, error) {
	var c Code
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), codePrefix)
	if !ok {
		return c, errors.New("this is not a VirtoMir join code")
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Address == "" || c.Fingerprint == "" || c.Secret == "" {
		return c, errors.New("the join code is incomplete; copy it again")
	}
	return c, nil
}

// Fingerprint is the SHA-256 of a certificate (DER), as hex.
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

// PinnedClient returns an HTTPS client that trusts exactly one certificate (by fingerprint),
// whatever its name or issuer. That is how members recognize each other's self-signed
// certificates without a CA.
func PinnedClient(fingerprint string, timeout time.Duration) *http.Client {
	want, _ := hex.DecodeString(fingerprint)
	tr := &http.Transport{
		Proxy:               nil, // peers talk directly
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   false, // HTTP/1.1: console WebSockets are proxied through it
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // replaced by the pin below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("peer sent no certificate")
				}
				got := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if len(want) != len(got) || subtle.ConstantTimeCompare(got[:], want) != 1 {
					return fmt.Errorf("certificate fingerprint %s does not match the expected %s", hex.EncodeToString(got[:]), fingerprint)
				}
				return nil
			},
		},
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// Peer calls another member's API.
type Peer struct {
	Node   Node
	selfID string
	key    string
	client *http.Client
}

// Peer returns a client for member n authenticated as this node. rt must be pinned to n's
// certificate (PinnedClient(n.Fingerprint, 0).Transport); callers keep one per member.
func (s *Store) Peer(n Node, rt http.RoundTripper) *Peer {
	return &Peer{Node: n, selfID: s.SelfID(), key: s.Key(), client: &http.Client{Transport: rt}}
}

// Credential is the Authorization header value this node sends to peers.
func (s *Store) Credential() string { return AuthScheme + s.SelfID() + "." + s.Key() }

// Hello asks the peer who it is. It carries no credential: it is also how a host checks a
// newcomer that does not know it yet.
func (p *Peer) Hello(ctx context.Context) (Hello, error) {
	var h Hello
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Node.Address+"/api/v1/cluster/peer/hello", nil)
	if err != nil {
		return h, err
	}
	return h, do(p.client, req, &h)
}

// Call sends a JSON request to the peer and decodes a JSON answer into out (may be nil).
func (p *Peer) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.Node.Address+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", AuthScheme+p.selfID+"."+p.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return do(p.client, req, out)
}

// do runs req and decodes the answer; API errors come back as their message.
func do(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return reachError(req.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// reachError explains a failed connection in terms of what to check.
func reachError(u *url.URL, err error) error {
	var ne net.Error
	switch {
	case strings.Contains(err.Error(), "fingerprint"):
		return fmt.Errorf("%s presented a different certificate than expected (was it reinstalled?): %w", u.Host, err)
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("%s did not answer in time; check the address and that port %s is open", u.Host, u.Port())
	}
	return fmt.Errorf("cannot reach %s: %w", u.Host, err)
}

// JoinRequest is what a joining node sends to the inviting node.
type JoinRequest struct {
	Secret string `json:"secret"`
	Node   Node   `json:"node"`
}

// MemberList is a versioned member list (join answers, pushes, GET /cluster).
type MemberList struct {
	Version int    `json:"version"`
	Members []Node `json:"members"`
}

// Hello is a node's answer to /api/v1/cluster/peer/hello: who it is and which version runs.
type Hello struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Join asks the node behind code to admit self. advertisePort is this node's listen port;
// the IP is the one this host used to reach the other node, so it is routable from there.
func (s *Store) Join(ctx context.Context, code Code, advertisePort string) error {
	if s.Clustered() {
		return errors.New("this host is already in a cluster; remove it from there first")
	}
	if code.Fingerprint == s.fingerprint {
		return errors.New("this join code was made on this host; use it on the host you want to add")
	}
	client := PinnedClient(code.Fingerprint, 30*time.Second)
	u, err := url.Parse(code.Address)
	if err != nil || u.Scheme != "https" {
		return errors.New("the join code has no valid https address")
	}
	// Learn our own address as the other host sees it: the local end of a route to it
	// (a UDP "connection" sends nothing, it only picks the source address).
	conn, err := net.DialTimeout("udp", u.Host, 5*time.Second)
	if err != nil {
		return reachError(u, err)
	}
	localIP := conn.LocalAddr().(*net.UDPAddr).IP
	conn.Close()
	self := s.Self("https://" + net.JoinHostPort(localIP.String(), advertisePort))

	b, _ := json.Marshal(JoinRequest{Secret: code.Secret, Node: self})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, code.Address+"/api/v1/cluster/peer/join", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var list MemberList
	if err := do(client, req, &list); err != nil {
		return err
	}
	return s.Rejoin(list.Members, list.Version)
}
