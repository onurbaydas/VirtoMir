package baremetal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func host() Host {
	return Host{Name: "hv3", MAC: "52:54:00:AA:BB:01", PasswordHash: "$6$salt$hash", SSHKeys: []string{"ssh-ed25519 AAAA ops@x"}}
}

func settings() Settings {
	s := Settings{Enabled: true, Address: "10.0.0.11"}
	_ = s.Normalize()
	return s
}

func TestValidate(t *testing.T) {
	h := host()
	if err := h.Validate(); err != nil || h.MAC != "52:54:00:aa:bb:01" {
		t.Fatalf("%v %s", err, h.MAC)
	}
	for name, change := range map[string]func(*Host){
		"bad name":     func(h *Host) { h.Name = "HV_3!" },
		"bad mac":      func(h *Host) { h.MAC = "nope" },
		"bad disk":     func(h *Host) { h.Disk = "sda; rm -rf /" },
		"bad bmc":      func(h *Host) { h.BMC = BMC{Driver: "telnet"} },
		"no endpoint":  func(h *Host) { h.BMC = BMC{Driver: "redfish"} },
		"bad static":   func(h *Host) { h.Static = &Static{Address: "10.0.0.21"} },
		"gateway away": func(h *Host) { h.Static = &Static{Address: "10.0.0.21/24", Gateway: "10.1.0.1"} },
		"bad key":      func(h *Host) { h.SSHKeys = []string{"hello"} },
		"no way in":    func(h *Host) { h.SSHKeys, h.PasswordHash = nil, "" },
	} {
		h := host()
		change(&h)
		if err := h.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, s := range map[string]Settings{
		"no address":          {Enabled: true},
		"proxy, no interface": {Enabled: true, Address: "10.0.0.11", Mode: ModeProxy},
		"bad repo":            {Repo: "ftp://x"},
		"bad mode":            {Mode: "magic"},
	} {
		if err := s.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s := settings(); s.Base() != "http://10.0.0.11:8424" || s.Mode != ModeExternal || s.Repo != DefaultRepo {
		t.Errorf("defaults %+v", s)
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baremetal.json")
	s, _ := Open(path)
	h, err := s.Add(host())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(host()); !errors.Is(err, ErrExists) {
		t.Errorf("same MAC: %v", err)
	}
	if _, err := s.Update(h.ID, func(x *Host) error {
		x.Token = "tok"
		x.BMC = BMC{Driver: "ipmi", Endpoint: "10.0.0.103", Password: "pw"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, _ := Open(path)
	got, ok := r.ByToken("tok")
	if !ok || got.BMC.Password != "pw" || got.Public().BMC.Password != "" || got.Public().Token != "" || got.Public().PasswordHash != "" {
		t.Errorf("reloaded %+v", got)
	}
	if _, ok := r.ByMAC("52-54-00-AA-BB-01"); !ok {
		t.Error("MAC in another spelling not found")
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := s.Update(h.ID, func(x *Host) error { x.Name = "BAD!"; return nil }); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid update: %v", err)
	}
	if err := s.Delete(h.ID); err != nil || s.Delete(h.ID) == nil {
		t.Errorf("delete: %v", err)
	}
}

func TestScripts(t *testing.T) {
	s := settings()
	h := host()
	_ = h.Validate()
	h.State, h.Token = StateInstalling, "t0k"
	if sc := IPXE(h, true, s); !strings.Contains(sc, "kernel http://10.0.0.11:8424/boot/files/debian13/linux auto=true priority=critical preseed/url=http://10.0.0.11:8424/boot/t0k/preseed.cfg hostname=hv3") ||
		!strings.Contains(sc, "BOOTIF=01-${netX/mac:hexhyp}") {
		t.Errorf("ipxe:\n%s", sc)
	}
	for _, sc := range []string{IPXE(Host{}, false, s), IPXE(Host{State: StateInstalled}, true, s)} {
		if !strings.Contains(sc, "\nexit\n") {
			t.Errorf("not installing must boot from disk:\n%s", sc)
		}
	}
	p := Preseed(h, s)
	for _, want := range []string{
		"d-i passwd/user-password-crypted password $6$salt$hash",
		`partman/early_command string debconf-set partman-auto/disk "$(list-devices disk | head -n1)"`,
		"d-i mirror/http/hostname string deb.debian.org",
		"curl -fsS http://10.0.0.11:8424/boot/t0k/late.sh",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("preseed lacks %q:\n%s", want, p)
		}
	}
	h.Disk = "/dev/nvme0n1"
	if p := Preseed(h, s); !strings.Contains(p, "partman-auto/disk string /dev/nvme0n1") || strings.Contains(p, "early_command") {
		t.Errorf("disk not used:\n%s", p)
	}
	h.Static = &Static{Address: "10.0.0.21/24", Gateway: "10.0.0.1", DNS: []string{"10.0.0.53"}}
	l := LateScript(h, s)
	for _, want := range []string{
		`echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] https://onurbaydas.github.io/VirtoMir/apt stable main"`,
		"apt-get install -y virtomir",
		"ssh-ed25519 AAAA ops@x",
		"address 10.0.0.21/24", "gateway 10.0.0.1", "dns-nameservers 10.0.0.53",
		"VIRTOMIR_JOIN_CODE=$(curl -fsS -X POST http://10.0.0.11:8424/boot/t0k/join)",
		"/usr/bin/virtomird $VIRTOMIRD_ARGS join\nsystemctl restart virtomird",
		"systemctl enable virtomir-join.service",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("late script lacks %q", want)
		}
	}
}

func TestBootServer(t *testing.T) {
	st, _ := Open("")
	_ = st.SetSettings(Settings{Enabled: true, Address: "10.0.0.11"})
	h, _ := st.Add(host())
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "debian13"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "debian13", "linux"), []byte("kernel"), 0o644)
	invited := ""
	srv := httptest.NewServer(&Server{Store: st, Dir: dir, RepoKey: []byte("KEY"),
		Invite: func(_ context.Context, name string) (string, error) { invited = name; return "vmj1-code", nil }})
	defer srv.Close()
	ua := "iPXE/1.21.1"
	call := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("User-Agent", ua)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if _, b := call("GET", "/boot/ipxe"); !strings.Contains(b, "chain http://10.0.0.11:8424/boot/ipxe?mac=${netX/mac}") {
		t.Errorf("chain: %s", b)
	}
	if _, b := call("GET", "/boot/ipxe?mac=52:54:00:aa:bb:01"); !strings.Contains(b, "exit") {
		t.Errorf("registered host must boot from disk: %s", b)
	}
	ua = "Wget"
	if c, _ := call("GET", "/boot/ipxe"); c != 404 {
		t.Errorf("the installer of a machine that did not boot from here: %d", c)
	}
	ua = "iPXE/1.21.1"
	_, _ = st.Update(h.ID, func(x *Host) error { x.State, x.Token = StateInstalling, "t0k"; return nil })
	if _, b := call("GET", "/boot/ipxe?mac=52-54-00-AA-BB-01"); !strings.Contains(b, "/boot/t0k/preseed.cfg") {
		t.Errorf("installing: %s", b)
	}
	// The installer takes DHCP's boot file name for its preseed URL.
	ua = "Wget"
	if c, b := call("GET", "/boot/ipxe"); c != 200 || !strings.Contains(b, "d-i passwd/username string virtomir") {
		t.Errorf("installer asking the boot file name: %d %s", c, b)
	}
	ua = "iPXE/1.21.1"
	if c, b := call("GET", "/boot/files/debian13/linux"); c != 200 || b != "kernel" {
		t.Errorf("kernel: %d %s", c, b)
	}
	if _, b := call("GET", "/boot/files/virtomir.asc"); b != "KEY" {
		t.Errorf("key: %s", b)
	}
	if c, _ := call("GET", "/boot/nope/preseed.cfg"); c != 404 {
		t.Errorf("unknown token: %d", c)
	}
	if c, _ := call("POST", "/boot/t0k/join"); c != 409 {
		t.Errorf("join before installed: %d", c)
	}
	if c, _ := call("POST", "/boot/t0k/preseed.cfg"); c != 405 {
		t.Errorf("wrong method: %d", c)
	}
	for _, step := range []struct{ method, path, want string }{
		{"GET", "/boot/t0k/preseed.cfg", "d-i passwd/username string virtomir"},
		{"GET", "/boot/t0k/late.sh", "apt-get install -y virtomir"},
		{"POST", "/boot/t0k/installed", "ok"},
		{"POST", "/boot/t0k/join", "vmj1-code"},
		{"POST", "/boot/t0k/joined", "ok"},
	} {
		if c, b := call(step.method, step.path); c != 200 || !strings.Contains(b, step.want) {
			t.Fatalf("%s %s: %d %s", step.method, step.path, c, b)
		}
	}
	got, _ := st.Host(h.ID)
	if got.State != StateJoined || got.Token != "" || invited != "hv3" {
		t.Errorf("after the install: %s token %q invited %q", got.State, got.Token, invited)
	}
	if c, _ := call("GET", "/boot/t0k/preseed.cfg"); c != 404 {
		t.Errorf("token still works after joining: %d", c)
	}
}

func TestFetchInstaller(t *testing.T) {
	files := map[string]string{"linux": "kernel bytes", "initrd.gz": "initrd bytes"}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	bad := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, installerPath+"/") {
		case "SHA256SUMS":
			for n, c := range files {
				io.WriteString(w, sum(c)+"  ./netboot/debian-installer/amd64/"+n+"\n")
			}
			io.WriteString(w, sum("x")+"  ./netboot/mini.iso\n")
		case "netboot/debian-installer/amd64/linux":
			if bad {
				io.WriteString(w, "tampered")
				return
			}
			io.WriteString(w, files["linux"])
		case "netboot/debian-installer/amd64/initrd.gz":
			io.WriteString(w, files["initrd.gz"])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	mirror := strings.TrimPrefix(srv.URL, "https://")
	if InstallerReady(dir) {
		t.Error("ready before download")
	}
	if err := FetchInstaller(context.Background(), srv.Client(), mirror, dir); err != nil {
		t.Fatal(err)
	}
	if !InstallerReady(dir) {
		t.Error("not ready after download")
	}
	bad = true
	_ = os.RemoveAll(dir)
	if err := FetchInstaller(context.Background(), srv.Client(), mirror, dir); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("tampered kernel: %v", err)
	}
	if InstallerReady(dir) {
		t.Error("a tampered kernel counts as ready")
	}
}

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestProxy(t *testing.T) {
	s := Settings{Enabled: true, Address: "10.0.0.11", Mode: ModeProxy, Interface: "br0"}
	_ = s.Normalize()
	conf := DnsmasqConfig(s, "10.0.0.0", []string{"52:54:00:aa:bb:01"}, "/tftp")
	for _, want := range []string{"port=0", "interface=br0", "dhcp-range=10.0.0.0,proxy", "dhcp-host=52:54:00:aa:bb:01,set:virtomir",
		"dhcp-boot=tag:virtomir,tag:ipxe,http://10.0.0.11:8424/boot/ipxe", `pxe-service=tag:virtomir,tag:!ipxe,X86-64_EFI,"VirtoMir install",ipxe.efi`} {
		if !strings.Contains(conf, want) {
			t.Errorf("dnsmasq lacks %q:\n%s", want, conf)
		}
	}
	old := ipxeDir
	defer func() { ipxeDir = old }()
	ipxeDir = t.TempDir()
	dir := t.TempDir()
	r := &fakeRunner{}
	if err := ApplyProxy(context.Background(), r, s, "10.0.0.0", []string{"52:54:00:aa:bb:01"}, dir); !errors.Is(err, ErrNoIPXE) {
		t.Errorf("without iPXE: %v", err)
	}
	for _, f := range []string{"undionly.kpxe", "ipxe.efi"} {
		_ = os.WriteFile(filepath.Join(ipxeDir, f), []byte("x"), 0o644)
	}
	r.calls = nil
	if err := ApplyProxy(context.Background(), r, s, "10.0.0.0", []string{"52:54:00:aa:bb:01"}, dir); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || !strings.HasPrefix(r.calls[1], "systemd-run --no-ask-password --unit="+Unit) {
		t.Errorf("calls %v", r.calls)
	}
	r.calls = nil
	_ = ApplyProxy(context.Background(), r, s, "10.0.0.0", nil, dir)
	if len(r.calls) != 1 || r.calls[0] != "systemctl --no-ask-password stop "+Unit {
		t.Errorf("nothing to install still runs dnsmasq: %v", r.calls)
	}
}
