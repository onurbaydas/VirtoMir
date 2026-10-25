package virt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// GuestExecResult is what a command run inside a machine by its guest agent left behind.
type GuestExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// guestExecCmd is the agent's guest-exec request for argv, with its output captured.
func guestExecCmd(argv []string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", invalidf("Name the program to run.")
	}
	raw, err := json.Marshal(map[string]any{
		"execute":   "guest-exec",
		"arguments": map[string]any{"path": argv[0], "arg": argv[1:], "capture-output": true},
	})
	return string(raw), err
}

// parseExecStatus reads a guest-exec-status answer; done is false while the command runs.
func parseExecStatus(answer string) (res GuestExecResult, done bool, err error) {
	var a struct {
		Return struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			Signal   int    `json:"signal"`
			Out      string `json:"out-data"`
			Err      string `json:"err-data"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(answer), &a); err != nil {
		return res, false, fmt.Errorf("guest-exec-status: %w", err)
	}
	if !a.Return.Exited {
		return res, false, nil
	}
	out, err1 := base64.StdEncoding.DecodeString(a.Return.Out)
	errOut, err2 := base64.StdEncoding.DecodeString(a.Return.Err)
	if err1 != nil || err2 != nil {
		return res, true, fmt.Errorf("guest-exec-status: output is not base64")
	}
	res = GuestExecResult{ExitCode: a.Return.ExitCode, Stdout: string(out), Stderr: string(errOut)}
	if a.Return.Signal != 0 {
		res.ExitCode = 128 + a.Return.Signal
	}
	return res, true, nil
}

// GuestExec runs argv inside a running machine through its guest agent and waits for it to
// end, at most until ctx is done (the command keeps running inside the guest then).
func (c *Client) GuestExec(ctx context.Context, uuid string, argv []string) (GuestExecResult, error) {
	cmd, err := guestExecCmd(argv)
	if err != nil {
		return GuestExecResult{}, err
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return GuestExecResult{}, err
	}
	x, err := c.parseDomain(l, d, false)
	if err != nil {
		return GuestExecResult{}, err
	}
	if !agentConnected(x) {
		return GuestExecResult{}, refusedf("%s has no connected guest agent: start the machine and install qemu-guest-agent in it.", d.Name)
	}
	answer, err := l.QEMUDomainAgentCommand(d, cmd, 10, 0)
	if err != nil {
		return GuestExecResult{}, fmt.Errorf("guest-exec: %w", classify(err))
	}
	var started struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if len(answer) == 0 || json.Unmarshal([]byte(answer[0]), &started) != nil {
		return GuestExecResult{}, fmt.Errorf("guest-exec: unexpected answer")
	}
	status := fmt.Sprintf(`{"execute":"guest-exec-status","arguments":{"pid":%d}}`, started.Return.PID)
	for wait := 100 * time.Millisecond; ; wait = min(wait*2, 2*time.Second) {
		select {
		case <-ctx.Done():
			return GuestExecResult{}, fmt.Errorf("%s inside %s: %w", argv[0], d.Name, ctx.Err())
		case <-time.After(wait):
		}
		a, err := l.QEMUDomainAgentCommand(d, status, 10, 0)
		if err != nil {
			return GuestExecResult{}, fmt.Errorf("guest-exec-status: %w", classify(err))
		}
		if len(a) == 0 {
			continue
		}
		res, done, err := parseExecStatus(a[0])
		if err != nil || done {
			return res, err
		}
	}
}
