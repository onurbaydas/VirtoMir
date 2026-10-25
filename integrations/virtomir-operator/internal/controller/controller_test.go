package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/onurbaydas/virtomir/integrations/virtomir-operator/api/v1alpha1"
	"github.com/onurbaydas/virtomir/integrations/virtomir-operator/internal/virtomir"
)

type vmRec struct {
	Name, State string
	Template    bool
}

// fakeVirtoMir holds machines, one template ("tpl", uuid t0), jobs and projects.
type fakeVirtoMir struct {
	mu       sync.Mutex
	vms      map[string]*vmRec
	projects map[string]virtomir.Project
	jobs     map[string]string // id → result uuid
	created  []map[string]any
	calls    []string
	n        int
}

func newFake() *fakeVirtoMir {
	return &fakeVirtoMir{vms: map[string]*vmRec{"t0": {Name: "tpl", State: "shutoff", Template: true}}, projects: map[string]virtomir.Project{}, jobs: map[string]string{}}
}

func (f *fakeVirtoMir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, op)
	reply := func(code int, v any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	switch {
	case op == "GET /api/v1/domains":
		var out []map[string]any
		for id, v := range f.vms {
			out = append(out, map[string]any{"uuid": id, "name": v.Name, "state": v.State, "template": v.Template})
		}
		reply(200, map[string]any{"domains": out})
	case op == "POST /api/v1/domains":
		f.n++
		id := "u" + string(rune('0'+f.n))
		state := "shutoff"
		if body["start"] == true {
			state = "running"
		}
		f.vms[id] = &vmRec{Name: body["name"].(string), State: state}
		f.created = append(f.created, body)
		reply(201, map[string]string{"uuid": id})
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "domains" && parts[2] == "deploy":
		f.n++
		id := "d" + string(rune('0'+f.n))
		f.vms[id] = &vmRec{Name: body["name"].(string), State: "running"}
		f.jobs["j1"] = id
		reply(202, map[string]string{"id": "j1"})
	case op == "GET /api/v1/jobs/j1":
		reply(200, map[string]string{"state": "succeeded", "result": f.jobs["j1"]})
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "domains":
		v, ok := f.vms[parts[1]]
		if !ok {
			reply(404, map[string]string{"error": "no such machine"})
			return
		}
		reply(200, map[string]any{"uuid": parts[1], "state": v.State, "devices": []map[string]any{{"kind": "interface", "addresses": []string{"10.0.0.7/24"}}}})
	case r.Method == "POST" && len(parts) == 4 && parts[2] == "actions":
		v := f.vms[parts[1]]
		switch parts[3] {
		case "start":
			v.State = "running"
		case "shutdown", "force-off":
			v.State = "shutoff"
		}
		w.WriteHeader(204)
	case r.Method == "DELETE" && len(parts) == 2 && parts[0] == "domains":
		delete(f.vms, parts[1])
		w.WriteHeader(200)
	case op == "POST /api/v1/projects":
		var p virtomir.Project
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &p)
		f.projects[p.ID] = p
		reply(201, p)
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "projects":
		p, ok := f.projects[parts[1]]
		if !ok {
			reply(404, map[string]string{"error": "no such project"})
			return
		}
		reply(200, p)
	case r.Method == "PUT" && len(parts) == 2 && parts[0] == "projects":
		var p virtomir.Project
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &p)
		f.projects[parts[1]] = p
		reply(200, p)
	case r.Method == "DELETE" && len(parts) == 2 && parts[0] == "projects":
		delete(f.projects, parts[1])
		w.WriteHeader(204)
	default:
		reply(404, map[string]string{"error": "no such endpoint " + op})
	}
}

func setup(t *testing.T, objs ...client.Object) (client.Client, *fakeVirtoMir, *virtomir.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&v1.VirtoMirMachine{}, &v1.VirtoMirProject{}).Build()
	f := newFake()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	vm, err := virtomir.New(srv.URL, "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	return c, f, vm
}

var key = types.NamespacedName{Namespace: "default", Name: "web"}

func reconcileMachine(t *testing.T, r *MachineReconciler, times int) v1.VirtoMirMachine {
	t.Helper()
	for range times {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	var m v1.VirtoMirMachine
	_ = r.Get(context.Background(), key, &m)
	return m
}

func TestMachineLifecycle(t *testing.T) {
	obj := &v1.VirtoMirMachine{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: v1.MachineSpec{BaseImage: "/img/debian.qcow2", StoragePool: "images", CloudInit: &v1.CloudInit{SSHKeys: []string{"ssh-ed25519 AAAA"}}}}
	c, f, vm := setup(t, obj)
	r := &MachineReconciler{Client: c, VirtoMir: vm, Every: time.Minute}
	m := reconcileMachine(t, r, 1)
	if m.Status.UUID != "u1" || m.Status.State != "running" || len(m.Status.Addresses) != 1 || !meta.IsStatusConditionTrue(m.Status.Conditions, "Ready") {
		t.Fatalf("status %+v", m.Status)
	}
	if len(m.Finalizers) != 1 {
		t.Errorf("finalizers %v", m.Finalizers)
	}
	body := f.created[0]
	if body["name"] != "web" || body["pool"] != "images" || body["start"] != true || body["cloudInit"].(map[string]any)["user"] != "kube" {
		t.Errorf("created %v", body)
	}
	// Again: nothing new is created.
	reconcileMachine(t, r, 2)
	if len(f.created) != 1 {
		t.Errorf("created %d machines", len(f.created))
	}

	// Stopped in Kubernetes: shut down in VirtoMir.
	_ = c.Get(context.Background(), key, &m)
	m.Spec.PowerState = v1.PowerStopped
	if err := c.Update(context.Background(), &m); err != nil {
		t.Fatal(err)
	}
	m = reconcileMachine(t, r, 2)
	if f.vms["u1"].State != "shutoff" || m.Status.State != "shutoff" {
		t.Errorf("not stopped: %s %s", f.vms["u1"].State, m.Status.State)
	}

	// Deleted outside Kubernetes: made again.
	delete(f.vms, "u1")
	m = reconcileMachine(t, r, 2)
	if m.Status.UUID != "u2" || len(f.created) != 2 {
		t.Errorf("not made again: %+v", m.Status)
	}

	// Deleting the object deletes the machine.
	_ = c.Delete(context.Background(), &m)
	reconcileMachine(t, r, 1)
	if _, ok := f.vms["u2"]; ok {
		t.Error("machine left behind")
	}
	var gone v1.VirtoMirMachine
	if err := c.Get(context.Background(), key, &gone); err == nil {
		t.Errorf("object still there: %v", gone.Finalizers)
	}
}

func TestMachineFromTemplateAndRetain(t *testing.T) {
	obj := &v1.VirtoMirMachine{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: v1.MachineSpec{Template: "tpl", Linked: true, DeletionPolicy: v1.DeletionRetain}}
	c, f, vm := setup(t, obj)
	r := &MachineReconciler{Client: c, VirtoMir: vm, Every: time.Minute}
	m := reconcileMachine(t, r, 1)
	if m.Status.Job != "j1" || m.Status.UUID != "" {
		t.Fatalf("after deploy %+v", m.Status)
	}
	m = reconcileMachine(t, r, 1)
	if m.Status.UUID != "d1" || m.Status.Job != "" || m.Status.State != "running" {
		t.Fatalf("after the job %+v", m.Status)
	}
	_ = c.Delete(context.Background(), &m)
	reconcileMachine(t, r, 1)
	if _, ok := f.vms["d1"]; !ok {
		t.Error("retained machine deleted")
	}
}

func TestMachineAdoptsAndReportsErrors(t *testing.T) {
	obj := &v1.VirtoMirMachine{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}, Spec: v1.MachineSpec{}}
	c, f, vm := setup(t, obj)
	r := &MachineReconciler{Client: c, VirtoMir: vm, Every: time.Minute}
	m := reconcileMachine(t, r, 1)
	if !strings.Contains(m.Status.Message, "spec.template or spec.baseImage") || meta.IsStatusConditionTrue(m.Status.Conditions, "Ready") {
		t.Errorf("no error reported: %+v", m.Status)
	}
	f.vms["x9"] = &vmRec{Name: "web", State: "running"}
	m = reconcileMachine(t, r, 1)
	if m.Status.UUID != "x9" || len(f.created) != 0 {
		t.Errorf("not adopted: %+v", m.Status)
	}
}

func TestProject(t *testing.T) {
	obj := &v1.VirtoMirProject{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Namespace: "default"},
		Spec: v1.ProjectSpec{DisplayName: "Team A", StoragePools: []string{"images"}, Limits: v1.ProjectLimits{Machines: 5},
			Members: []v1.ProjectMember{{Kind: "user", Name: "ayse", Role: "operator"}}}}
	c, f, vm := setup(t, obj)
	r := &ProjectReconciler{Client: c, VirtoMir: vm, Every: time.Minute}
	pk := types.NamespacedName{Namespace: "default", Name: "team-a"}
	run := func() v1.VirtoMirProject {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pk}); err != nil {
			t.Fatal(err)
		}
		var p v1.VirtoMirProject
		_ = c.Get(context.Background(), pk, &p)
		return p
	}
	p := run()
	if got := f.projects["team-a"]; got.Name != "Team A" || got.Limits.Machines != 5 || len(got.Members) != 1 || p.Status.ID != "team-a" {
		t.Fatalf("project %+v status %+v", got, p.Status)
	}
	f.calls = nil
	p = run()
	if strings.Contains(strings.Join(f.calls, ","), "PUT") {
		t.Errorf("unchanged project updated: %v", f.calls)
	}
	p.Spec.Limits.Machines = 9
	if err := c.Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	p = run()
	if f.projects["team-a"].Limits.Machines != 9 {
		t.Errorf("limit not updated: %+v", f.projects["team-a"])
	}
	_ = c.Delete(context.Background(), &p)
	run()
	if _, ok := f.projects["team-a"]; ok {
		t.Error("project left behind")
	}
}
