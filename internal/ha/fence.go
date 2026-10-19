package ha

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// FenceAction is what a fence agent is asked to do.
type FenceAction string

const (
	FenceOff    FenceAction = "off"    // hard power off (what failover needs)
	FenceReboot FenceAction = "reboot" // power cycle
	FenceStatus FenceAction = "status" // only ask: is it on?
)

// FenceResult is the outcome of one fence call.
type FenceResult struct {
	NodeID string      `json:"nodeId"`
	Driver string      `json:"driver"`
	Action FenceAction `json:"action"`
	// Power is what the BMC reports afterwards: "on", "off" or "" (unknown).
	Power   string    `json:"power,omitempty"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// ErrManualFence: the member has no BMC agent; an admin must confirm it is off.
var ErrManualFence = errors.New("this host has no fence agent: switch it off yourself and confirm it on the HA page")

// Fencer talks to BMCs. Its hooks are replaced in tests.
type Fencer struct {
	// Run executes ipmitool (env carries IPMI_PASSWORD, so it never shows in ps).
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// HTTP returns the client for a Redfish BMC.
	HTTP func(insecure bool) *http.Client
	// Poll is how long to wait between power checks after "off".
	Poll time.Duration
}

func NewFencer() *Fencer {
	return &Fencer{
		Run: func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
			if _, err := exec.LookPath(name); err != nil {
				return nil, fmt.Errorf("%s is not installed on this host (apt install %s)", name, name)
			}
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = append(os.Environ(), env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return out, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
			}
			return out, nil
		},
		HTTP: func(insecure bool) *http.Client {
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure} //nolint:gosec // opt-in per agent for self-signed BMCs
			return &http.Client{Transport: tr, Timeout: 20 * time.Second}
		},
		Poll: 2 * time.Second,
	}
}

// Fence runs action against a member's BMC. For FenceOff it returns successfully only once
// the BMC reports the power off: failover must never start a machine twice.
func (f *Fencer) Fence(ctx context.Context, a FenceAgent, action FenceAction) (FenceResult, error) {
	res := FenceResult{NodeID: a.NodeID, Driver: a.Driver, Action: action, Time: time.Now()}
	var err error
	switch a.Driver {
	case "ipmi":
		res.Power, err = f.ipmi(ctx, a, action)
	case "redfish":
		res.Power, err = f.redfish(ctx, a, action)
	case "manual", "":
		return res, ErrManualFence
	default:
		return res, fmt.Errorf("unknown fence driver %q", a.Driver)
	}
	if err != nil {
		res.Message = err.Error()
		return res, err
	}
	if action == FenceOff && res.Power != "off" {
		err = fmt.Errorf("the BMC accepted power off but still reports %q", res.Power)
		res.Message = err.Error()
		return res, err
	}
	res.Message = fmt.Sprintf("%s via %s: power is %s", action, a.Driver, res.Power)
	return res, nil
}

// ---- IPMI ----------------------------------------------------------------------------------

func (f *Fencer) ipmi(ctx context.Context, a FenceAgent, action FenceAction) (string, error) {
	host, port := a.Endpoint, ""
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	}
	base := []string{"-I", "lanplus", "-H", host, "-U", a.Username, "-E"}
	if port != "" {
		base = append(base, "-p", port)
	}
	env := []string{"IPMI_PASSWORD=" + a.Password}
	run := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := f.Run(c, env, "ipmitool", append(append([]string{}, base...), args...)...)
		return string(out), err
	}
	status := func() (string, error) {
		out, err := run("chassis", "power", "status")
		if err != nil {
			return "", err
		}
		switch l := strings.ToLower(out); {
		case strings.Contains(l, "is off"):
			return "off", nil
		case strings.Contains(l, "is on"):
			return "on", nil
		}
		return "", fmt.Errorf("unexpected answer from the BMC: %s", strings.TrimSpace(out))
	}
	switch action {
	case FenceStatus:
		return status()
	case FenceReboot:
		if _, err := run("chassis", "power", "cycle"); err != nil {
			return "", err
		}
		return status()
	}
	if _, err := run("chassis", "power", "off"); err != nil {
		return "", err
	}
	return f.waitOff(ctx, status)
}

// waitOff polls the power state for up to a minute (BMCs report "off" a little later).
func (f *Fencer) waitOff(ctx context.Context, status func() (string, error)) (string, error) {
	deadline := time.Now().Add(time.Minute)
	var last string
	var lastErr error
	answered := false
	for {
		p, err := status()
		if err == nil && p == "off" {
			return p, nil
		}
		if ctx.Err() == nil { // an answer of the BMC, not a call cut short
			last, lastErr, answered = p, err, true
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", lastErr
			}
			return last, nil
		}
		select {
		case <-ctx.Done():
			// Say what the BMC last answered: that is why the host counts as not fenced.
			switch {
			case !answered:
				return "", fmt.Errorf("%w; the BMC did not answer", ctx.Err())
			case lastErr != nil:
				return "", fmt.Errorf("%w; the BMC last answered: %v", ctx.Err(), lastErr)
			}
			return "", fmt.Errorf("%w; the BMC still reports the power %q", ctx.Err(), last)
		case <-time.After(f.Poll):
		}
	}
}

// ---- Redfish -------------------------------------------------------------------------------

func (f *Fencer) redfish(ctx context.Context, a FenceAgent, action FenceAction) (string, error) {
	base := strings.TrimRight(a.Endpoint, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	client := f.HTTP(a.InsecureTLS)
	call := func(method, path string, body any, out any) error {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
		if err != nil {
			return err
		}
		req.SetBasicAuth(a.Username, a.Password)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("cannot reach the BMC: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("the BMC answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	var systems struct {
		Members []struct {
			ID string `json:"@odata.id"`
		}
	}
	if err := call(http.MethodGet, "/redfish/v1/Systems", nil, &systems); err != nil {
		return "", err
	}
	if len(systems.Members) == 0 {
		return "", errors.New("the BMC lists no computer system")
	}
	sys := systems.Members[0].ID
	status := func() (string, error) {
		var s struct{ PowerState string }
		if err := call(http.MethodGet, sys, nil, &s); err != nil {
			return "", err
		}
		return strings.ToLower(s.PowerState), nil
	}
	switch action {
	case FenceStatus:
		return status()
	case FenceReboot:
		if err := call(http.MethodPost, sys+"/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "ForceRestart"}, nil); err != nil {
			return "", err
		}
		return status()
	}
	if err := call(http.MethodPost, sys+"/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "ForceOff"}, nil); err != nil {
		return "", err
	}
	return f.waitOff(ctx, status)
}
