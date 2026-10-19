package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/sdn"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// ovnRecorder stands in for ovs-vsctl/ovn-nbctl: it records the commands and answers the
// few questions the SDN asks (a DHCP row's UUID, a port's dynamic address).
type ovnRecorder struct {
	mu    sync.Mutex
	calls []string
	fail  string // a command containing this fails
}

func (o *ovnRecorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	o.mu.Lock()
	o.calls = append(o.calls, line)
	o.mu.Unlock()
	switch {
	case o.fail != "" && strings.Contains(line, o.fail):
		return nil, fmt.Errorf("%s: refused", name)
	case strings.Contains(line, "create DHCP_Options"):
		return []byte("8a7b6c5d-0000-4000-8000-000000000001\n"), nil
	case strings.Contains(line, "dynamic_addresses"):
		return []byte(`"52:54:00:aa:bb:cc 10.0.1.5"` + "\n"), nil
	}
	return nil, nil
}

func (o *ovnRecorder) called(part string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.calls {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

func (f *fakeBackend) AttachInterface(uuid string, spec virt.NICSpec) error {
	f.call("attach-nic " + uuid + " " + spec.MAC + " " + spec.IfaceID)
	return f.err
}

func (f *fakeBackend) DetachDevice(uuid string, ref virt.DeviceRef) (virt.DeviceResult, error) {
	f.call("detach " + ref.Kind + " " + ref.MAC)
	return virt.DeviceResult{}, f.err
}

func sdnNodes(t *testing.T) (*testNode, *testNode, *ovnRecorder) {
	t.Helper()
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	rec := &ovnRecorder{}
	for _, n := range []*testNode{a, b} {
		st, _ := sdn.Open("")
		n.api.sdn, n.api.sdnRunner = st, rec
		if _, err := n.projects.Create(project.Project{ID: "web", Name: "Web", Members: []project.Member{{Kind: "user", Name: "op", Role: "operator"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := n.projects.Create(project.Project{ID: "data", Name: "Data"}); err != nil {
			t.Fatal(err)
		}
	}
	joinNodes(t, a, b)
	return a, b, rec
}

func TestSDNVPCLifecycle(t *testing.T) {
	a, b, ovn := sdnNodes(t)
	op, viewer := a.signIn(t, "op"), a.signIn(t, "view")

	if resp, _ := viewer.req("POST", "/api/v1/sdn/vpcs", `{"id":"prod","name":"Prod","projectId":"web","ipv4Cidr":"10.0.0.0/16"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer created a VPC: %d", resp.StatusCode)
	}
	if resp, _ := op.req("POST", "/api/v1/sdn/vpcs", `{"id":"etl","name":"ETL","projectId":"data","ipv4Cidr":"10.1.0.0/16"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator of web created a VPC in data: %d", resp.StatusCode)
	}
	resp, body := op.req("POST", "/api/v1/sdn/vpcs", `{"id":"prod","name":"Prod","projectId":"web","ipv4Cidr":"10.0.0.0/16"}`)
	if resp.StatusCode != http.StatusCreated || !ovn.called("ovn-nbctl --may-exist lr-add lr-prod") {
		t.Fatalf("create: %d %s %v", resp.StatusCode, body, ovn.calls)
	}
	resp, body = op.req("POST", "/api/v1/sdn/subnets", `{"id":"prod-web","vpcId":"prod","name":"web","cidr":"10.0.1.0/24","dhcpEnabled":true}`)
	var sub sdn.Subnet
	if resp.StatusCode != http.StatusCreated || json.Unmarshal([]byte(body), &sub) != nil || sub.OVNDHCPID == "" || sub.GatewayIP != "10.0.1.1" {
		t.Fatalf("subnet: %d %s", resp.StatusCode, body)
	}
	// The other member has it all, without touching OVN itself.
	if _, ok := b.api.sdn.GetSubnet("prod-web"); !ok {
		t.Error("beta did not get the subnet")
	}
	if resp, _ := op.req("PUT", "/api/v1/sdn/vpcs/prod/routes", `{"routes":[{"destinationCidr":"192.168.0.0/16","nextHopIp":"10.0.1.254"}]}`); resp.StatusCode != http.StatusOK ||
		!ovn.called("lr-route-add lr-prod 192.168.0.0/16 10.0.1.254") {
		t.Errorf("routes: %d", resp.StatusCode)
	}
	// Gateways and floating IPs take an admin.
	gw := `{"enabled":true,"externalNetwork":"physnet1","externalIp":"203.0.113.10/24","gatewayIp":"203.0.113.1","gatewayChassis":"host-a"}`
	if resp, _ := op.req("PUT", "/api/v1/sdn/vpcs/prod/gateway", gw); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator set the gateway: %d", resp.StatusCode)
	}
	admin := a.signIn(t, "onur")
	if resp, body := admin.req("PUT", "/api/v1/sdn/vpcs/prod/gateway", gw); resp.StatusCode != http.StatusOK || !ovn.called("lr-nat-add lr-prod snat 203.0.113.10 10.0.1.0/24") {
		t.Errorf("gateway: %d %s", resp.StatusCode, body)
	}
	// The data project's members do not see prod at all.
	_, body = viewer.req("GET", "/api/v1/sdn/vpcs", "")
	if strings.Contains(body, "prod") {
		t.Errorf("a non-member sees the VPC: %s", body)
	}
	if resp, _ := viewer.req("GET", "/api/v1/sdn/vpcs/prod", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a non-member reads the VPC: %d", resp.StatusCode)
	}
	_, body = op.req("GET", "/api/v1/sdn/vpcs/prod", "")
	var d VPCDetail
	if json.Unmarshal([]byte(body), &d) != nil || d.Gateway == nil || len(d.Router.Routes) != 1 {
		t.Errorf("detail %s", body)
	}
	// Busy VPCs stay; empty ones go, with their router.
	if resp, _ := op.req("DELETE", "/api/v1/sdn/vpcs/prod", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("deleted a VPC with a subnet: %d", resp.StatusCode)
	}
	if resp, _ := op.req("DELETE", "/api/v1/sdn/subnets/prod-web", ""); resp.StatusCode != http.StatusNoContent || !ovn.called("ls-del ls-prod-web") {
		t.Errorf("subnet delete: %d", resp.StatusCode)
	}
	if resp, _ := op.req("DELETE", "/api/v1/sdn/vpcs/prod", ""); resp.StatusCode != http.StatusNoContent || !ovn.called("lr-del lr-prod") {
		t.Errorf("VPC delete: %d", resp.StatusCode)
	}
	if _, ok := b.api.sdn.GetVPC("prod"); ok {
		t.Error("beta kept the VPC")
	}
}

func TestSDNKeepsStateWhenOVNRefuses(t *testing.T) {
	a, _, ovn := sdnNodes(t)
	ovn.fail = "lr-add"
	admin := a.signIn(t, "onur")
	resp, body := admin.req("POST", "/api/v1/sdn/vpcs", `{"id":"prod","name":"Prod","projectId":"web","ipv4Cidr":"10.0.0.0/16"}`)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "saved") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, ok := a.api.sdn.GetVPC("prod"); !ok {
		t.Fatal("the VPC was not kept")
	}
	ovn.fail = ""
	resp, body = admin.req("POST", "/api/v1/sdn/reconcile", "")
	var res ReconcileResult
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil || res.Applied != 2 || len(res.Errors) != 0 {
		t.Errorf("reconcile: %d %s", resp.StatusCode, body)
	}
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/sdn/reconcile", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an operator reconciled: %d", resp.StatusCode)
	}
}

func TestSDNMachineAdapters(t *testing.T) {
	a, b, ovn := sdnNodes(t)
	a.backend.domains = []virt.Domain{{UUID: "u-new", Name: "web1", Project: "web"}}
	admin := a.signIn(t, "onur")
	for _, req := range [][2]string{
		{"/api/v1/sdn/vpcs", `{"id":"prod","name":"Prod","projectId":"web","ipv4Cidr":"10.0.0.0/16"}`},
		{"/api/v1/sdn/subnets", `{"id":"prod-web","vpcId":"prod","name":"web","cidr":"10.0.1.0/24","dhcpEnabled":true}`},
	} {
		if resp, body := admin.req("POST", req[0], req[1]); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d %s", req[0], resp.StatusCode, body)
		}
	}
	resp, body := admin.req("POST", "/api/v1/domains", `{"name":"web1","project":"web","network":{"ovnSubnet":"prod-web","mac":"52:54:00:aa:bb:cc"},"start":true}`)
	if resp.StatusCode != http.StatusCreated || strings.Contains(body, "startError") {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	port := sdn.PortID("u-new", "52:54:00:aa:bb:cc")
	if p, ok := a.api.sdn.GetPort(port); !ok || p.IP != "10.0.1.5" {
		t.Fatalf("port %+v", p)
	}
	if _, ok := b.api.sdn.GetPort(port); !ok {
		t.Error("beta does not know the port")
	}
	calls := strings.Join(a.backend.calls, "\n")
	if !strings.Contains(calls, "attach-nic u-new 52:54:00:aa:bb:cc "+port) || a.backend.lastAction != virt.ActionStart {
		t.Errorf("backend %s / %s", calls, a.backend.lastAction)
	}
	if !ovn.called("lsp-set-port-security " + port + " 52:54:00:aa:bb:cc 10.0.1.5") {
		t.Errorf("ovn %v", ovn.calls)
	}
	// Taking the adapter off frees the port; deleting the machine frees the rest.
	if resp, _ := admin.req("POST", "/api/v1/domains/u-new/devices/detach", `{"kind":"interface","mac":"52:54:00:AA:BB:CC"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("detach %d", resp.StatusCode)
	}
	if _, ok := a.api.sdn.GetPort(port); ok || !ovn.called("lsp-del "+port) {
		t.Error("the port stayed")
	}
	// A machine of another project may not plug into web's subnet.
	a.backend.domains[0].Project = "data"
	if resp, _ := a.signIn(t, "op").req("POST", "/api/v1/domains/u-new/interfaces", `{"ovnSubnet":"prod-web"}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-project plug: %d", resp.StatusCode)
	}
}

func TestSDNIPsecIsRedactedAndAdminOnly(t *testing.T) {
	a, _, _ := sdnNodes(t)
	if resp, _ := a.signIn(t, "op").req("PUT", "/api/v1/sdn/ipsec", `{"enabled":false}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator: %d", resp.StatusCode)
	}
	admin := a.signIn(t, "onur")
	if resp, body := admin.req("PUT", "/api/v1/sdn/ipsec", `{"enabled":true,"mode":"psk","psk":"s3cret"}`); resp.StatusCode != http.StatusUnprocessableEntity || strings.Contains(body, "s3cret") {
		t.Errorf("psk: %d %s", resp.StatusCode, body)
	}
	resp, body := admin.req("PUT", "/api/v1/sdn/ipsec", `{"enabled":true,"certFile":"/etc/ipsec.d/certs/host.pem","keyFile":"/etc/ipsec.d/private/host.key","caCertFile":"/etc/ipsec.d/cacerts/ca.pem"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"active"`) {
		t.Errorf("cert: %d %s", resp.StatusCode, body)
	}
}

func TestSDNAnswersFollowOpenAPI(t *testing.T) {
	a, _, _ := sdnNodes(t)
	admin := a.signIn(t, "onur")
	for _, req := range [][2]string{
		{"/api/v1/sdn/vpcs", `{"id":"prod","name":"Prod","projectId":"web","ipv4Cidr":"10.0.0.0/16"}`},
		{"/api/v1/sdn/subnets", `{"id":"prod-web","vpcId":"prod","name":"web","cidr":"10.0.1.0/24","dhcpEnabled":true,"dnsServers":["1.1.1.1"]}`},
		{"/api/v1/sdn/floating-ips", `{"vpcId":"prod","externalIp":"203.0.113.20"}`},
		{"/api/v1/sdn/security-groups", `{"vpcId":"prod","name":"web"}`},
		{"/api/v1/sdn/load-balancers", `{"vpcId":"prod","name":"front","vip":"10.0.1.100","port":80,"members":["10.0.1.10:8080"]}`},
	} {
		if resp, body := admin.req("POST", req[0], req[1]); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d %s", req[0], resp.StatusCode, body)
		}
	}
	doc := loadOpenAPI(t)
	c := contractChecker{schemas: doc.Components.Schemas}
	for _, path := range []string{"/api/v1/sdn/vpcs", "/api/v1/sdn/vpcs/prod", "/api/v1/sdn/subnets", "/api/v1/sdn/ports",
		"/api/v1/sdn/floating-ips", "/api/v1/sdn/security-groups", "/api/v1/sdn/load-balancers", "/api/v1/sdn/ipsec"} {
		resp, body := admin.req("GET", path, "")
		rec := httptest.NewRecorder()
		rec.Code = resp.StatusCode
		rec.HeaderMap = resp.Header
		rec.Body.WriteString(body)
		c.checkAnswer(t, "GET "+path, doc.Paths[strings.Replace(path, "/prod", "/{id}", 1)]["get"], rec, false)
	}
}
