// Package client is a small client for the VirtoMir API (/api/v1) used by the Terraform
// provider: machines, networks, storage pools and background jobs.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is a 404 from the API.
var ErrNotFound = errors.New("not found")

// Client talks to one VirtoMir host (or, with Host set, a cluster member through it).
type Client struct {
	base  string
	token string
	http  *http.Client
	// Host routes every call to a cluster member: /api/v1/hosts/<Host>/…
	Host string
	// Poll is how often jobs and power states are checked (tests shorten it).
	Poll time.Duration
}

// New returns a client for url (e.g. https://hv1:8420) with an API token.
func New(baseURL, token string, insecure bool) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url must look like https://host:8420, not %q", baseURL)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for: self-signed host certificates
	}
	return &Client{base: u.String(), token: token, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute}, Poll: 2 * time.Second}, nil
}

// APIError is an error answer of the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("VirtoMir API %d: %s", e.Status, e.Message) }

func (c *Client) path(p string) string {
	if c.Host != "" && strings.HasPrefix(p, "/api/v1/") {
		return "/api/v1/hosts/" + url.PathEscape(c.Host) + strings.TrimPrefix(p, "/api/v1")
	}
	return p
}

// Do sends a request; body (if not nil) is JSON, out (if not nil) receives the JSON answer.
func (c *Client) Do(ctx context.Context, method, p string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+c.path(p), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, p, ErrNotFound)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return &APIError{Status: res.StatusCode, Message: e.Error}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ---- machines ---------------------------------------------------------------------------

// Domain is a machine as the list shows it.
type Domain struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	State        string `json:"state"`
	VCPUs        int    `json:"vcpus"`
	MaxMemoryKiB uint64 `json:"maxMemoryKiB"`
	Autostart    bool   `json:"autostart"`
	Template     bool   `json:"template"`
}

// Running reports whether the state counts as on (running, paused, …).
func (d Domain) Running() bool {
	switch d.State {
	case "", "shutoff", "crashed", "pmsuspended":
		return false
	}
	return true
}

func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	var out struct {
		Domains []Domain `json:"domains"`
	}
	err := c.Do(ctx, http.MethodGet, "/api/v1/domains", nil, &out)
	return out.Domains, err
}

// Domain finds a machine by UUID (ErrNotFound when it is gone).
func (c *Client) Domain(ctx context.Context, uuid string) (Domain, error) {
	list, err := c.Domains(ctx)
	if err != nil {
		return Domain{}, err
	}
	for _, d := range list {
		if strings.EqualFold(d.UUID, uuid) {
			return d, nil
		}
	}
	return Domain{}, fmt.Errorf("machine %s: %w", uuid, ErrNotFound)
}

// DomainByName finds a machine by name or UUID.
func (c *Client) DomainByName(ctx context.Context, name string) (Domain, error) {
	list, err := c.Domains(ctx)
	if err != nil {
		return Domain{}, err
	}
	for _, d := range list {
		if d.Name == name || strings.EqualFold(d.UUID, name) {
			return d, nil
		}
	}
	return Domain{}, fmt.Errorf("machine %s: %w", name, ErrNotFound)
}

type CloudInit struct {
	Hostname string   `json:"hostname,omitempty"`
	User     string   `json:"user"`
	Password string   `json:"password,omitempty"`
	SSHKeys  []string `json:"sshKeys,omitempty"`
	Packages []string `json:"packages,omitempty"`
}

type NIC struct {
	Network string `json:"network,omitempty"`
	Bridge  string `json:"bridge,omitempty"`
}

// CreateSpec is POST /api/v1/domains.
type CreateSpec struct {
	Name       string     `json:"name"`
	OS         string     `json:"os,omitempty"`
	VCPUs      int        `json:"vcpus"`
	MemoryMiB  uint64     `json:"memoryMiB"`
	Firmware   string     `json:"firmware,omitempty"`
	SecureBoot bool       `json:"secureBoot,omitempty"`
	TPM        bool       `json:"tpm,omitempty"`
	Pool       string     `json:"pool,omitempty"`
	DiskGiB    uint64     `json:"diskGiB,omitempty"`
	DiskPath   string     `json:"diskPath,omitempty"`
	BaseImage  string     `json:"baseImage,omitempty"`
	CloudInit  *CloudInit `json:"cloudInit,omitempty"`
	ISO        string     `json:"iso,omitempty"`
	Network    *NIC       `json:"network,omitempty"`
	Start      bool       `json:"start"`
}

// CreateDomain creates a machine and returns its UUID.
func (c *Client) CreateDomain(ctx context.Context, s CreateSpec) (string, error) {
	var res struct {
		UUID       string `json:"uuid"`
		StartError string `json:"startError"`
	}
	if err := c.Do(ctx, http.MethodPost, "/api/v1/domains?wait=true", s, &res); err != nil {
		return "", err
	}
	if res.StartError != "" {
		return res.UUID, fmt.Errorf("created %s, but it did not start: %s", s.Name, res.StartError)
	}
	return res.UUID, nil
}

// DeploySpec is POST /api/v1/domains/{template}/deploy.
type DeploySpec struct {
	Name      string     `json:"name"`
	Linked    bool       `json:"linked"`
	CloudInit *CloudInit `json:"cloudInit,omitempty"`
	Start     bool       `json:"start"`
}

// Deploy makes a machine from a template and waits for the job; it returns the new UUID.
func (c *Client) Deploy(ctx context.Context, template string, s DeploySpec) (string, error) {
	var job Job
	if err := c.Do(ctx, http.MethodPost, "/api/v1/domains/"+url.PathEscape(template)+"/deploy", s, &job); err != nil {
		return "", err
	}
	done, err := c.Wait(ctx, job.ID)
	if err != nil {
		return "", err
	}
	return done.Result, nil
}

// Action runs start, shutdown, force-off, reboot, … on a machine.
func (c *Client) Action(ctx context.Context, uuid, action string) error {
	return c.Do(ctx, http.MethodPost, "/api/v1/domains/"+url.PathEscape(uuid)+"/actions/"+action, nil, nil)
}

// WaitState waits until the machine is (not) running.
func (c *Client) WaitState(ctx context.Context, uuid string, running bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		d, err := c.Domain(ctx, uuid)
		if err != nil {
			return err
		}
		if d.Running() == running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still %s after %s", d.Name, d.State, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.Poll):
		}
	}
}

// Stop shuts a machine down gracefully, and pulls the plug if it does not stop in time.
func (c *Client) Stop(ctx context.Context, uuid string, grace time.Duration) error {
	d, err := c.Domain(ctx, uuid)
	if err != nil || !d.Running() {
		return err
	}
	if err := c.Action(ctx, uuid, "shutdown"); err == nil && c.WaitState(ctx, uuid, false, grace) == nil {
		return nil
	}
	if err := c.Action(ctx, uuid, "force-off"); err != nil {
		return err
	}
	return c.WaitState(ctx, uuid, false, time.Minute)
}

func (c *Client) SetAutostart(ctx context.Context, uuid string, on bool) error {
	return c.Do(ctx, http.MethodPut, "/api/v1/domains/"+url.PathEscape(uuid)+"/autostart", map[string]bool{"enabled": on}, nil)
}

// Resize changes vCPUs and memory through the settings form (live where possible).
func (c *Client) Resize(ctx context.Context, uuid string, vcpus int, memoryMiB uint64) error {
	var s map[string]any
	if err := c.Do(ctx, http.MethodGet, "/api/v1/domains/"+url.PathEscape(uuid)+"/settings", nil, &s); err != nil {
		return err
	}
	delete(s, "bootDevices")
	delete(s, "hotplugMemoryMiB")
	s["vcpus"], s["maxVcpus"] = vcpus, vcpus
	s["memoryMiB"], s["currentMemoryMiB"] = memoryMiB, memoryMiB
	// The topology must multiply up to the vCPUs: keep it simple, one socket.
	s["sockets"], s["cores"], s["threads"] = 1, vcpus, 1
	// Pinning has one entry per vCPU: cut it, or extend it with unpinned ones ("").
	if pins, ok := s["pinning"].([]any); ok && len(pins) != vcpus {
		next := make([]any, vcpus)
		for i := range next {
			next[i] = ""
			if i < len(pins) {
				next[i] = pins[i]
			}
		}
		s["pinning"] = next
	}
	return c.Do(ctx, http.MethodPut, "/api/v1/domains/"+url.PathEscape(uuid)+"/settings", s, nil)
}

// DeleteDomain removes a machine; with disks, its own disk images too.
func (c *Client) DeleteDomain(ctx context.Context, uuid string, disks bool) error {
	p := "/api/v1/domains/" + url.PathEscape(uuid)
	if disks {
		p += "?storage=remove"
	}
	return c.Do(ctx, http.MethodDelete, p, nil, nil)
}

// ---- jobs ------------------------------------------------------------------------------

type Job struct {
	ID      string `json:"id"`
	State   string `json:"state"` // running, succeeded, failed
	Result  string `json:"result"`
	Message string `json:"message"`
}

// Wait polls a job until it ends.
func (c *Client) Wait(ctx context.Context, id string) (Job, error) {
	for {
		var j Job
		if err := c.Do(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(id), nil, &j); err != nil {
			return j, err
		}
		switch j.State {
		case "succeeded":
			return j, nil
		case "failed":
			return j, fmt.Errorf("job %s failed: %s", id, j.Message)
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(c.Poll):
		}
	}
}

// ---- networks and pools -----------------------------------------------------------------

type Network struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Bridge  string `json:"bridge,omitempty"`
	Subnet  string `json:"subnet,omitempty"`
	Subnet6 string `json:"subnet6,omitempty"`
	Active  bool   `json:"active"`
}

type NetworkSpec struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Subnet  string `json:"subnet,omitempty"`
	DHCP    bool   `json:"dhcp"`
	Bridge  string `json:"bridge,omitempty"`
	Subnet6 string `json:"subnet6,omitempty"`
	DHCP6   bool   `json:"dhcp6,omitempty"`
}

func (c *Client) Network(ctx context.Context, name string) (Network, error) {
	var out struct {
		Networks []Network `json:"networks"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/v1/networks", nil, &out); err != nil {
		return Network{}, err
	}
	for _, n := range out.Networks {
		if n.Name == name {
			return n, nil
		}
	}
	return Network{}, fmt.Errorf("network %s: %w", name, ErrNotFound)
}

func (c *Client) CreateNetwork(ctx context.Context, s NetworkSpec) error {
	return c.Do(ctx, http.MethodPost, "/api/v1/networks", s, nil)
}

func (c *Client) DeleteNetwork(ctx context.Context, name string) error {
	return c.Do(ctx, http.MethodDelete, "/api/v1/networks/"+url.PathEscape(name), nil, nil)
}

type Pool struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Path   string `json:"path,omitempty"`
	Active bool   `json:"active"`
}

type PoolSpec struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Type    string   `json:"type,omitempty"`
	Host    string   `json:"host,omitempty"`
	Export  string   `json:"export,omitempty"`
	Target  string   `json:"target,omitempty"`
	Source  string   `json:"source,omitempty"`
	Devices []string `json:"devices,omitempty"`
}

func (c *Client) Pool(ctx context.Context, name string) (Pool, error) {
	var out struct {
		Pools []Pool `json:"pools"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/v1/storage/pools", nil, &out); err != nil {
		return Pool{}, err
	}
	for _, p := range out.Pools {
		if p.Name == name {
			return p, nil
		}
	}
	return Pool{}, fmt.Errorf("pool %s: %w", name, ErrNotFound)
}

func (c *Client) CreatePool(ctx context.Context, s PoolSpec) error {
	return c.Do(ctx, http.MethodPost, "/api/v1/storage/pools", s, nil)
}

// DeletePool stops and forgets a pool; its files stay.
func (c *Client) DeletePool(ctx context.Context, name string) error {
	if p, err := c.Pool(ctx, name); err == nil && p.Active {
		if err := c.Do(ctx, http.MethodPost, "/api/v1/storage/pools/"+url.PathEscape(name)+"/actions/stop", nil, nil); err != nil {
			return err
		}
	}
	return c.Do(ctx, http.MethodDelete, "/api/v1/storage/pools/"+url.PathEscape(name), nil, nil)
}

// HostInfo is GET /api/v1/host.
type HostInfo struct {
	Hostname          string `json:"hostname"`
	HypervisorVersion string `json:"hypervisorVersion"`
	LibvirtVersion    string `json:"libvirtVersion"`
	CPUModel          string `json:"cpuModel"`
	CPUs              int    `json:"cpus"`
	MemoryKiB         uint64 `json:"memoryKiB"`
}

func (c *Client) Info(ctx context.Context) (HostInfo, error) {
	var h HostInfo
	err := c.Do(ctx, http.MethodGet, "/api/v1/host", nil, &h)
	return h, err
}

// ---- projects ---------------------------------------------------------------------------

type ProjectMember struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type ProjectLimits struct {
	Machines  int   `json:"machines"`
	VCPUs     int   `json:"vcpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskGiB   int64 `json:"diskGiB"`
}

type ProjectUsage struct {
	Machines  int   `json:"machines"`
	VCPUs     int   `json:"vcpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskGiB   int64 `json:"diskGiB"`
}

type Project struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Members      []ProjectMember `json:"members"`
	Limits       *ProjectLimits  `json:"limits,omitempty"`
	StoragePools []string        `json:"storagePools"`
	Networks     []string        `json:"networks"`
	Created      time.Time       `json:"created,omitempty"`
	Usage        *ProjectUsage   `json:"usage,omitempty"`
}

func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	err := c.Do(ctx, http.MethodGet, "/api/v1/projects", nil, &out)
	return out.Projects, err
}

func (c *Client) Project(ctx context.Context, id string) (Project, error) {
	var p Project
	err := c.Do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(id), nil, &p)
	return p, err
}

func (c *Client) CreateProject(ctx context.Context, p Project) error {
	if p.Members == nil {
		p.Members = []ProjectMember{}
	}
	if p.StoragePools == nil {
		p.StoragePools = []string{}
	}
	if p.Networks == nil {
		p.Networks = []string{}
	}
	return c.Do(ctx, http.MethodPost, "/api/v1/projects", p, nil)
}

func (c *Client) UpdateProject(ctx context.Context, id string, p Project) error {
	if p.Members == nil {
		p.Members = []ProjectMember{}
	}
	if p.StoragePools == nil {
		p.StoragePools = []string{}
	}
	if p.Networks == nil {
		p.Networks = []string{}
	}
	return c.Do(ctx, http.MethodPut, "/api/v1/projects/"+url.PathEscape(id), p, nil)
}

func (c *Client) DeleteProject(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/api/v1/projects/"+url.PathEscape(id), nil, nil)
}

// ---- placement rules --------------------------------------------------------------------

type PlacementKind string

const (
	PlacementVMAffinity     PlacementKind = "vm_affinity"
	PlacementVMAntiAffinity PlacementKind = "vm_anti_affinity"
	PlacementVMHost         PlacementKind = "vm_host"
)

type PlacementStrength string

const (
	PlacementStrengthMust   PlacementStrength = "must"
	PlacementStrengthShould PlacementStrength = "should"
)

type PlacementRule struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Kind     PlacementKind     `json:"kind"`
	Enabled  bool              `json:"enabled"`
	Strength PlacementStrength `json:"strength"`
	Domains  []string          `json:"domains"`
	Hosts    *[]string         `json:"hosts,omitempty"`
	Exclude  bool              `json:"exclude,omitempty"`
}

func (c *Client) PlacementRules(ctx context.Context) ([]PlacementRule, error) {
	var out struct {
		Rules []PlacementRule `json:"rules"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/v1/placement", nil, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

func (c *Client) PlacementRule(ctx context.Context, id string) (PlacementRule, error) {
	rules, err := c.PlacementRules(ctx)
	if err != nil {
		return PlacementRule{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r, nil
		}
	}
	return PlacementRule{}, fmt.Errorf("placement rule %s: %w", id, ErrNotFound)
}

func (c *Client) CreatePlacementRule(ctx context.Context, r PlacementRule) (PlacementRule, error) {
	if r.Domains == nil {
		r.Domains = []string{}
	}
	var out PlacementRule
	err := c.Do(ctx, http.MethodPost, "/api/v1/placement/rules", r, &out)
	return out, err
}

func (c *Client) UpdatePlacementRule(ctx context.Context, id string, r PlacementRule) error {
	if r.Domains == nil {
		r.Domains = []string{}
	}
	return c.Do(ctx, http.MethodPut, "/api/v1/placement/rules/"+url.PathEscape(id), r, nil)
}

func (c *Client) DeletePlacementRule(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/api/v1/placement/rules/"+url.PathEscape(id), nil, nil)
}

