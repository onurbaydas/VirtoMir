package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

func replaceBool() []planmodifier.Bool {
	return []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
}

// ---- virtomir_network --------------------------------------------------------------------

type networkResource struct{ c *client.Client }

func newNetwork() resource.Resource { return &networkResource{} }

type networkModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Mode    types.String `tfsdk:"mode"`
	Subnet  types.String `tfsdk:"subnet"`
	DHCP    types.Bool   `tfsdk:"dhcp"`
	Subnet6 types.String `tfsdk:"subnet6"`
	DHCP6   types.Bool   `tfsdk:"dhcp6"`
	Bridge  types.String `tfsdk:"bridge"`
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A libvirt virtual network: nat, route or isolated with its own IPv4 and/or IPv6 subnet, or bridge (machines join an existing host bridge). Any change replaces it.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replaceStr()},
			"mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("nat"), PlanModifiers: replaceStr(),
				Description: "nat, route, isolated or bridge."},
			"subnet":  schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "IPv4 network, e.g. 192.168.100.0/24 (the host is .1)."},
			"dhcp":    schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), PlanModifiers: replaceBool()},
			"subnet6": schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "IPv6 /64, e.g. fd12:3456:789a:1::/64 (SLAAC; NAT66 in nat mode)."},
			"dhcp6":   schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool()},
			"bridge":  schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "Host bridge for mode = bridge, e.g. br0."},
		},
	}
}

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec := client.NetworkSpec{Name: m.Name.ValueString(), Mode: m.Mode.ValueString(), Subnet: strOr(m.Subnet),
		DHCP: m.DHCP.ValueBool() && strOr(m.Subnet) != "", Subnet6: strOr(m.Subnet6), DHCP6: m.DHCP6.ValueBool(), Bridge: strOr(m.Bridge)}
	if err := r.c.CreateNetwork(ctx, spec); err != nil {
		resp.Diagnostics.AddError("Network not created", err.Error())
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, err := r.c.Network(ctx, m.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Network not read", err.Error())
		return
	}
	m.Mode = types.StringValue(n.Mode)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute replaces the network; nothing to do in place.
	var m networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.c.DeleteNetwork(ctx, m.Name.ValueString()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Network not deleted", err.Error())
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

// ---- virtomir_pool -------------------------------------------------------------------------

type poolResource struct{ c *client.Client }

func newPool() resource.Resource { return &poolResource{} }

type poolModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Path    types.String `tfsdk:"path"`
	Host    types.String `tfsdk:"host"`
	Export  types.String `tfsdk:"export"`
	Target  types.String `tfsdk:"target"`
	Source  types.String `tfsdk:"source"`
	Devices types.List   `tfsdk:"devices"`
}

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pool"
}

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A storage pool: a directory, an NFS share, an iSCSI target, an LVM volume group or a ZFS pool. Destroying it only forgets it; files and volumes stay.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replaceStr()},
			"type": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("dir"), PlanModifiers: replaceStr(),
				Description: "dir, netfs (NFS), iscsi, logical (LVM) or zfs."},
			"path":   schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "dir: the directory; netfs: where the share is mounted."},
			"host":   schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "netfs: NFS server; iscsi: portal."},
			"export": schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "netfs: the exported directory."},
			"target": schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "iscsi: the target IQN."},
			"source": schema.StringAttribute{Optional: true, PlanModifiers: replaceStr(), Description: "logical: volume group; zfs: zpool or dataset."},
			"devices": schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "logical: empty disks to build a new volume group from.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
		},
	}
}

func (r *poolResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec := client.PoolSpec{Name: m.Name.ValueString(), Type: m.Type.ValueString(), Path: strOr(m.Path), Host: strOr(m.Host),
		Export: strOr(m.Export), Target: strOr(m.Target), Source: strOr(m.Source), Devices: list(ctx, m.Devices)}
	if err := r.c.CreatePool(ctx, spec); err != nil {
		resp.Diagnostics.AddError("Pool not created", err.Error())
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.c.Pool(ctx, m.Name.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Pool not read", err.Error())
		return
	}
	m.Type = types.StringValue(p.Type)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.c.DeletePool(ctx, m.Name.ValueString()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Pool not removed", err.Error())
	}
}

// ---- data "virtomir_host" ------------------------------------------------------------------

type hostData struct{ c *client.Client }

func newHostData() datasource.DataSource { return &hostData{} }

type hostModel struct {
	Hostname       types.String `tfsdk:"hostname"`
	LibvirtVersion types.String `tfsdk:"libvirt_version"`
	QEMUVersion    types.String `tfsdk:"qemu_version"`
	CPUModel       types.String `tfsdk:"cpu_model"`
	CPUs           types.Int64  `tfsdk:"cpus"`
	MemoryMB       types.Int64  `tfsdk:"memory_mb"`
}

func (d *hostData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host"
}

func (d *hostData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "Facts about the host the provider talks to.",
		Attributes: map[string]dschema.Attribute{
			"hostname":        dschema.StringAttribute{Computed: true},
			"libvirt_version": dschema.StringAttribute{Computed: true},
			"qemu_version":    dschema.StringAttribute{Computed: true},
			"cpu_model":       dschema.StringAttribute{Computed: true},
			"cpus":            dschema.Int64Attribute{Computed: true},
			"memory_mb":       dschema.Int64Attribute{Computed: true},
		},
	}
}

func (d *hostData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.c = clientFrom(req.ProviderData)
}

func (d *hostData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	h, err := d.c.Info(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Host not read", err.Error())
		return
	}
	m := hostModel{Hostname: types.StringValue(h.Hostname), LibvirtVersion: types.StringValue(h.LibvirtVersion),
		QEMUVersion: types.StringValue(h.HypervisorVersion), CPUModel: types.StringValue(h.CPUModel),
		CPUs: types.Int64Value(int64(h.CPUs)), MemoryMB: types.Int64Value(int64(h.MemoryKiB >> 10))}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

var (
	_ resource.ResourceWithImportState = (*networkResource)(nil)
)
