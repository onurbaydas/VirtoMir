package virtomir

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

func envOr(name string) string { return os.Getenv(name) }

var errNotFound = errors.New("not found")

// client is the part of the VirtoMir API the builder uses.
type client struct {
	base, token, host string
	http              *http.Client
}

func newClient(c *Config) (*client, error) {
	u, err := url.Parse(strings.TrimRight(c.URL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url must look like https://host:8420, not %q", c.URL)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for: self-signed host certificates
	}
	return &client{base: u.String(), token: c.Token, host: c.Host, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}, nil
}

func (c *client) do(ctx context.Context, method, p string, body, out any) error {
	if c.host != "" {
		p = "/api/v1/hosts/" + url.PathEscape(c.host) + strings.TrimPrefix(p, "/api/v1")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+p, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		if res.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%s %s: %w: %s", method, p, errNotFound, e.Error)
		}
		return fmt.Errorf("VirtoMir %s %s: %d %s", method, p, res.StatusCode, e.Error)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type machine struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Devices []struct {
		Kind      string   `json:"kind"`
		Addresses []string `json:"addresses"`
	} `json:"devices"`
}

func (c *client) machine(ctx context.Context, uuid string) (machine, error) {
	var m machine
	err := c.do(ctx, http.MethodGet, "/api/v1/domains/"+url.PathEscape(uuid), nil, &m)
	return m, err
}

// ipv4 is the machine's first IPv4 address, if it has one yet.
func (m machine) ipv4() string {
	for _, d := range m.Devices {
		if d.Kind != "interface" {
			continue
		}
		for _, a := range d.Addresses {
			a, _, _ = strings.Cut(a, "/")
			if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return ""
}

func (c *client) action(ctx context.Context, uuid, action string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/domains/"+url.PathEscape(uuid)+"/actions/"+action, nil, nil)
}

func (c *client) remove(ctx context.Context, uuid string) error {
	if m, err := c.machine(ctx, uuid); err == nil && m.State != "shutoff" {
		_ = c.action(ctx, uuid, "force-off")
	}
	err := c.do(ctx, http.MethodDelete, "/api/v1/domains/"+url.PathEscape(uuid)+"?storage=remove", nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}
