// VMware inventory reader for the migration UI (ADR-0061).
//
// The inventory is taken with govmomi (github.com/vmware/govmomi v0.56.0,
// © Broadcom, Apache-2.0 — its simulator package used by the tests carries the
// same license). govmomi is imported only by internal/vmware; the libvirt
// dependency is untouched.
//
// One SOAP session per List call: log in, collect, log out (defer). The
// credentials never appear in the URL that is kept around or in any error:
// they ride on a private url.URL copy used only to dial and sign in, and every
// error is wrapped so err.Error() can never contain the password.
package vmware

import (
	"context"
	"net/url"
	"path"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// Datacenter is one vCenter datacenter.
type Datacenter struct {
	Name string `json:"name"`
}

// ESXHost is one ESXi host and the datacenter it belongs to.
type ESXHost struct {
	Name       string `json:"name"`
	Datacenter string `json:"datacenter"`
}

// VMDisk is one virtual disk of a VM.
type VMDisk struct {
	Datastore     string `json:"datastore"`
	CapacityBytes uint64 `json:"capacityBytes"`
}

// VMNIC is one network interface of a VM.
type VMNIC struct {
	Network string `json:"network"`
	MAC     string `json:"mac"`
}

// VM is a VMware machine seen by the migration UI.
type VM struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Datacenter string   `json:"datacenter"`
	Host       string   `json:"host"`
	PowerState string   `json:"powerState"`
	VCPUs      int      `json:"vcpus"`
	MemoryMiB  uint64   `json:"memoryMiB"`
	GuestOS    string   `json:"guestOs"`
	Disks      []VMDisk `json:"disks"`
	NICs       []VMNIC  `json:"nics"`
}

// Inventory is everything one List call sees.
type Inventory struct {
	Datacenters []Datacenter `json:"datacenters"`
	Hosts       []ESXHost    `json:"hosts"`
	VMs         []VM         `json:"vms"`
}

// Lister reads the inventory of a VMware environment over c.
type Lister interface {
	List(ctx context.Context, c Connection) (Inventory, error)
}

// GovmomiLister is the Lister that talks to a real vCenter or ESXi host.
type GovmomiLister struct{}

var _ Lister = GovmomiLister{}

// List reads the inventory: datacenters, their hosts and the VMs on them.
func (GovmomiLister) List(ctx context.Context, c Connection) (Inventory, error) {
	var inv Inventory

	// The stored URL is the bare host address (https://vcenter.example.com);
	// the SOAP endpoint is always /sdk under it. It holds no credentials.
	u, err := url.Parse(c.URL)
	if err != nil {
		return inv, err
	}
	u.Path = "/sdk"
	u.User, u.RawQuery, u.Fragment = nil, "", ""

	// The credentials ride on this copy alone: it is used to dial and sign in
	// and then dropped; it is never logged and never reaches an error message.
	dial := *u
	dial.User = url.UserPassword(c.Username, c.Password)

	// TLS is never skipped wholesale: the connection is either CA-verified or,
	// when the certificate is not trusted, pinned to the stored thumbprint —
	// govmomi's dial falls back to the thumbprint check exactly then, so the
	// soap client is always created with verification on. Insecure therefore
	// only means "the CA is not trusted, the thumbprint is the trust" (the
	// store refuses Insecure without one).
	sc := soap.NewClient(&dial, false)
	if c.Thumbprint != "" {
		sc.SetThumbprint(u.Host, thumbprintStd(c.Thumbprint))
	}

	vc, err := vim25.NewClient(ctx, sc)
	if err != nil {
		return inv, redact(err, c.Password)
	}
	cl := &govmomi.Client{Client: vc, SessionManager: session.NewManager(vc)}
	defer func() { _ = cl.Logout(context.Background()) }()

	if err = cl.Login(ctx, dial.User); err != nil {
		return inv, redact(err, c.Password)
	}

	inv, err = collect(ctx, cl)
	return inv, redact(err, c.Password)
}

// collect walks the inventory with the finder and the property collector.
func collect(ctx context.Context, cl *govmomi.Client) (Inventory, error) {
	var inv Inventory

	f := find.NewFinder(cl.Client, false)
	dcs, err := f.DatacenterList(ctx, "*")
	if err != nil {
		return inv, err
	}
	dcRefs := make([]types.ManagedObjectReference, len(dcs))
	for i, dc := range dcs {
		dcRefs[i] = dc.Reference()
	}
	var moDCs []mo.Datacenter
	if err = cl.Retrieve(ctx, dcRefs, []string{"name"}, &moDCs); err != nil {
		return inv, err
	}

	for i := range moDCs {
		dcName := moDCs[i].Name
		inv.Datacenters = append(inv.Datacenters, Datacenter{Name: dcName})
		f.SetDatacenter(dcs[i])

		// Port group and datastore names are looked up by managed-object
		// reference, so a NIC's network and a disk's datastore keep their name
		// even when the device backing only carries a reference (distributed
		// port groups do — their key is the reference's value part).
		netNames := map[string]string{}
		nets, err := f.NetworkList(ctx, "*")
		if err != nil {
			return inv, err
		}
		for _, n := range nets {
			r := n.Reference()
			if name := path.Base(n.GetInventoryPath()); name != "" && name != "/" && name != "." {
				netNames[r.String()] = name
				netNames[r.Value] = name
			}
		}
		dsNames := map[string]string{}
		datastores, err := f.DatastoreList(ctx, "*")
		if err != nil {
			return inv, err
		}
		for _, ds := range datastores {
			r := ds.Reference()
			dsNames[r.String()] = ds.Name()
			dsNames[r.Value] = ds.Name()
		}

		hostNames := map[string]string{}
		hosts, err := f.HostSystemList(ctx, "*")
		if err != nil {
			return inv, err
		}
		for _, h := range hosts {
			r := h.Reference()
			hostNames[r.String()] = h.Name()
			hostNames[r.Value] = h.Name()
			inv.Hosts = append(inv.Hosts, ESXHost{Name: h.Name(), Datacenter: dcName})
		}

		vms, err := f.VirtualMachineList(ctx, "*")
		if err != nil {
			return inv, err
		}
		vmRefs := make([]types.ManagedObjectReference, len(vms))
		for i, vm := range vms {
			vmRefs[i] = vm.Reference()
		}
		var moVMs []mo.VirtualMachine
		props := []string{
			"name", "summary.runtime.powerState", "summary.runtime.host",
			"config.template", "config.guestFullName",
			"config.hardware.numCPU", "config.hardware.memoryMB",
			"config.hardware.device",
		}
		if err = cl.Retrieve(ctx, vmRefs, props, &moVMs); err != nil {
			return inv, err
		}

		for i := range moVMs {
			v, ok := vmFromMo(&moVMs[i], dcName, hostNames, dsNames, netNames)
			if !ok {
				continue
			}
			inv.VMs = append(inv.VMs, v)
		}
	}
	return inv, nil
}

// vmFromMo maps one managed-object VM onto its inventory entry. ok is false
// for machines with nothing to migrate: a template is a mould for new machines,
// not one to migrate, and a VM without a config (orphaned or broken) has no
// hardware to read.
func vmFromMo(mv *mo.VirtualMachine, dcName string, hostNames, dsNames, netNames map[string]string) (v VM, ok bool) {
	if mv.Config == nil || mv.Config.Template {
		return VM{}, false
	}
	v = VM{
		ID:         mv.Self.Value,
		Name:       mv.Name,
		Datacenter: dcName,
		PowerState: powerState(mv.Summary.Runtime.PowerState),
	}
	if host := mv.Summary.Runtime.Host; host != nil {
		v.Host = hostNames[host.String()]
		if v.Host == "" {
			v.Host = hostNames[host.Value]
		}
	}
	v.VCPUs = int(mv.Config.Hardware.NumCPU)
	v.MemoryMiB = uint64(mv.Config.Hardware.MemoryMB)
	v.GuestOS = mv.Config.GuestFullName
	for _, dev := range mv.Config.Hardware.Device {
		switch d := dev.(type) {
		case *types.VirtualDisk:
			ds, file := diskBacking(d.Backing)
			name := dsName(ds, file, dsNames)
			if name == "" {
				continue
			}
			capacity := uint64(d.CapacityInBytes)
			if capacity == 0 && d.CapacityInKB > 0 {
				capacity = uint64(d.CapacityInKB) * 1024
			}
			v.Disks = append(v.Disks, VMDisk{Datastore: name, CapacityBytes: capacity})
		case types.BaseVirtualEthernetCard:
			card := d.GetVirtualEthernetCard()
			if card == nil {
				continue
			}
			v.NICs = append(v.NICs, VMNIC{
				Network: nicNetwork(card.Backing, netNames),
				MAC:     card.MacAddress,
			})
		}
	}
	return v, true
}

// powerState maps the vSphere power state onto VirtoMir's ("on"/"off"/"suspended").
func powerState(s types.VirtualMachinePowerState) string {
	switch s {
	case types.VirtualMachinePowerStatePoweredOn:
		return "on"
	case types.VirtualMachinePowerStatePoweredOff:
		return "off"
	case types.VirtualMachinePowerStateSuspended:
		return "suspended"
	}
	return ""
}

// diskBacking returns the datastore reference and file path of a virtual disk
// backing, as far as it carries them.
func diskBacking(b types.BaseVirtualDeviceBackingInfo) (ds *types.ManagedObjectReference, file string) {
	switch b := b.(type) {
	case *types.VirtualDiskFlatVer1BackingInfo:
		return b.Datastore, b.FileName
	case *types.VirtualDiskFlatVer2BackingInfo:
		return b.Datastore, b.FileName
	case *types.VirtualDiskSparseVer1BackingInfo:
		return b.Datastore, b.FileName
	case *types.VirtualDiskSparseVer2BackingInfo:
		return b.Datastore, b.FileName
	case *types.VirtualDiskSeSparseBackingInfo:
		return b.Datastore, b.FileName
	}
	return nil, ""
}

// dsName resolves a disk's datastore: by its managed-object reference when the
// backing carries one, otherwise from the "[datastore] path" file name.
func dsName(ds *types.ManagedObjectReference, file string, names map[string]string) string {
	if ds != nil && ds.Value != "" {
		if n, ok := names[ds.String()]; ok {
			return n
		}
		if n, ok := names[ds.Value]; ok {
			return n
		}
	}
	if strings.HasPrefix(file, "[") {
		if end := strings.IndexByte(file, ']'); end > 1 {
			return file[1:end]
		}
	}
	return ""
}

// nicNetwork resolves a NIC's port group: by network reference when the backing
// carries one (distributed port groups do, keyed by the reference's value),
// falling back to the backing's device name.
func nicNetwork(b types.BaseVirtualDeviceBackingInfo, names map[string]string) string {
	switch b := b.(type) {
	case *types.VirtualEthernetCardNetworkBackingInfo:
		if b.Network != nil {
			if n, ok := names[b.Network.String()]; ok {
				return n
			}
			if n, ok := names[b.Network.Value]; ok {
				return n
			}
		}
		return b.DeviceName
	case *types.VirtualEthernetCardDistributedVirtualPortBackingInfo:
		if n, ok := names[b.Port.PortgroupKey]; ok {
			return n
		}
		return b.Port.PortgroupKey
	}
	return ""
}

// thumbprintStd normalizes a stored thumbprint into govmomi's format — hex
// bytes joined with colons, uppercase — which is what its dial compares the
// certificate against. The store accepts colons or not, either case.
func thumbprintStd(t string) string {
	hex := strings.ToUpper(strings.ReplaceAll(t, ":", ""))
	var b strings.Builder
	for i := 0; i+1 < len(hex); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String()
}

// redacted wraps an error so its message can never contain the password: any
// occurrence is replaced with the mask before it reaches a log.
type redacted struct {
	err      error
	password string
}

func redact(err error, password string) error {
	if err == nil {
		return nil
	}
	return redacted{err: err, password: password}
}

func (e redacted) Error() string {
	msg := e.err.Error()
	if e.password != "" {
		msg = strings.ReplaceAll(msg, e.password, Secret)
	}
	return msg
}

func (e redacted) Unwrap() error { return e.err }
