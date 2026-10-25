package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errNotFound is a 404 of the VirtoMir API.
var errNotFound = errors.New("not found")

// API is the part of the VirtoMir API the driver uses: volumes in storage pools, and disks
// of the machines the Kubernetes nodes run in.
type API struct {
	base  string
	token string
	http  *http.Client
}

// NewAPI is a client for base (https://hv1:8420) with an API token of an admin.
func NewAPI(base, token string, insecure bool) (*API, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("the VirtoMir URL must look like https://host:8420, not %q", base)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for: self-signed host certificates
	}
	return &API{base: u.String(), token: token, http: &http.Client{Transport: tr, Timeout: time.Minute}}, nil
}

// at is path p on member host ("" = the host the URL names).
func at(host, p string) string {
	if host == "" {
		return p
	}
	return "/api/v1/hosts/" + url.PathEscape(host) + strings.TrimPrefix(p, "/api/v1")
}

func (a *API) do(ctx context.Context, method, p string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+p, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	res, err := a.http.Do(req)
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

// Volume is a volume of a storage pool.
type Volume struct {
	Name          string   `json:"name"`
	CapacityBytes int64    `json:"capacityBytes"`
	UsedBy        []string `json:"usedBy"`
}

func (a *API) volumes(ctx context.Context, host, pool string) ([]Volume, error) {
	var v struct {
		Volumes []Volume `json:"volumes"`
	}
	err := a.do(ctx, http.MethodGet, at(host, "/api/v1/storage/pools/"+url.PathEscape(pool)+"/volumes"), nil, &v)
	return v.Volumes, err
}

func (a *API) createVolume(ctx context.Context, host, pool, name string, gib int64, format string) error {
	return a.do(ctx, http.MethodPost, at(host, "/api/v1/storage/pools/"+url.PathEscape(pool)+"/volumes"),
		map[string]any{"name": name, "sizeGiB": gib, "format": format}, nil)
}

func (a *API) deleteVolume(ctx context.Context, host, pool, name string) error {
	return a.do(ctx, http.MethodDelete, at(host, "/api/v1/storage/pools/"+url.PathEscape(pool)+"/volumes/"+url.PathEscape(name)), nil, nil)
}

// findMachine is the member that runs machine uuid ("" without a cluster).
func (a *API) findMachine(ctx context.Context, uuid string) (string, error) {
	var cl struct {
		Enabled bool `json:"enabled"`
		Members []struct {
			ID   string `json:"id"`
			Self bool   `json:"self"`
		} `json:"members"`
	}
	if err := a.do(ctx, http.MethodGet, "/api/v1/cluster", nil, &cl); err != nil {
		return "", err
	}
	hosts := []string{""}
	if cl.Enabled && len(cl.Members) > 1 {
		hosts = hosts[:0]
		for _, m := range cl.Members {
			if m.Self {
				hosts = append([]string{""}, hosts...)
			} else {
				hosts = append(hosts, m.ID)
			}
		}
	}
	for _, h := range hosts {
		var d struct {
			UUID string `json:"uuid"`
		}
		err := a.do(ctx, http.MethodGet, at(h, "/api/v1/domains/"+url.PathEscape(uuid)), nil, &d)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, errNotFound) {
			return "", err
		}
	}
	return "", fmt.Errorf("machine %s: %w on any member", uuid, errNotFound)
}

type device struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Serial string `json:"serial"`
}

func (a *API) devices(ctx context.Context, host, uuid string) ([]device, error) {
	var d struct {
		Devices []device `json:"devices"`
	}
	err := a.do(ctx, http.MethodGet, at(host, "/api/v1/domains/"+url.PathEscape(uuid)), nil, &d)
	return d.Devices, err
}

func (a *API) attach(ctx context.Context, host, uuid, pool, volume, serial string) error {
	return a.do(ctx, http.MethodPost, at(host, "/api/v1/domains/"+url.PathEscape(uuid)+"/disks"),
		map[string]any{"pool": pool, "volume": volume, "serial": serial}, nil)
}

func (a *API) detach(ctx context.Context, host, uuid, target string) error {
	return a.do(ctx, http.MethodPost, at(host, "/api/v1/domains/"+url.PathEscape(uuid)+"/devices/detach"),
		map[string]any{"kind": "disk", "target": target}, nil)
}

// volumeID is host/pool/name ("-" for the URL's host); it names the volume everywhere.
type volumeID struct{ host, pool, name string }

func (v volumeID) String() string {
	h := v.host
	if h == "" {
		h = "-"
	}
	return h + "/" + v.pool + "/" + v.name
}

func parseVolumeID(s string) (volumeID, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return volumeID{}, fmt.Errorf("volume ID %q is not host/pool/name", s)
	}
	if parts[0] == "-" {
		parts[0] = ""
	}
	return volumeID{parts[0], parts[1], parts[2]}, nil
}

// serial is the disk serial of a volume: short enough for virtio (20), the same for the
// controller that attaches it and the node that looks for it.
func serial(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "vmcsi-" + hex.EncodeToString(sum[:7])
}
