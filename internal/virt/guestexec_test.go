package virt

import (
	"encoding/json"
	"testing"
)

func TestGuestExecCmd(t *testing.T) {
	cmd, err := guestExecCmd([]string{"/bin/sh", "-c", "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Execute   string
		Arguments struct {
			Path    string
			Arg     []string
			Capture bool `json:"capture-output"`
		}
	}
	if err := json.Unmarshal([]byte(cmd), &got); err != nil {
		t.Fatal(err)
	}
	if got.Execute != "guest-exec" || got.Arguments.Path != "/bin/sh" || len(got.Arguments.Arg) != 2 || !got.Arguments.Capture {
		t.Errorf("command %s", cmd)
	}
	if _, err := guestExecCmd(nil); err == nil {
		t.Error("empty argv accepted")
	}
}

func TestParseExecStatus(t *testing.T) {
	if _, done, err := parseExecStatus(`{"return":{"exited":false}}`); done || err != nil {
		t.Errorf("running: done=%v err=%v", done, err)
	}
	res, done, err := parseExecStatus(`{"return":{"exited":true,"exitcode":3,"out-data":"aGkK","err-data":"b29wcwo="}}`)
	if err != nil || !done || res.ExitCode != 3 || res.Stdout != "hi\n" || res.Stderr != "oops\n" {
		t.Errorf("exited: %+v done=%v err=%v", res, done, err)
	}
	if res, _, _ := parseExecStatus(`{"return":{"exited":true,"signal":9}}`); res.ExitCode != 137 {
		t.Errorf("killed: exit %d", res.ExitCode)
	}
	if _, _, err := parseExecStatus(`{"return":{"exited":true,"out-data":"!!"}}`); err == nil {
		t.Error("bad base64 accepted")
	}
}
