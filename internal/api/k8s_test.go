package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/k8s"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// GuestExec plays a Kubernetes node whose setup has finished.
func (f *fakeBackend) GuestExec(_ context.Context, uuid string, argv []string) (virt.GuestExecResult, error) {
	f.call("exec " + uuid + " " + strings.Join(argv, " "))
	switch {
	case argv[0] == "/bin/cat":
		return virt.GuestExecResult{Stdout: "done\n"}, nil
	case argv[0] == "/usr/sbin/ip":
		return virt.GuestExecResult{Stdout: "2: enp1s0    inet 10.1.2.3/24 scope global enp1s0\n"}, nil
	case slices.Contains(argv, "deployment"):
		return virt.GuestExecResult{Stdout: "2"}, nil
	case slices.Contains(argv, "get"):
		return virt.GuestExecResult{Stdout: `{"items":[{"metadata":{"name":"lab-cp-1"},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.36.2"}}}]}`}, nil
	}
	return virt.GuestExecResult{}, nil
}

func k8sNode(t *testing.T) *testNode {
	t.Helper()
	n := newTestNode(t, "alpha", nil)
	st, _ := k8s.Open("")
	n.api.k8s = st
	n.api.k8sOrch = k8s.New(st, n.api.k8sDriverFor, n.api.log)
	n.api.k8sOrch.Poll = time.Millisecond
	return n
}

func TestK8sClusterAPI(t *testing.T) {
	n := k8sNode(t)
	admin, view := n.signIn(t, "onur"), n.signIn(t, "view")
	body := `{"name":"lab","image":"/img/debian-13.qcow2","network":"default","workers":0}`
	if resp, _ := view.req("POST", "/api/v1/k8s/clusters", body); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer creates: %d", resp.StatusCode)
	}
	if resp, b := admin.req("POST", "/api/v1/k8s/clusters", `{"name":"Lab!","image":"x","network":"default"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad name: %d %s", resp.StatusCode, b)
	}
	resp, b := admin.req("POST", "/api/v1/k8s/clusters", body)
	if resp.StatusCode != http.StatusAccepted || strings.Contains(b, "BEGIN RSA") || strings.Contains(b, "CAKey") {
		t.Fatalf("create: %d %s", resp.StatusCode, b)
	}
	var c k8s.Cluster
	_ = json.Unmarshal([]byte(b), &c)
	n.api.k8sOrch.Wait()

	_, b = view.req("GET", "/api/v1/k8s/clusters/"+c.ID, "")
	_ = json.Unmarshal([]byte(b), &c)
	if c.State != k8s.StateReady || c.Endpoint != "https://10.1.2.3:6443" || c.Nodes[0].Version != "v1.36.2" {
		t.Fatalf("after provisioning: %s", b)
	}
	spec := n.backend.lastCreate
	if spec.Name != "lab-cp-1" || spec.BaseImage != "/img/debian-13.qcow2" || spec.Network == nil || spec.Network.Network != "default" ||
		spec.CloudInit == nil || !strings.HasPrefix(spec.CloudInit.UserData, "#cloud-config") || !spec.Start {
		t.Errorf("machine %+v", spec)
	}
	if !slices.ContainsFunc(n.backend.calls, func(c string) bool { return strings.Contains(c, "taint nodes --all") }) {
		t.Errorf("control plane not opened for workloads: %v", n.backend.calls)
	}

	if resp, _ := view.req("GET", "/api/v1/k8s/clusters/"+c.ID+"/kubeconfig", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer kubeconfig: %d", resp.StatusCode)
	}
	resp, b = admin.req("GET", "/api/v1/k8s/clusters/"+c.ID+"/kubeconfig", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, "server: https://10.1.2.3:6443") || !strings.Contains(b, "name: virtomir-onur") {
		t.Errorf("kubeconfig: %d %s", resp.StatusCode, b)
	}
	if resp, _ := admin.req("POST", "/api/v1/k8s/clusters/"+c.ID+"/scale", `{"workers":-1}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("negative workers: %d", resp.StatusCode)
	}

	if resp, _ := view.req("POST", "/api/v1/domains/u-new/guest/exec", `{"argv":["/bin/true"]}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer exec: %d", resp.StatusCode)
	}
	if resp, b := admin.req("POST", "/api/v1/domains/u-new/guest/exec", `{"argv":["/bin/cat","/x"]}`); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"stdout":"done\n"`) {
		t.Errorf("exec: %d %s", resp.StatusCode, b)
	}

	if resp, _ := admin.req("DELETE", "/api/v1/k8s/clusters/"+c.ID, ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	n.api.k8sOrch.Wait()
	_, b = view.req("GET", "/api/v1/k8s/clusters", "")
	if !strings.Contains(b, `"clusters":[]`) || !slices.Contains(n.backend.calls, "delete+storage u-new") {
		t.Errorf("after delete: %s %v", b, n.backend.calls)
	}
}

func TestK8sHosts(t *testing.T) {
	a, b := newTestNode(t, "alpha", nil), newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	got, err := a.api.k8sHosts([]string{"beta", "alpha", "beta"})
	if err != nil || len(got) != 2 || got[0] != b.store.SelfID() || got[1] != a.store.SelfID() {
		t.Errorf("hosts %v %v", got, err)
	}
	if _, err := a.api.k8sHosts([]string{"gamma"}); err == nil {
		t.Error("unknown member accepted")
	}
	if got, _ := a.api.k8sHosts(nil); len(got) != 1 || got[0] != a.store.SelfID() {
		t.Errorf("default %v", got)
	}
}
