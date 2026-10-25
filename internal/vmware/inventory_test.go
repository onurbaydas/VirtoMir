// Tests against the official govmomi simulator (github.com/vmware/govmomi/simulator,
// © Broadcom, Apache-2.0 — same license as the library, see inventory.go).
package vmware

import (
	"context"
	"crypto/tls"
	"net/url"
	"strings"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// vpxServer spins an in-memory vCenter (the simulator's VPX model) with TLS on.
func vpxServer(t *testing.T) *simulator.Server {
	t.Helper()
	model := simulator.VPX()
	t.Cleanup(func() { model.Remove() })
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	model.Service.TLS = new(tls.Config)
	// Fixed credentials on the Service: with the default login the simulator
	// accepts any password, with these it really checks them.
	model.Service.Listen = &url.URL{User: url.UserPassword("user", "pass")}
	s := model.Service.NewServer()
	t.Cleanup(s.Close)
	return s
}

// vmByName finds the model's VMs through a throwaway client and changes their
// state: one powered off, one suspended, one marked as template.
func shapeModel(t *testing.T, s *simulator.Server) {
	t.Helper()
	ctx := context.Background()
	c, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Logout(ctx) }()

	f := find.NewFinder(c.Client, false)
	dcs, err := f.DatacenterList(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	f.SetDatacenter(dcs[0])
	vms, err := f.VirtualMachineList(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*object.VirtualMachine{}
	for _, vm := range vms {
		byName[vm.Name()] = vm
	}
	for _, task := range []struct {
		name string
		run  func(*object.VirtualMachine) error
	}{
		{"DC0_H0_VM1", func(vm *object.VirtualMachine) error {
			task, err := vm.PowerOff(ctx)
			if err != nil {
				return err
			}
			_, err = task.WaitForResult(ctx, nil)
			return err
		}},
		{"DC0_C0_RP0_VM0", func(vm *object.VirtualMachine) error {
			task, err := vm.Suspend(ctx)
			if err != nil {
				return err
			}
			_, err = task.WaitForResult(ctx, nil)
			return err
		}},
		{"DC0_C0_RP0_VM1", func(vm *object.VirtualMachine) error {
			// A template must be powered off before it is marked as one.
			task, err := vm.PowerOff(ctx)
			if err != nil {
				return err
			}
			if _, err = task.WaitForResult(ctx, nil); err != nil {
				return err
			}
			return vm.MarkAsTemplate(ctx)
		}},
	} {
		vm, ok := byName[task.name]
		if !ok {
			t.Fatalf("the VPX model has no VM %q", task.name)
		}
		if err := task.run(vm); err != nil {
			t.Fatalf("%s: %v", task.name, err)
		}
	}
}

func TestListInventory(t *testing.T) {
	s := vpxServer(t)
	shapeModel(t, s)

	// The right thumbprint, in the store's accepted compact lowercase form,
	// must connect — with Insecure, i.e. pinned to exactly that certificate.
	tp := soap.ThumbprintSHA256(s.Certificate())
	conn := Connection{
		URL:        "https://" + s.URL.Host,
		Username:   "user",
		Password:   "pass",
		Thumbprint: strings.ToLower(strings.ReplaceAll(tp, ":", "")),
		Insecure:   true,
	}
	inv, err := (GovmomiLister{}).List(context.Background(), conn)
	if err != nil {
		t.Fatalf("List with the right thumbprint: %v", err)
	}

	if len(inv.Datacenters) != 1 || inv.Datacenters[0].Name != "DC0" {
		t.Errorf("datacenters = %+v, want one DC0", inv.Datacenters)
	}
	if len(inv.Hosts) != 4 {
		t.Fatalf("hosts = %+v, want the 4 hosts of the VPX model", inv.Hosts)
	}
	for _, h := range inv.Hosts {
		if !strings.HasPrefix(h.Name, "DC0_") || h.Datacenter != "DC0" {
			t.Errorf("host %+v is not of DC0", h)
		}
	}

	// The template (DC0_C0_RP0_VM1) is out of the list entirely.
	if len(inv.VMs) != 3 {
		t.Fatalf("VMs = %d (%v), want the model's 3 non-template VMs", len(inv.VMs), names(inv.VMs))
	}
	byName := map[string]VM{}
	for _, vm := range inv.VMs {
		byName[vm.Name] = vm
	}
	states := map[string]string{
		"DC0_H0_VM0":     "on",        // the model autostarts every VM
		"DC0_H0_VM1":     "off",       // powered off above
		"DC0_C0_RP0_VM0": "suspended", // suspended above
	}
	for name, want := range states {
		vm, ok := byName[name]
		if !ok {
			t.Errorf("VM %q missing from the inventory", name)
			continue
		}
		if vm.PowerState != want {
			t.Errorf("%s: PowerState = %q, want %q", name, vm.PowerState, want)
		}
		if vm.Datacenter != "DC0" {
			t.Errorf("%s: Datacenter = %q, want DC0", name, vm.Datacenter)
		}
		if vm.VCPUs != 1 || vm.MemoryMiB != 32 {
			t.Errorf("%s: VCPUs = %d, MemoryMiB = %d, want 1/32 (model defaults)", name, vm.VCPUs, vm.MemoryMiB)
		}
		if vm.GuestOS != "otherGuest" {
			t.Errorf("%s: GuestOS = %q, want otherGuest", name, vm.GuestOS)
		}
		if vm.ID == "" {
			t.Errorf("%s: ID (the mo-ref) is empty", name)
		}
	}

	// The standalone-host VM carries its host, one 10 GiB disk on LocalDS_0
	// and one NIC on the model's distributed port group.
	vm := byName["DC0_H0_VM0"]
	if vm.Host != "DC0_H0" {
		t.Errorf("DC0_H0_VM0: Host = %q, want DC0_H0", vm.Host)
	}
	if len(vm.Disks) != 1 {
		t.Fatalf("DC0_H0_VM0: disks = %+v, want one", vm.Disks)
	}
	if want := uint64(10 * 1024 * 1024 * 1024); vm.Disks[0].CapacityBytes != want {
		t.Errorf("DC0_H0_VM0: disk capacity = %d, want %d", vm.Disks[0].CapacityBytes, want)
	}
	if !strings.HasPrefix(vm.Disks[0].Datastore, "LocalDS_") {
		t.Errorf("DC0_H0_VM0: disk datastore = %q, want LocalDS_…", vm.Disks[0].Datastore)
	}
	if len(vm.NICs) != 1 {
		t.Fatalf("DC0_H0_VM0: NICs = %+v, want one", vm.NICs)
	}
	if vm.NICs[0].Network != "DC0_DVPG0" {
		t.Errorf("DC0_H0_VM0: NIC network = %q, want the port group DC0_DVPG0", vm.NICs[0].Network)
	}
	if vm.NICs[0].MAC == "" {
		t.Error("DC0_H0_VM0: NIC MAC is empty")
	}
}

func TestListThumbprint(t *testing.T) {
	s := vpxServer(t)
	base := Connection{
		URL:      "https://" + s.URL.Host,
		Username: "user",
		Password: "pass",
	}
	ctx := context.Background()

	// A wrong thumbprint must not connect: the certificate does not match.
	tp := soap.ThumbprintSHA256(s.Certificate())
	wrong := "00" + tp[2:]
	if wrong == tp {
		wrong = "FF" + tp[2:]
	}
	_, err := (GovmomiLister{}).List(ctx, Connection{
		URL: base.URL, Username: base.Username, Password: base.Password,
		Thumbprint: wrong, Insecure: true,
	})
	if err == nil {
		t.Fatal("List with a wrong thumbprint connected, want refusal")
	}

	// Without a thumbprint the connection is fully verified — and the
	// simulator's self-signed certificate fails that verification.
	_, err = (GovmomiLister{}).List(ctx, base)
	if err == nil {
		t.Fatal("List against a self-signed certificate without a thumbprint connected, want refusal")
	}
}

func TestListRedactsPassword(t *testing.T) {
	s := vpxServer(t)
	const pw = "hunter2-vmware!"
	_, err := (GovmomiLister{}).List(context.Background(), Connection{
		URL:        "https://" + s.URL.Host,
		Username:   "user",
		Password:   pw,
		Thumbprint: soap.ThumbprintSHA256(s.Certificate()),
		Insecure:   true,
	})
	if err == nil {
		t.Fatal("List with a wrong password connected, want refusal")
	}
	if strings.Contains(err.Error(), pw) {
		t.Fatalf("the login error leaks the password: %s", err.Error())
	}
}

func TestVMFromMoSkipsUnusable(t *testing.T) {
	// A template is a mould for new machines, not one to migrate.
	if _, ok := vmFromMo(&mo.VirtualMachine{
		Config: &types.VirtualMachineConfigInfo{Template: true},
	}, "DC0", nil, nil, nil); ok {
		t.Error("a template must be skipped")
	}
	// A VM without a config (orphaned or broken) has no hardware to read.
	if _, ok := vmFromMo(&mo.VirtualMachine{}, "DC0", nil, nil, nil); ok {
		t.Error("a VM without a config must be skipped")
	}
}

func names(vms []VM) []string {
	out := make([]string, len(vms))
	for i, vm := range vms {
		out[i] = vm.Name
	}
	return out
}
