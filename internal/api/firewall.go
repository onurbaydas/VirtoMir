package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// Firewaller manages a machine's firewall (implemented by *virt.Client, ADR-0033).
type Firewaller interface {
	Firewall(uuid string) (virt.Firewall, error)
	SetFirewall(uuid string, fw virt.Firewall) (virt.FirewallResult, error)
	RemoveFirewall(uuid string) error
}

func (s *Server) firewallRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/firewall", s.getFirewall)
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/firewall", s.putFirewall)
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/download", s.downloadToPool)
}

func (s *Server) firewaller(w http.ResponseWriter) (Firewaller, bool) {
	f, ok := s.backend.(Firewaller)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "firewalls are not available"})
	}
	return f, ok
}

func (s *Server) getFirewall(w http.ResponseWriter, r *http.Request) {
	f, ok := s.firewaller(w)
	if !ok {
		return
	}
	fw, err := f.Firewall(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, fw)
}

func (s *Server) putFirewall(w http.ResponseWriter, r *http.Request) {
	f, ok := s.firewaller(w)
	if !ok {
		return
	}
	var fw virt.Firewall
	if !s.decode(w, r, &fw) {
		return
	}
	res, err := f.SetFirewall(r.PathValue("uuid"), fw)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

// downloadToPool fetches an image (ISO, cloud image) from a URL straight into a storage pool
// as a background job: {"url": "https://…", "name": "debian.iso", "sha256": "<hex>"}.
// With a checksum, a mismatching download is deleted again.
func (s *Server) downloadToPool(w http.ResponseWriter, r *http.Request) {
	var b struct {
		URL    string `json:"url"`
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	u, err := url.Parse(strings.TrimSpace(b.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "give an http:// or https:// address"})
		return
	}
	if b.Name == "" {
		b.Name = path.Base(u.Path)
	}
	want := strings.ToLower(strings.TrimSpace(b.SHA256))
	if want != "" {
		if raw, err := hex.DecodeString(want); err != nil || len(raw) != sha256.Size {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the SHA-256 checksum must be 64 hex characters"})
			return
		}
	}
	pool := r.PathValue("pool")
	job := s.jobs.Start("download", "Download "+b.Name, func(ctx context.Context, report func(string)) (string, error) {
		return s.fetchInto(ctx, u.String(), pool, b.Name, want, report)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// downloadClient has no overall timeout (images are big) but gives up on a silent server.
var downloadClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 30 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
}}

func (s *Server) fetchInto(ctx context.Context, src, pool, name, want string, report func(string)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "VirtoMir")
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the server answered %s", resp.Status)
	}
	if resp.ContentLength <= 0 {
		return "", fmt.Errorf("the server does not say how big the file is; download it and upload it instead")
	}
	total := resp.ContentLength
	var done atomic.Int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				n := done.Load()
				report(fmt.Sprintf("%.0f of %.0f MiB (%d%%)", float64(n)/(1<<20), float64(total)/(1<<20), n*100/total))
			}
		}
	}()
	h := sha256.New()
	body := io.TeeReader(&countingReader{r: resp.Body, n: &done}, h)
	vol, err := s.backend.UploadVolume(ctx, pool, name, total, body)
	if err != nil {
		return "", err
	}
	s.live.Refresh()
	if got := hex.EncodeToString(h.Sum(nil)); want != "" && got != want {
		_ = s.backend.DeleteVolume(pool, vol.Name)
		return "", fmt.Errorf("checksum mismatch (got %s); the download was deleted", got)
	}
	if want != "" {
		return vol.Name + " (SHA-256 verified)", nil
	}
	return vol.Name, nil
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}
