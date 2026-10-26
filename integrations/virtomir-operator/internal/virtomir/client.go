// Package virtomir is the part of the VirtoMir API the operator uses.
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
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is a 404 of the API.
var ErrNotFound = errors.New("not found")

// Client talks to one VirtoMir host (and to the other members through it).
type Client struct {
	base, token string
	http        *http.Client
}

// New is a client for base (https://hv1:8420) with an API token of an admin.
func New(base, token string, insecure bool) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("the VirtoMir URL must look like https://host:8420, not %q", base)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for: self-signed host certificates
	}
	return &Client{base: u.String(), token: token, http: &http.Client{Transport: tr, Timeout: time.Minute}}, nil
}

// on is path p on member host ("" = the host the URL names).
func on(host, p string) string {
	if host == "" {
		return p
	}
	return "/api/v1/hosts/" + url.PathEscape(host) + strings.TrimPrefix(p, "/api/v1")
}

// Do sends a request; body and out are JSON (nil for none).
func (c *Client) Do(ctx context.Context, method, p string, body, out any) error {
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
			return fmt.Errorf("%w: %s", ErrNotFound, e.Error)
		}
		return fmt.Errorf("VirtoMir %s %s: %d %s", method, p, res.StatusCode, e.Error)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Machine is a machine as VirtoMir lists it.
type Machine struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Template bool   `json:"template"`
}

// Machines lists a member's machines.
func (c *Client) Machines(ctx context.Context, host string) ([]Machine, error) {
	var out struct {
		Domains []Machine `json:"domains"`
	}
	err := c.Do(ctx, http.MethodGet, on(host, "/api/v1/domains"), nil, &out)
	return out.Domains, err
}

// Detail is a machine's state and addresses.
type Detail struct {
	UUID      string
	State     string
	Addresses []string
}

// Machine reads one machine.
func (c *Client) Machine(ctx context.Context, host, uuid string) (Detail, error) {
	var d struct {
		UUID    string `json:"uuid"`
		State   string `json:"state"`
		Devices []struct {
			Kind      string   `json:"kind"`
			Addresses []string `json:"addresses"`
		} `json:"devices"`
	}
	if err := c.Do(ctx, http.MethodGet, on(host, "/api/v1/domains/"+url.PathEscape(uuid)), nil, &d); err != nil {
		return Detail{}, err
	}
	out := Detail{UUID: d.UUID, State: d.State}
	for _, dev := range d.Devices {
		if dev.Kind == "interface" {
			out.Addresses = append(out.Addresses, dev.Addresses...)
		}
	}
	return out, nil
}

// Create makes a machine (POST /api/v1/domains) and returns its UUID.
func (c *Client) Create(ctx context.Context, host string, body map[string]any) (string, error) {
	var res struct {
		UUID       string `json:"uuid"`
		StartError string `json:"startError"`
	}
	if err := c.Do(ctx, http.MethodPost, on(host, "/api/v1/domains?wait=true"), body, &res); err != nil {
		return "", err
	}
	if res.StartError != "" {
		return res.UUID, fmt.Errorf("created but not started: %s", res.StartError)
	}
	return res.UUID, nil
}

// Deploy starts a machine from a template in the background and returns the job ID.
func (c *Client) Deploy(ctx context.Context, host, template string, body map[string]any) (string, error) {
	var job struct {
		ID string `json:"id"`
	}
	err := c.Do(ctx, http.MethodPost, on(host, "/api/v1/domains/"+url.PathEscape(template)+"/deploy"), body, &job)
	return job.ID, err
}

// Job is a background job: State running, succeeded or failed; Result the new UUID.
type Job struct {
	State, Message, Result string
}

// Job reads a background job.
func (c *Client) Job(ctx context.Context, host, id string) (Job, error) {
	var j Job
	err := c.Do(ctx, http.MethodGet, on(host, "/api/v1/jobs/"+url.PathEscape(id)), nil, &j)
	return j, err
}

// Action is start, shutdown, force-off, …
func (c *Client) Action(ctx context.Context, host, uuid, action string) error {
	return c.Do(ctx, http.MethodPost, on(host, "/api/v1/domains/"+url.PathEscape(uuid)+"/actions/"+action), nil, nil)
}

// Delete switches a machine off and deletes it with its disks.
func (c *Client) Delete(ctx context.Context, host, uuid string) error {
	if d, err := c.Machine(ctx, host, uuid); err == nil && d.State != "shutoff" {
		_ = c.Action(ctx, host, uuid, "force-off")
	}
	err := c.Do(ctx, http.MethodDelete, on(host, "/api/v1/domains/"+url.PathEscape(uuid)+"?storage=remove"), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Project is a VirtoMir project.
type Project struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Members      []ProjectMember `json:"members"`
	Limits       ProjectLimits   `json:"limits"`
	StoragePools []string        `json:"storagePools"`
	Networks     []string        `json:"networks"`
}

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

func (c *Client) Project(ctx context.Context, id string) (Project, error) {
	var p Project
	err := c.Do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(id), nil, &p)
	return p, err
}

func (c *Client) CreateProject(ctx context.Context, p Project) error {
	return c.Do(ctx, http.MethodPost, "/api/v1/projects", p, nil)
}

func (c *Client) UpdateProject(ctx context.Context, p Project) error {
	return c.Do(ctx, http.MethodPut, "/api/v1/projects/"+url.PathEscape(p.ID), p, nil)
}

func (c *Client) DeleteProject(ctx context.Context, id string) error {
	err := c.Do(ctx, http.MethodDelete, "/api/v1/projects/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
