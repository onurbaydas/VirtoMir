package ha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockBMC is a Redfish BMC (DMTF ComputerSystem schema) whose behaviour each test sets.
type mockBMC struct {
	mu sync.Mutex
	// power is "On" or "Off"; offAfter is how many power reads still say "On" after a
	// ForceOff (a BMC reports the new state a little later); stuck ignores ForceOff.
	power    string
	offAfter int
	stuck    bool
	// systems and system replace the JSON answers when set (malformed BMCs).
	systems, system string
	resets          []string
	reads           int
	bootOverride    string
}

func (b *mockBMC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "root" || p != "calvin" {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"Base.1.8.GeneralError","message":"Invalid credentials"}}`)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.URL.Path == "/redfish/v1/Systems" && r.Method == http.MethodGet:
		io.WriteString(w, cmpOr(b.systems, `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`))
	case r.URL.Path == "/redfish/v1/Systems/1" && r.Method == http.MethodGet:
		b.reads++
		if b.system != "" {
			io.WriteString(w, b.system)
			return
		}
		state := b.power
		if b.offAfter > 0 {
			b.offAfter--
			state = "On"
		}
		io.WriteString(w, `{"Id":"1","PowerState":"`+state+`"}`)
	case r.URL.Path == "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" && r.Method == http.MethodPost:
		var body struct{ ResetType string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.resets = append(b.resets, body.ResetType)
		switch {
		case b.stuck:
		case body.ResetType == "ForceOff":
			b.power = "Off"
		case body.ResetType == "ForceRestart", body.ResetType == "On":
			b.power = "On"
		}
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/redfish/v1/Systems/1" && r.Method == http.MethodPatch:
		var body struct {
			Boot struct{ BootSourceOverrideTarget, BootSourceOverrideEnabled string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.bootOverride = body.Boot.BootSourceOverrideTarget + "/" + body.Boot.BootSourceOverrideEnabled
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func redfishAgent(url string) FenceAgent {
	return FenceAgent{NodeID: "n2", Driver: "redfish", Endpoint: url, Username: "root", Password: "calvin", InsecureTLS: true}
}

func fastFencer() *Fencer {
	f := NewFencer()
	f.Poll = time.Millisecond
	return f
}

func TestRedfishOffWaitsForThePowerToGo(t *testing.T) {
	bmc := &mockBMC{power: "On", offAfter: 3}
	srv := httptest.NewTLSServer(bmc)
	defer srv.Close()
	res, err := fastFencer().Fence(t.Context(), redfishAgent(srv.URL), FenceOff)
	if err != nil || res.Power != "off" || !strings.Contains(res.Message, "power is off") {
		t.Fatalf("off = %+v, %v", res, err)
	}
	if !slices.Equal(bmc.resets, []string{"ForceOff"}) || bmc.reads != 4 {
		t.Errorf("resets %v, power reads %d (want ForceOff once, polled until off)", bmc.resets, bmc.reads)
	}
}

func TestRedfishReboot(t *testing.T) {
	bmc := &mockBMC{power: "On"}
	srv := httptest.NewTLSServer(bmc)
	defer srv.Close()
	res, err := fastFencer().Fence(t.Context(), redfishAgent(srv.URL), FenceReboot)
	if err != nil || res.Power != "on" || !slices.Equal(bmc.resets, []string{"ForceRestart"}) {
		t.Fatalf("reboot = %+v, %v, resets %v", res, err, bmc.resets)
	}
}

func TestRedfishFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bmc   *mockBMC
		agent func(FenceAgent) FenceAgent
		want  string
	}{
		{"wrong password", &mockBMC{power: "On"}, func(a FenceAgent) FenceAgent { a.Password = "nope"; return a }, "401"},
		{"no systems", &mockBMC{power: "On", systems: `{"Members":[]}`}, nil, "no computer system"},
		{"malformed systems", &mockBMC{power: "On", systems: `{"Members":[`}, nil, "unexpected EOF"},
		{"malformed power state", &mockBMC{power: "On", system: `{"PowerState":`}, nil, "unexpected EOF"},
		{"unknown power state", &mockBMC{power: "On", system: `{"PowerState":"PoweringOn"}`}, nil, `the power "poweringon"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(tc.bmc)
			defer srv.Close()
			a := redfishAgent(srv.URL)
			if tc.agent != nil {
				a = tc.agent(a)
			}
			// waitOff polls for up to a minute; the context ends it sooner.
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			res, err := fastFencer().Fence(ctx, a, FenceOff)
			if err == nil || res.Power == "off" {
				t.Fatalf("fenced: %+v", res)
			}
			if !strings.Contains(res.Message, tc.want) {
				t.Errorf("message %q, want it to mention %q", res.Message, tc.want)
			}
		})
	}
}

// The BMC takes the command but the server stays on: never a successful fence, so
// failover does not start its machines a second time.
func TestRedfishAcceptedButStaysOn(t *testing.T) {
	bmc := &mockBMC{power: "On", stuck: true}
	srv := httptest.NewTLSServer(bmc)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	res, err := fastFencer().Fence(ctx, redfishAgent(srv.URL), FenceOff)
	if err == nil || res.Power == "off" || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), `"on"`) {
		t.Fatalf("a server that stayed on: %+v, %v", res, err)
	}
	bmc.mu.Lock() // the BMC may still be answering a poll the deadline cut short
	reads := bmc.reads
	bmc.mu.Unlock()
	if reads < 2 {
		t.Errorf("power polled %d times, want it to keep checking", reads)
	}
}

func TestRedfishUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(&mockBMC{})
	url := srv.URL
	srv.Close()
	_, err := fastFencer().Fence(t.Context(), redfishAgent(url), FenceStatus)
	if err == nil || !strings.Contains(err.Error(), "cannot reach the BMC") {
		t.Errorf("err = %v", err)
	}
}

// fakeIPMI answers ipmitool commands like a BMC; err makes every call fail like ipmitool does.
type fakeIPMI struct {
	power string
	out   string // replaces the status answer when set
	err   error
	calls []string
	args  []string
}

func (f *fakeIPMI) run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	if name != "ipmitool" {
		return nil, errors.New("not ipmitool: " + name)
	}
	f.args = args
	cmd := strings.Join(args[len(args)-3:], " ")
	f.calls = append(f.calls, cmd)
	if f.err != nil {
		return []byte(f.err.Error()), f.err
	}
	switch cmd {
	case "chassis power off":
		f.power = "off"
		return []byte("Chassis Power Control: Down/Off\n"), nil
	case "chassis power cycle", "chassis power reset", "chassis power on":
		f.power = "on"
		return []byte("Chassis Power Control: Up/On\n"), nil
	case "chassis bootdev pxe":
		return []byte("Set Boot Device to pxe\n"), nil
	case "chassis power status":
		if f.out != "" {
			return []byte(f.out), nil
		}
		return []byte("Chassis Power is " + f.power + "\n"), nil
	}
	return nil, errors.New("unexpected " + cmd)
}

func TestIPMIStatusRebootAndPort(t *testing.T) {
	b := &fakeIPMI{power: "off"}
	f := &Fencer{Poll: time.Millisecond, Run: b.run}
	a := FenceAgent{Driver: "ipmi", Endpoint: "https://10.0.0.102:6230", Username: "admin", Password: "pw"}
	if res, err := f.Fence(t.Context(), a, FenceStatus); err != nil || res.Power != "off" {
		t.Fatalf("status = %+v, %v", res, err)
	}
	if want := []string{"-I", "lanplus", "-H", "10.0.0.102", "-U", "admin", "-E", "-p", "6230"}; !slices.Equal(b.args[:len(want)], want) {
		t.Errorf("ipmitool args = %v", b.args)
	}
	if slices.Contains(b.args, "pw") {
		t.Error("the password is on the command line")
	}
	b.calls = nil
	if res, err := f.Fence(t.Context(), a, FenceReboot); err != nil || res.Power != "on" ||
		!slices.Equal(b.calls, []string{"chassis power cycle", "chassis power status"}) {
		t.Fatalf("reboot = %+v, %v, calls %v", res, err, b.calls)
	}
}

func TestIPMIFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *fakeIPMI
		want string
	}{
		{"invalid credentials", &fakeIPMI{power: "on", err: errors.New("Error: Unable to establish IPMI v2 / RMCP+ session")}, "RMCP+ session"},
		{"unreachable", &fakeIPMI{power: "on", err: errors.New("Error: Unable to establish IPMI v2 / RMCP+ session: timeout")}, "timeout"},
		{"unexpected answer", &fakeIPMI{power: "on", out: "Chassis Power Control: Unknown\n"}, "unexpected answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &Fencer{Poll: time.Millisecond, Run: tc.b.run}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			res, err := f.Fence(ctx, FenceAgent{Driver: "ipmi", Endpoint: "10.0.0.102"}, FenceOff)
			if err == nil || res.Power == "off" {
				t.Fatalf("fenced: %+v", res)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestUnknownFenceDriver(t *testing.T) {
	if _, err := NewFencer().Fence(t.Context(), FenceAgent{Driver: "ilo-ssh"}, FenceOff); err == nil || !strings.Contains(err.Error(), "unknown fence driver") {
		t.Errorf("err = %v", err)
	}
}

func TestBootFromNetwork(t *testing.T) {
	bmc := &mockBMC{power: "Off"}
	srv := httptest.NewTLSServer(bmc)
	defer srv.Close()
	f := fastFencer()
	if res, err := f.Fence(t.Context(), redfishAgent(srv.URL), FencePXE); err != nil || res.Power != "on" {
		t.Fatalf("redfish pxe = %+v, %v", res, err)
	}
	if bmc.bootOverride != "Pxe/Once" || !slices.Equal(bmc.resets, []string{"On"}) {
		t.Errorf("override %q, resets %v", bmc.bootOverride, bmc.resets)
	}
	if _, err := f.Fence(t.Context(), redfishAgent(srv.URL), FencePXE); err != nil || bmc.resets[1] != "ForceRestart" {
		t.Errorf("redfish pxe while on: %v %v", err, bmc.resets)
	}

	ip := &fakeIPMI{power: "on"}
	fi := &Fencer{Poll: time.Millisecond, Run: ip.run}
	a := FenceAgent{Driver: "ipmi", Endpoint: "10.0.0.102", Username: "admin", Password: "pw"}
	if res, err := fi.Fence(t.Context(), a, FencePXE); err != nil || res.Power != "on" ||
		!slices.Equal(ip.calls, []string{"chassis bootdev pxe", "chassis power status", "chassis power reset"}) {
		t.Errorf("ipmi pxe = %+v, %v, calls %v", res, err, ip.calls)
	}
}
