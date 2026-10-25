package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

// Data sources look up what exists already: a machine or template, a network, a storage
// pool, a project.

// ---- machine ------------------------------------------------------------------------------

type machineData struct{ c *client.Client }

func newMachineData() datasource.DataSource { return &machineData{} }

type machineDataModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	State     types.String `tfsdk:"state"`
	VCPUs     types.Int64  `tfsdk:"vcpus"`
	MemoryMB  types.Int64  `tfsdk:"memory_mb"`
	Autostart types.Bool   `tfsdk:"autostart"`
	Template  types.Bool   `tfsdk:"template"`
}

func (d *machineData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine"
}

func (d *machineData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "A machine or template, by UUID (id) or name.",
		Attributes: map[string]dschema.Attribute{
			"id":        dschema.StringAttribute{Optional: true, Computed: true, Description: "The machine's UUID."},
			"name":      dschema.StringAttribute{Optional: true, Computed: true},
			"state":     dschema.StringAttribute{Computed: true},
			"vcpus":     dschema.Int64Attribute{Computed: true},
			"memory_mb": dschema.Int64Attribute{Computed: true},
			"autostart": dschema.BoolAttribute{Computed: true},
			"template":  dschema.BoolAttribute{Computed: true},
		},
	}
}

func (d *machineData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.c = clientFrom(req.ProviderData)
}

// readMachine looks a machine up by id, or else by name.
func readMachine(ctx context.Context, c *client.Client, id, name string) (machineDataModel, error) {
	var m client.Domain
	var err error
	switch {
	case id != "":
		m, err = c.Domain(ctx, id)
	case name != "":
		m, err = c.DomainByName(ctx, name)
	default:
		return machineDataModel{}, errors.New("give the machine's id or name")
	}
	if err != nil {
		return machineDataModel{}, err
	}
	return machineDataModel{ID: types.StringValue(m.UUID), Name: types.StringValue(m.Name), State: types.StringValue(m.State),
		VCPUs: types.Int64Value(int64(m.VCPUs)), MemoryMB: types.Int64Value(int64(m.MaxMemoryKiB >> 10)),
		Autostart: types.BoolValue(m.Autostart), Template: types.BoolValue(m.Template)}, nil
}

func (d *machineData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var in machineDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &in)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := readMachine(ctx, d.c, in.ID.ValueString(), in.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Machine not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- network ------------------------------------------------------------------------------

type networkData struct{ c *client.Client }

func newNetworkData() datasource.DataSource { return &networkData{} }

type networkDataModel struct {
	Name    types.String `tfsdk:"name"`
	Mode    types.String `tfsdk:"mode"`
	Bridge  types.String `tfsdk:"bridge"`
	Subnet  types.String `tfsdk:"subnet"`
	Subnet6 types.String `tfsdk:"subnet6"`
	Active  types.Bool   `tfsdk:"active"`
}

func (d *networkData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "A virtual network, by name.",
		Attributes: map[string]dschema.Attribute{
			"name":    dschema.StringAttribute{Required: true},
			"mode":    dschema.StringAttribute{Computed: true},
			"bridge":  dschema.StringAttribute{Computed: true},
			"subnet":  dschema.StringAttribute{Computed: true},
			"subnet6": dschema.StringAttribute{Computed: true},
			"active":  dschema.BoolAttribute{Computed: true},
		},
	}
}

func (d *networkData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.c = clientFrom(req.ProviderData)
}

func readNetwork(ctx context.Context, c *client.Client, name string) (networkDataModel, error) {
	n, err := c.Network(ctx, name)
	if err != nil {
		return networkDataModel{}, err
	}
	return networkDataModel{Name: types.StringValue(n.Name), Mode: types.StringValue(n.Mode), Bridge: types.StringValue(n.Bridge),
		Subnet: types.StringValue(n.Subnet), Subnet6: types.StringValue(n.Subnet6), Active: types.BoolValue(n.Active)}, nil
}

func (d *networkData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var in networkDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &in)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, err := readNetwork(ctx, d.c, in.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Network not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &n)...)
}

// ---- pool ---------------------------------------------------------------------------------

type poolData struct{ c *client.Client }

func newPoolData() datasource.DataSource { return &poolData{} }

type poolDataModel struct {
	Name   types.String `tfsdk:"name"`
	Type   types.String `tfsdk:"type"`
	Path   types.String `tfsdk:"path"`
	Active types.Bool   `tfsdk:"active"`
}

func (d *poolData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pool"
}

func (d *poolData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "A storage pool, by name.",
		Attributes: map[string]dschema.Attribute{
			"name":   dschema.StringAttribute{Required: true},
			"type":   dschema.StringAttribute{Computed: true},
			"path":   dschema.StringAttribute{Computed: true},
			"active": dschema.BoolAttribute{Computed: true},
		},
	}
}

func (d *poolData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.c = clientFrom(req.ProviderData)
}

func readPool(ctx context.Context, c *client.Client, name string) (poolDataModel, error) {
	p, err := c.Pool(ctx, name)
	if err != nil {
		return poolDataModel{}, err
	}
	return poolDataModel{Name: types.StringValue(p.Name), Type: types.StringValue(p.Type), Path: types.StringValue(p.Path), Active: types.BoolValue(p.Active)}, nil
}

func (d *poolData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var in poolDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &in)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := readPool(ctx, d.c, in.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Storage pool not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}

// ---- project ------------------------------------------------------------------------------

type projectData struct{ c *client.Client }

func newProjectData() datasource.DataSource { return &projectData{} }

type projectDataModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	StoragePools types.List   `tfsdk:"storage_pools"`
	Networks     types.List   `tfsdk:"networks"`
	Machines     types.Int64  `tfsdk:"machines"`
}

func (d *projectData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "A project, by ID.",
		Attributes: map[string]dschema.Attribute{
			"id":            dschema.StringAttribute{Required: true},
			"name":          dschema.StringAttribute{Computed: true},
			"description":   dschema.StringAttribute{Computed: true},
			"storage_pools": dschema.ListAttribute{Computed: true, ElementType: types.StringType},
			"networks":      dschema.ListAttribute{Computed: true, ElementType: types.StringType},
			"machines":      dschema.Int64Attribute{Computed: true, Description: "How many machines it has now."},
		},
	}
}

func (d *projectData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	d.c = clientFrom(req.ProviderData)
}

func readProject(ctx context.Context, c *client.Client, id string) (projectDataModel, error) {
	p, err := c.Project(ctx, id)
	if err != nil {
		return projectDataModel{}, err
	}
	pools, d1 := types.ListValueFrom(ctx, types.StringType, nonNil(p.StoragePools))
	nets, d2 := types.ListValueFrom(ctx, types.StringType, nonNil(p.Networks))
	if d1.HasError() || d2.HasError() {
		return projectDataModel{}, fmt.Errorf("project %s: lists not read", id)
	}
	machines := int64(0)
	if p.Usage != nil {
		machines = int64(p.Usage.Machines)
	}
	return projectDataModel{ID: types.StringValue(p.ID), Name: types.StringValue(p.Name), Description: types.StringValue(p.Description),
		StoragePools: pools, Networks: nets, Machines: types.Int64Value(machines)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (d *projectData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var in projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &in)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := readProject(ctx, d.c, in.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Project not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &p)...)
}
