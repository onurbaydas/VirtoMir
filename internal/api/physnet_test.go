package api

import (
	"encoding/binary"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/onurbaydas/virtomir/internal/physnet"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// dnsAcker answers every DNS UPDATE with NOERROR and counts them.
func dnsAcker(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var n atomic.Int32
	go func() {
		buf := make([]byte, 4096)
		for {
			k, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			n.Add(1)
			ans := append([]byte{}, buf[:12]...)
			binary.BigEndian.PutUint16(ans[2:], 0x8000|5<<11)
			_ = k
			_, _ = conn.WriteTo(ans, from)
		}
	}()
	return conn.LocalAddr().String(), &n
}

func physNodes(t *testing.T) (*testNode, *testNode, *cephRecorder) {
	t.Helper()
	a, b := newTestNode(t, "alpha", nil), newTestNode(t, "beta", nil)
	rec := &cephRecorder{answers: map[string]string{}}
	for _, n := range []*testNode{a, b} {
		st, _ := physnet.Open(filepath.Join(t.TempDir(), "physnet.json"))
		root := t.TempDir()
		n.api.physnet, n.api.physRunner, n.api.sriov = st, rec, physnet.SRIOV{Root: root, RulesDir: filepath.Join(root, "rules")}
	}
	joinNodes(t, a, b)
	return a, b, rec
}

const poolJSON = `{"id":"prod-v20","name":"Prod VLAN 20","network":"default","cidr":"10.20.0.0/24","gateway":"10.20.0.1","rangeStart":"10.20.0.10","rangeEnd":"10.20.0.20","dnsServers":["10.20.0.53"],"domain":"prod.example"}`

func TestIPPoolsAcrossTheCluster(t *testing.T) {
	a, b, _ := physNodes(t)
	admin := a.signIn(t, "onur")
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/physnet/ip-pools", poolJSON); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator made a pool: %d", resp.StatusCode)
	}
	if resp, body := admin.req("POST", "/api/v1/physnet/ip-pools", poolJSON); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d %s", resp.StatusCode, body)
	}
	if _, ok := b.api.physnet.Pool("prod-v20"); !ok {
		t.Fatal("beta has no pool")
	}
	if resp, body := admin.req("POST", "/api/v1/physnet/ip-pools/prod-v20/allocations", `{"ip":"10.20.0.15","name":"printer"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("reserve %d %s", resp.StatusCode, body)
	}
	_, body := admin.req("POST", "/api/v1/physnet/ip-pools/prod-v20/check", `{"ip":"10.20.0.15"}`)
	if !strings.Contains(body, `"free":false`) || !strings.Contains(body, "printer") {
		t.Errorf("check %s", body)
	}
	if p, _ := b.api.physnet.Pool("prod-v20"); len(p.Allocations) != 1 {
		t.Errorf("beta's copy has %d allocations", len(p.Allocations))
	}
	if resp, _ := admin.req("DELETE", "/api/v1/physnet/ip-pools/prod-v20", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("delete with allocations: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/physnet/ip-pools/prod-v20/allocations/10.20.0.15", ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("release: %d", resp.StatusCode)
	}
	if resp, _ := admin.req("POST", "/api/v1/physnet/ip-pools", strings.Replace(poolJSON, "prod-v20", "dup", 1)); resp.StatusCode != http.StatusConflict {
		t.Errorf("overlapping pool: %d", resp.StatusCode)
	}
	a.backend.netList = []virt.VNetwork{{Name: "lan", Mode: "nat", DHCPStart: "10.40.0.2", DHCPEnd: "10.40.0.254"}}
	clash := `{"id":"lan","name":"LAN","network":"lan","cidr":"10.40.0.0/24","rangeStart":"10.40.0.200","rangeEnd":"10.40.0.220"}`
	if resp, body := admin.req("POST", "/api/v1/physnet/ip-pools", clash); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "DHCP range") {
		t.Errorf("inside the DHCP range: %d %s", resp.StatusCode, body)
	}
}

func TestMachineGetsAFixedAddressAndName(t *testing.T) {
	a, _, _ := physNodes(t)
	admin := a.signIn(t, "onur")
	dnsAddr, updates := dnsAcker(t)
	if resp, body := admin.req("POST", "/api/v1/physnet/ip-pools", poolJSON); resp.StatusCode != http.StatusCreated {
		t.Fatalf("pool %d %s", resp.StatusCode, body)
	}
	if resp, body := admin.req("PUT", "/api/v1/physnet/dns", `{"enabled":true,"server":"`+dnsAddr+`","zone":"prod.example","reverseZones":["20.10.in-addr.arpa"]}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dns %d %s", resp.StatusCode, body)
	}
	create := `{"name":"web-01","vcpus":1,"memoryMiB":1024,"pool":"default","diskGiB":10,"baseImage":"/img/debian.qcow2","network":{"network":"default"},` +
		`"cloudInit":{"user":"ops","password":"pw"},"ip":{"pool":"prod-v20"}}`
	resp, body := admin.req("POST", "/api/v1/domains?wait=true", create)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d %s", resp.StatusCode, body)
	}
	spec := a.backend.lastCreate
	if spec.CloudInit.Network == nil || spec.CloudInit.Network.Addresses[0] != "10.20.0.10/24" || spec.CloudInit.Network.Gateway4 != "10.20.0.1" ||
		spec.Network.MAC == "" || spec.CloudInit.Network.MAC != spec.Network.MAC || spec.CloudInit.Network.Search[0] != "prod.example" {
		t.Errorf("cloud-init %+v nic %+v", spec.CloudInit.Network, spec.Network)
	}
	p, _ := a.api.physnet.Pool("prod-v20")
	if len(p.Allocations) != 1 || p.Allocations[0].VMUUID != "u-new" || p.Allocations[0].Hostname != "web-01" {
		t.Errorf("allocation %+v", p.Allocations)
	}
	if n := updates.Load(); n != 2 {
		t.Errorf("DNS updates on create: %d (forward and reverse)", n)
	}
	if resp, _ := admin.req("DELETE", "/api/v1/domains/u-new", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	if p, _ := a.api.physnet.Pool("prod-v20"); len(p.Allocations) != 0 {
		t.Errorf("the address stayed: %+v", p.Allocations)
	}
	if n := updates.Load(); n != 4 {
		t.Errorf("DNS updates after delete: %d", n)
	}
	noCI := `{"name":"db","vcpus":1,"memoryMiB":1024,"diskGiB":10,"network":{"network":"default"},"ip":{"pool":"prod-v20"}}`
	if resp, body := admin.req("POST", "/api/v1/domains?wait=true", noCI); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "cloud-init") {
		t.Errorf("without cloud-init: %d %s", resp.StatusCode, body)
	}
	_, body = admin.req("GET", "/api/v1/physnet/dns", "")
	if !strings.Contains(body, `"zone":"prod.example."`) || strings.Contains(body, `"secret":"`) && !strings.Contains(body, `"secret":""`) {
		t.Errorf("dns view %s", body)
	}
	_, body = admin.req("POST", "/api/v1/physnet/dns/test", "")
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("dns test %s", body)
	}
}

func TestBGPKeepsPasswords(t *testing.T) {
	a, _, rec := physNodes(t)
	old := physnet.FRRFile
	physnet.FRRFile = filepath.Join(t.TempDir(), "bgp.conf")
	defer func() { physnet.FRRFile = old }()
	rec.answers["vtysh -c show version"] = "FRRouting 10.2.1 (dev).\n"
	rec.answers["vtysh -c show daemons"] = " zebra bgpd staticd\n"
	rec.answers["vtysh -c show bgp summary json"] = `{"ipv4Unicast":{"peers":{"10.0.0.1":{"remoteAs":65000,"state":"Established","pfxRcd":3,"pfxSnt":1}}}}`
	admin := a.signIn(t, "onur")
	cfg := `{"enabled":true,"localAs":65011,"routerId":"10.0.0.11","neighbors":[{"address":"10.0.0.1","remoteAs":65000,"password":"s3cret"}],"announce":["10.20.0.0/24"],"evpn":false}`
	if resp, body := admin.req("PUT", "/api/v1/physnet/bgp", cfg); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put %d %s", resp.StatusCode, body)
	}
	_, body := admin.req("GET", "/api/v1/physnet/bgp", "")
	if strings.Contains(body, "s3cret") || !strings.Contains(body, `"state":"Established"`) || !strings.Contains(body, `"appliedAs":65011`) {
		t.Errorf("view %s", body)
	}
	if resp, _ := admin.req("PUT", "/api/v1/physnet/bgp", strings.Replace(cfg, "s3cret", "********", 1)); resp.StatusCode != http.StatusNoContent {
		t.Fatal("second put")
	}
	if got := a.api.physnet.BGP().Neighbors[0].Password; got != "s3cret" {
		t.Errorf("password %q", got)
	}
	if !rec.called("vtysh -c configure terminal -c no router bgp 65011") {
		t.Error("the old section was not replaced")
	}
	if resp, _ := admin.req("PUT", "/api/v1/physnet/bgp", `{"enabled":true,"localAs":0,"routerId":"x","neighbors":[]}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid: %d", resp.StatusCode)
	}
}

func TestSRIOVEndpoints(t *testing.T) {
	a, _, _ := physNodes(t)
	admin := a.signIn(t, "onur")
	_, body := a.signIn(t, "view").req("GET", "/api/v1/physnet/sriov", "")
	if body != `{"devices":[]}`+"\n" && body != `{"devices":[]}` {
		t.Errorf("no SR-IOV NICs: %q", body)
	}
	if resp, _ := admin.req("PUT", "/api/v1/physnet/sriov/ens1f0", `{"vfs":4,"persistent":true}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown NIC: %d", resp.StatusCode)
	}
	if resp, _ := a.signIn(t, "op").req("PUT", "/api/v1/physnet/sriov/ens1f0", `{"vfs":4}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator changed VFs: %d", resp.StatusCode)
	}
}
