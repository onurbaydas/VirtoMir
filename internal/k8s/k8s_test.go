package k8s

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func spec() Spec {
	return Spec{Name: "prod", Image: "/var/lib/libvirt/images/debian-13.qcow2", Network: "default", Workers: 2}
}

func TestNormalize(t *testing.T) {
	s := spec()
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Version != DefaultVersion || s.ControlPlanes != 1 || s.CNI != CNIFlannel || s.PodCIDR != DefaultPodCIDR || s.ControlPlane.MemoryMiB != 2048 {
		t.Errorf("defaults %+v", s)
	}
	for name, change := range map[string]func(*Spec){
		"bad name":      func(s *Spec) { s.Name = "Prod_1" },
		"two cps":       func(s *Spec) { s.ControlPlanes = 2 },
		"three no vip":  func(s *Spec) { s.ControlPlanes = 3 },
		"old version":   func(s *Spec) { s.Version = "1.20" },
		"small cp":      func(s *Spec) { s.ControlPlane = Size{VCPUs: 1, MemoryMiB: 2048, DiskGiB: 20} },
		"no image":      func(s *Spec) { s.Image = "" },
		"no network":    func(s *Spec) { s.Network = "" },
		"overlap":       func(s *Spec) { s.PodCIDR, s.ServiceCIDR = "10.0.0.0/16", "10.0.128.0/20" },
		"unmasked cidr": func(s *Spec) { s.PodCIDR = "10.244.1.0/16" },
		"tiny pods":     func(s *Spec) { s.PodCIDR = "10.244.0.0/24" },
		"bad cni":       func(s *Spec) { s.CNI = "weave" },
		"bad key":       func(s *Spec) { s.SSHKeys = []string{"hello"} },
		"ipv6 vip":      func(s *Spec) { s.ControlPlanes, s.VIP = 3, "fd00::1" },
	} {
		s := spec()
		change(&s)
		if err := s.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s = spec()
	s.ControlPlanes, s.IPPool = 3, "lab"
	if err := s.Normalize(); err != nil {
		t.Errorf("three with a pool: %v", err)
	}
	if NextVersion("1.36") != "1.37" || NextVersion(Versions[len(Versions)-1]) != "" {
		t.Error("next version")
	}
}

func TestPlan(t *testing.T) {
	s := spec()
	s.ControlPlanes, s.VIP = 3, "10.0.0.5"
	_ = s.Normalize()
	nodes := Plan(s, []string{"a", "b"})
	got := ""
	for _, n := range nodes {
		got += n.Name + "@" + n.HostID + " "
	}
	if got != "prod-cp-1@a prod-cp-2@b prod-cp-3@a prod-worker-1@b prod-worker-2@a " {
		t.Errorf("plan %s", got)
	}
	c := Cluster{Spec: s, Nodes: nodes}
	if NextWorkerName(c) != "prod-worker-3" {
		t.Error(NextWorkerName(c))
	}
}

func TestPKI(t *testing.T) {
	now := time.Now()
	p, err := NewPKI(now)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`).MatchString(p.Token) {
		t.Errorf("token %q", p.Token)
	}
	h, err := p.CAHash()
	if err != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(h) {
		t.Errorf("hash %q %v", h, err)
	}
	kc, err := p.Kubeconfig("prod", "https://10.0.0.5:6443", "virtomir-admin", now)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`client-certificate-data: (\S+)`).FindSubmatch(kc)
	if m == nil || !strings.Contains(string(kc), "server: https://10.0.0.5:6443") {
		t.Fatalf("kubeconfig:\n%s", kc)
	}
	raw, _ := base64.StdEncoding.DecodeString(string(m[1]))
	b, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ca, _, _ := parseCA(p.CACert, p.CAKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("client cert: %v", err)
	}
	if cert.Subject.Organization[0] != "kubeadm:cluster-admins" || cert.Subject.CommonName != "virtomir-admin" {
		t.Errorf("subject %v", cert.Subject)
	}
	if _, err := (PKI{}).CAHash(); err == nil {
		t.Error("hash without CA")
	}
}

// files decodes the write_files of a #cloud-config.
func files(t *testing.T, ud string) map[string]string {
	t.Helper()
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^  - path: (\S+)\n    permissions: "(\d+)"\n    encoding: b64\n    content: (\S+)$`)
	for _, m := range re.FindAllStringSubmatch(ud, -1) {
		raw, err := base64.StdEncoding.DecodeString(m[3])
		if err != nil {
			t.Fatal(err)
		}
		out[m[1]] = m[2] + " " + string(raw)
	}
	return out
}

func testCluster(t *testing.T, s Spec) Cluster {
	t.Helper()
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, err := NewPKI(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return Cluster{ID: "c1", Spec: s, Version: s.Version, Nodes: Plan(s, []string{"h1"}), PKI: p, State: StateProvisioning}
}

func TestUserData(t *testing.T) {
	s := spec()
	s.SSHKeys = []string{"ssh-ed25519 AAAA me@x"}
	c := testCluster(t, s)
	first, err := UserData(NodeInput{Cluster: c, Node: c.Nodes[0], Token: c.PKI.Token})
	if err != nil {
		t.Fatal(err)
	}
	f := files(t, first)
	if !strings.HasPrefix(first, "#cloud-config\n") || !strings.Contains(first, "qemu-guest-agent") || !strings.Contains(first, "ssh-ed25519 AAAA me@x") {
		t.Errorf("first:\n%s", first)
	}
	if !strings.HasPrefix(f["/etc/kubernetes/pki/ca.key"], "0600 -----BEGIN RSA PRIVATE KEY") || !strings.Contains(f["/etc/virtomir/kubeadm.yaml"], `token: "`+c.PKI.Token+`"`) {
		t.Errorf("files %v", f["/etc/virtomir/kubeadm.yaml"])
	}
	script := f["/usr/local/sbin/virtomir-k8s"]
	for _, want := range []string{"v1.36/deb", "kubeadm init --config", "kube-flannel.yml", "s#/usr/lib/cni#/opt/cni/bin#g", "echo done"} {
		if !strings.Contains(script, want) {
			t.Errorf("first script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "kube-vip") || strings.Contains(f["/etc/virtomir/kubeadm.yaml"], "controlPlaneEndpoint") {
		t.Error("a single control plane has no VIP")
	}
	if _, err := UserData(NodeInput{Cluster: c, Node: c.Nodes[1], Token: c.PKI.Token}); err == nil {
		t.Error("a worker without endpoint")
	}
	w, err := UserData(NodeInput{Cluster: c, Node: c.Nodes[1], Token: c.PKI.Token, Endpoint: "10.0.0.10:6443"})
	if err != nil {
		t.Fatal(err)
	}
	wf := files(t, w)
	hash, _ := c.PKI.CAHash()
	if _, ok := wf["/etc/kubernetes/pki/ca.key"]; ok {
		t.Error("a worker got the CA key")
	}
	if ws := wf["/usr/local/sbin/virtomir-k8s"]; !strings.Contains(ws, "kubeadm join 10.0.0.10:6443 --token "+c.PKI.Token+" --discovery-token-ca-cert-hash "+hash+" --node-name prod-worker-1") || strings.Contains(ws, "--control-plane") {
		t.Errorf("worker script:\n%s", ws)
	}

	// Three control planes behind kube-vip, calico.
	s = spec()
	s.ControlPlanes, s.VIP, s.CNI, s.PodCIDR = 3, "10.0.0.5", CNICalico, "10.200.0.0/16"
	c = testCluster(t, s)
	f = files(t, mustUD(t, NodeInput{Cluster: c, Node: c.Nodes[0], Token: "a"}))
	if !strings.Contains(f["/etc/virtomir/kubeadm.yaml"], `controlPlaneEndpoint: "10.0.0.5:6443"`) || !strings.Contains(f["/etc/virtomir/kube-vip.yaml"], `{name: address, value: "10.0.0.5"}`) {
		t.Errorf("vip files %v", f)
	}
	if sc := f["/usr/local/sbin/virtomir-k8s"]; !strings.Contains(sc, "super-admin.conf") || !strings.Contains(sc, "calico.yaml") || strings.Contains(sc, "sed -i 's#10.244.0.0/16") {
		t.Errorf("vip script:\n%s", sc)
	}
	f = files(t, mustUD(t, NodeInput{Cluster: c, Node: c.Nodes[1], Token: "a", Endpoint: "10.0.0.5:6443"}))
	if sc := f["/usr/local/sbin/virtomir-k8s"]; !strings.Contains(sc, "--control-plane --node-name prod-cp-2") || !strings.Contains(sc, "@KUBECONFIG@#/etc/kubernetes/admin.conf") {
		t.Errorf("cp2 script:\n%s", sc)
	}
	if _, ok := f["/etc/kubernetes/pki/sa.key"]; !ok {
		t.Error("cp2 lacks the service-account key")
	}
}

func mustUD(t *testing.T, in NodeInput) string {
	t.Helper()
	ud, err := UserData(in)
	if err != nil {
		t.Fatal(err)
	}
	return ud
}

func TestParseNodeIP(t *testing.T) {
	out := "2: flannel.1    inet 10.244.0.0/32 scope global flannel.1\n3: enp1s0    inet 192.168.122.50/24 brd 192.168.122.255 scope global dynamic enp1s0\n"
	if ip := parseNodeIP(out); ip != "192.168.122.50" {
		t.Errorf("ip %q", ip)
	}
	if minorOf("v1.34.2") != "1.34" || minorOf("x") != "" {
		t.Error("minorOf")
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k8s.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := Cluster{ID: "a", Spec: Spec{Name: "prod"}, PKI: PKI{CAKey: "secret"}}
	if err := s.Create(c); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(Cluster{ID: "b", Spec: Spec{Name: "prod"}}); !errors.Is(err, ErrExists) {
		t.Errorf("same name: %v", err)
	}
	for i := range maxEvents + 5 {
		s.Log("a", false, "line %d", i)
	}
	r, _ := Open(path)
	got, ok := r.Get("a")
	if !ok || len(got.Events) != maxEvents || got.PKI.CAKey != "secret" || got.Public().PKI.CAKey != "" {
		t.Errorf("reloaded %d events, key %q", len(got.Events), got.PKI.CAKey)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := s.Update("a", func(*Cluster) error { return errors.New("no") }); err == nil {
		t.Error("update error lost")
	}
	if err := s.Delete("a"); err != nil || s.Delete("a") == nil {
		t.Errorf("delete: %v", err)
	}
}

// fakeDriver plays the members and the nodes inside the machines.
type fakeDriver struct {
	mu       sync.Mutex
	vms      map[string]string // uuid → node name
	ips      map[string]string // node name → IP
	versions map[string]string // node name → kubelet version
	failNode string            // its setup fails
	noDNS    bool              // CoreDNS never starts
	created  []string
	deleted  []string
	reserved []string
	released []string
	rules    []string
	kubectl  []string
	steps    []string
	n        int
}

func newFake() *fakeDriver {
	return &fakeDriver{vms: map[string]string{}, ips: map[string]string{}, versions: map[string]string{}}
}

func (f *fakeDriver) CreateVM(_ context.Context, hostID string, r VMRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.UserData, "#cloud-config") {
		return "", errors.New("no user-data")
	}
	f.n++
	uuid := fmt.Sprintf("uuid-%d", f.n)
	f.vms[uuid] = r.Name
	f.ips[r.Name] = fmt.Sprintf("10.0.0.%d", 10+f.n)
	f.versions[r.Name] = "v1.36.1"
	f.created = append(f.created, r.Name+"@"+hostID)
	return uuid, nil
}

func (f *fakeDriver) DeleteVM(_ context.Context, _ string, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, ok := f.vms[uuid]
	if !ok {
		return ErrNotFound
	}
	delete(f.vms, uuid)
	delete(f.versions, name)
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeDriver) Exec(_ context.Context, _, uuid string, argv ...string) (ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, ok := f.vms[uuid]
	if !ok {
		return ExecResult{}, errors.New("no such machine")
	}
	cmd := strings.Join(argv, " ")
	switch {
	case argv[0] == "/bin/cat":
		if name == f.failNode {
			return ExecResult{Stdout: "failed at line 20: apt-get update\n"}, nil
		}
		return ExecResult{Stdout: "done\n"}, nil
	case argv[0] == "/usr/bin/tail":
		return ExecResult{Stdout: "E: no network\n"}, nil
	case argv[0] == "/usr/sbin/ip":
		return ExecResult{Stdout: "2: enp1s0    inet " + f.ips[name] + "/24 scope global enp1s0\n"}, nil
	case argv[0] == "/usr/bin/kubeadm":
		return ExecResult{Stdout: "abcdef.0123456789abcdef\n"}, nil
	case argv[0] == "/usr/bin/systemd-run":
		f.steps = append(f.steps, name+" "+strings.TrimPrefix(argv[1], "--unit="))
		if strings.HasPrefix(argv[1], "--unit=virtomir-k8s-kubelet-") {
			m := regexp.MustCompile(`/v([0-9.]+)/deb/#g`).FindStringSubmatch(argv[len(argv)-1])
			f.versions[name] = "v" + m[1] + ".0"
		}
		return ExecResult{}, nil
	case strings.Contains(cmd, "deployment coredns"):
		if f.noDNS {
			return ExecResult{Stdout: ""}, nil
		}
		return ExecResult{Stdout: "2"}, nil
	case strings.Contains(cmd, "get nodes -o json"):
		var items []string
		for n, v := range f.versions {
			items = append(items, fmt.Sprintf(`{"metadata":{"name":%q},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":%q}}}`, n, v))
		}
		return ExecResult{Stdout: `{"items":[` + strings.Join(items, ",") + `]}`}, nil
	case argv[0] == "/usr/bin/kubectl":
		f.kubectl = append(f.kubectl, strings.Join(argv[3:], " "))
		return ExecResult{}, nil
	}
	return ExecResult{ExitCode: 127, Stderr: "unknown " + cmd}, nil
}

func (f *fakeDriver) ReserveIP(_ context.Context, pool, name string) (string, error) {
	f.reserved = append(f.reserved, pool+" "+name)
	return "10.0.0.5", nil
}

func (f *fakeDriver) ReleaseIP(_ context.Context, pool, ip string) error {
	f.released = append(f.released, pool+" "+ip)
	return nil
}

func (f *fakeDriver) AntiAffinity(_ context.Context, name string, uuids []string) (string, error) {
	f.rules = append(f.rules, name+" "+strings.Join(uuids, ","))
	return "rule-1", nil
}

func (f *fakeDriver) DeleteRule(_ context.Context, id string) error {
	f.rules = append(f.rules, "deleted "+id)
	return nil
}

func newOrch(t *testing.T, f *fakeDriver) (*Orchestrator, *Store) {
	t.Helper()
	st, _ := Open("")
	o := New(st, func(Cluster) Driver { return f }, nil)
	o.Poll = time.Millisecond
	return o, st
}

func TestLifecycle(t *testing.T) {
	f := newFake()
	o, st := newOrch(t, f)
	c := testCluster(t, spec())
	c.Spec.Hosts = []string{"h1"}
	if err := st.Create(c); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := o.Provision(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := o.Scale(ctx, "c1", 1); !errors.Is(err, ErrBusy) {
		t.Errorf("scale while provisioning: %v", err)
	}
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateReady || c.Endpoint != "https://10.0.0.11:6443" || len(f.created) != 3 {
		t.Fatalf("after provisioning %s %q %s, created %v", c.State, c.Message, c.Endpoint, f.created)
	}
	for _, n := range c.Nodes {
		if n.State != NodeReady || n.IP == "" || n.Version != "v1.36.1" {
			t.Errorf("node %+v", n)
		}
	}

	// Down to one worker, then up to three.
	if err := o.Scale(ctx, "c1", 1); err != nil {
		t.Fatal(err)
	}
	o.Wait()
	c, _ = st.Get("c1")
	if len(c.Nodes) != 2 || strings.Join(f.deleted, ",") != "prod-worker-2" || !strings.Contains(strings.Join(f.kubectl, ";"), "drain prod-worker-2") {
		t.Fatalf("after scale down: %d nodes, deleted %v, kubectl %v", len(c.Nodes), f.deleted, f.kubectl)
	}
	if err := o.Scale(ctx, "c1", 3); err != nil {
		t.Fatal(err)
	}
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateReady || len(c.Nodes) != 4 || c.Spec.Workers != 3 || c.Nodes[3].Name != "prod-worker-3" {
		t.Fatalf("after scale up: %s %q %+v", c.State, c.Message, c.Nodes)
	}

	// Upgrade: control plane first with apply, then each worker.
	if err := o.Upgrade(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateReady || c.Version != "1.37" || c.UpgradeTo != "" {
		t.Fatalf("after upgrade: %s %q %s", c.State, c.Message, c.Version)
	}
	if f.steps[0] != "prod-cp-1 virtomir-k8s-kubeadm-1-37" || f.steps[1] != "prod-cp-1 virtomir-k8s-kubelet-1-37" || len(f.steps) != 8 {
		t.Errorf("steps %v", f.steps)
	}
	if err := o.Upgrade(ctx, "c1"); !errors.Is(err, ErrInvalid) {
		t.Errorf("past the newest: %v", err)
	}

	if err := o.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	o.Wait()
	if _, ok := st.Get("c1"); ok || len(f.vms) != 0 {
		t.Errorf("not deleted: %v", f.vms)
	}
}

func TestThreeControlPlanes(t *testing.T) {
	f := newFake()
	o, st := newOrch(t, f)
	s := spec()
	s.ControlPlanes, s.IPPool, s.Workers = 3, "lab", 0
	c := testCluster(t, s)
	c.Spec.Hosts = []string{"h1", "h2"}
	c.Nodes = Plan(c.Spec, c.Spec.Hosts)
	_ = st.Create(c)
	_ = o.Provision(context.Background(), "c1")
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateReady || c.ReservedVIP != "10.0.0.5" || c.Endpoint != "https://10.0.0.5:6443" || c.RuleID != "rule-1" {
		t.Fatalf("%s %q %+v", c.State, c.Message, c)
	}
	if strings.Join(f.created, ",") != "prod-cp-1@h1,prod-cp-2@h2,prod-cp-3@h1" || len(f.reserved) != 1 {
		t.Errorf("created %v reserved %v", f.created, f.reserved)
	}
	if !strings.Contains(strings.Join(f.kubectl, ";"), "taint nodes --all node-role.kubernetes.io/control-plane-") {
		t.Errorf("no workers, control planes not opened: %v", f.kubectl)
	}
	_ = o.Delete(context.Background(), "c1")
	o.Wait()
	if strings.Join(f.released, ",") != "lab 10.0.0.5" || f.rules[len(f.rules)-1] != "deleted rule-1" {
		t.Errorf("released %v rules %v", f.released, f.rules)
	}
}

func TestProvisionFailure(t *testing.T) {
	f := newFake()
	f.failNode = "prod-worker-2"
	o, st := newOrch(t, f)
	c := testCluster(t, spec())
	c.Spec.Hosts = []string{"h1"}
	_ = st.Create(c)
	_ = o.Provision(context.Background(), "c1")
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateFailed || !strings.Contains(c.Message, "prod-worker-2: failed at line 20") || !strings.Contains(c.Message, "E: no network") {
		t.Fatalf("%s %q", c.State, c.Message)
	}
	// Scaling again makes the failed worker anew.
	f.failNode = ""
	if err := o.Scale(context.Background(), "c1", 2); err != nil {
		t.Fatal(err)
	}
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateReady || len(c.Nodes) != 3 || strings.Join(f.deleted, ",") != "prod-worker-2" {
		t.Errorf("%s %q nodes %d deleted %v", c.State, c.Message, len(c.Nodes), f.deleted)
	}
}

func TestBrokenPodNetwork(t *testing.T) {
	f := newFake()
	f.noDNS = true
	o, st := newOrch(t, f)
	o.ReadyTimeout = 20 * time.Millisecond
	c := testCluster(t, spec())
	c.Spec.Hosts = []string{"h1"}
	_ = st.Create(c)
	_ = o.Provision(context.Background(), "c1")
	o.Wait()
	c, _ = st.Get("c1")
	if c.State != StateFailed || !strings.Contains(c.Message, "the pod network does not work") {
		t.Errorf("%s %q", c.State, c.Message)
	}
}

func TestRecover(t *testing.T) {
	f := newFake()
	o, st := newOrch(t, f)
	c := testCluster(t, spec())
	c.State = StateUpgrading
	_ = st.Create(c)
	o.Recover(context.Background())
	c, _ = st.Get("c1")
	if c.State != StateFailed || !strings.Contains(c.Message, "while the cluster was upgrading") {
		t.Errorf("%s %q", c.State, c.Message)
	}
}
