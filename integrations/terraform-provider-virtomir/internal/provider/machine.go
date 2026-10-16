package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

// virtomir_machine: a machine created from scratch (new disk, cloud image, ISO, existing
// disk) or from a template. vCPUs, memory, power and autostart change in place; everything
// that defines how it was made replaces it.
type machineResource struct{ c *client.Client }

func newMachine() resource.Resource { return &machineResource{} }

type cloudInitModel struct {
	User     types.String `tfsdk:"user"`
	Password types.String `tfsdk:"password"`
	SSHKeys  types.List   `tfsdk:"ssh_keys"`
	Hostname types.String `tfsdk:"hostname"`
	Packages types.List   `tfsdk:"packages"`
}

type machineModel struct {
	ID          types.String    `tfsdk:"id"`
	Name        types.String    `tfsdk:"name"`
	OS          types.String    `tfsdk:"os"`
	VCPUs       types.Int64     `tfsdk:"vcpus"`
	MemoryMB    types.Int64     `tfsdk:"memory_mb"`
	Firmware    types.String    `tfsdk:"firmware"`
	Pool        types.String    `tfsdk:"pool"`
	DiskGB      types.Int64     `tfsdk:"disk_gb"`
	DiskPath    types.String    `tfsdk:"disk_path"`
	BaseImage   types.String    `tfsdk:"base_image"`
	ISO         types.String    `tfsdk:"iso"`
	Network     types.String    `tfsdk:"network"`
	Bridge      types.String    `tfsdk:"bridge"`
	Template    types.String    `tfsdk:"template"`
	Linked      types.Bool      `tfsdk:"linked"`
	CloudInit   *cloudInitModel `tfsdk:"cloud_init"`
	Running     types.Bool      `tfsdk:"running"`
	Autostart   types.Bool      `tfsdk:"autostart"`
	DeleteDisks types.Bool      `tfsdk:"delete_disks"`
	State       types.String    `tfsdk:"state"`
}

func (r *machineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine"
}

func replaceStr() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplace()}
}
func replaceInt() []planmodifier.Int64 {
	return []planmodifier.Int64{int64planmodifier.RequiresReplace()}
}

func (r *machineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A virtual machine. Give pool + disk_gb for a new disk, base_image for a copy of a cloud image, disk_path to boot an existing disk, iso for installation media, or template for a machine made from a VirtoMir template.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The machine's UUID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replaceStr()},
			"os": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("linux"),
				Description: "linux, windows or other: picks devices that work without extra drivers.", PlanModifiers: replaceStr()},
			"vcpus":     schema.Int64Attribute{Required: true, Description: "Changed in place (live where the guest allows)."},
			"memory_mb": schema.Int64Attribute{Required: true, Description: "MiB; changed in place."},
			"firmware": schema.StringAttribute{Optional: true, Description: "uefi (default for new machines) or bios.",
				PlanModifiers: replaceStr()},
			"pool":       schema.StringAttribute{Optional: true, Description: "Storage pool for a new disk or the copy of base_image.", PlanModifiers: replaceStr()},
			"disk_gb":    schema.Int64Attribute{Optional: true, Description: "Size of the new disk (or base_image grown to it).", PlanModifiers: replaceInt()},
			"disk_path":  schema.StringAttribute{Optional: true, Description: "An existing disk image to boot as it is.", PlanModifiers: replaceStr()},
			"base_image": schema.StringAttribute{Optional: true, Description: "Path of a cloud image to copy (e.g. one in a pool).", PlanModifiers: replaceStr()},
			"iso":        schema.StringAttribute{Optional: true, Description: "Path of installation media to boot.", PlanModifiers: replaceStr()},
			"network":    schema.StringAttribute{Optional: true, Description: "Virtual network for the adapter (default: none unless bridge).", PlanModifiers: replaceStr()},
			"bridge":     schema.StringAttribute{Optional: true, Description: "Host bridge for the adapter instead of a network.", PlanModifiers: replaceStr()},
			"template":   schema.StringAttribute{Optional: true, Description: "Template (name or UUID) to make the machine from.", PlanModifiers: replaceStr()},
			"linked": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description:   "With template: a linked clone (instant, stores only its changes).",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"cloud_init": schema.SingleNestedAttribute{Optional: true,
				Description:   "First-boot configuration of a cloud image or template with cloud-init.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"user":     schema.StringAttribute{Required: true},
					"password": schema.StringAttribute{Optional: true, Sensitive: true},
					"ssh_keys": schema.ListAttribute{Optional: true, ElementType: types.StringType,
						PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
					"hostname": schema.StringAttribute{Optional: true},
					"packages": schema.ListAttribute{Optional: true, ElementType: types.StringType},
				}},
			"running": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Keep it running (true) or shut down (false)."},
			"autostart": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Start it when the host boots."},
			"delete_disks": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "On destroy, also delete its own disk images (shared images, ISOs and template disks stay)."},
			"state": schema.StringAttribute{Computed: true, Description: "libvirt state: running, shutoff, …"},
		},
	}
}

func (r *machineResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func strOr(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func list(ctx context.Context, l types.List) []string {
	var out []string
	if l.IsNull() || l.IsUnknown() {
		return out
	}
	_ = l.ElementsAs(ctx, &out, false)
	return out
}

func (m *machineModel) cloudInit(ctx context.Context) *client.CloudInit {
	if m.CloudInit == nil {
		return nil
	}
	return &client.CloudInit{User: strOr(m.CloudInit.User), Password: strOr(m.CloudInit.Password),
		SSHKeys: list(ctx, m.CloudInit.SSHKeys), Hostname: strOr(m.CloudInit.Hostname), Packages: list(ctx, m.CloudInit.Packages)}
}

func (r *machineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m machineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var uuid string
	var err error
	run := m.Running.ValueBool()
	if t := strOr(m.Template); t != "" {
		tpl, e := r.c.DomainByName(ctx, t)
		if e != nil {
			resp.Diagnostics.AddError("Template not found", e.Error())
			return
		}
		uuid, err = r.c.Deploy(ctx, tpl.UUID, client.DeploySpec{Name: m.Name.ValueString(), Linked: m.Linked.ValueBool(), CloudInit: m.cloudInit(ctx)})
		if err == nil {
			// The template decides the hardware; bring vCPUs and memory to the plan.
			err = r.c.Resize(ctx, uuid, int(m.VCPUs.ValueInt64()), uint64(m.MemoryMB.ValueInt64()))
		}
		if err == nil && run {
			err = r.c.Action(ctx, uuid, "start")
		}
	} else {
		spec := client.CreateSpec{Name: m.Name.ValueString(), OS: strOr(m.OS), VCPUs: int(m.VCPUs.ValueInt64()),
			MemoryMiB: uint64(m.MemoryMB.ValueInt64()), Firmware: strOr(m.Firmware), Pool: strOr(m.Pool),
			DiskPath: strOr(m.DiskPath), BaseImage: strOr(m.BaseImage), ISO: strOr(m.ISO), CloudInit: m.cloudInit(ctx), Start: run}
		if !m.DiskGB.IsNull() {
			spec.DiskGiB = uint64(m.DiskGB.ValueInt64())
		}
		if n, b := strOr(m.Network), strOr(m.Bridge); n != "" || b != "" {
			spec.Network = &client.NIC{Network: n, Bridge: b}
		}
		uuid, err = r.c.CreateDomain(ctx, spec)
	}
	if err != nil {
		if uuid != "" {
			// It exists but is not as planned: keep it in the state (Terraform taints it and
			// replaces it next time, with its disks, as delete_disks says).
			m.ID = types.StringValue(uuid)
			m.State = types.StringValue("unknown")
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		}
		resp.Diagnostics.AddError("Machine not created", err.Error())
		return
	}
	m.ID = types.StringValue(uuid)
	if m.Autostart.ValueBool() {
		if err := r.c.SetAutostart(ctx, uuid, true); err != nil {
			resp.Diagnostics.AddError("Autostart not set", err.Error())
			return
		}
	}
	r.refresh(ctx, &m, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type diags interface {
	AddError(string, string)
}

func (r *machineResource) refresh(ctx context.Context, m *machineModel, d diags) bool {
	dom, err := r.c.Domain(ctx, m.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		return false
	}
	if err != nil {
		d.AddError("Machine not read", err.Error())
		return true
	}
	m.Name = types.StringValue(dom.Name)
	m.VCPUs = types.Int64Value(int64(dom.VCPUs))
	m.MemoryMB = types.Int64Value(int64(dom.MaxMemoryKiB >> 10))
	m.State = types.StringValue(dom.State)
	m.Running = types.BoolValue(dom.Running())
	m.Autostart = types.BoolValue(dom.Autostart)
	return true
}

func (r *machineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m machineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, &m, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan creates it again
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *machineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state machineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.ID.ValueString()
	plan.ID = state.ID
	if !plan.VCPUs.Equal(state.VCPUs) || !plan.MemoryMB.Equal(state.MemoryMB) {
		if err := r.c.Resize(ctx, uuid, int(plan.VCPUs.ValueInt64()), uint64(plan.MemoryMB.ValueInt64())); err != nil {
			resp.Diagnostics.AddError("vCPUs/memory not changed", err.Error())
			return
		}
	}
	if !plan.Autostart.Equal(state.Autostart) {
		if err := r.c.SetAutostart(ctx, uuid, plan.Autostart.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Autostart not changed", err.Error())
			return
		}
	}
	if !plan.Running.Equal(state.Running) {
		var err error
		if plan.Running.ValueBool() {
			err = r.c.Action(ctx, uuid, "start")
		} else {
			err = r.c.Stop(ctx, uuid, 2*time.Minute)
		}
		if err != nil {
			resp.Diagnostics.AddError("Power state not changed", err.Error())
			return
		}
	}
	r.refresh(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *machineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m machineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := m.ID.ValueString()
	if err := r.c.Stop(ctx, uuid, time.Minute); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Machine not stopped", err.Error())
		return
	}
	if err := r.c.DeleteDomain(ctx, uuid, m.DeleteDisks.ValueBool()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Machine not deleted", err.Error())
	}
}

func (r *machineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	dom, err := r.c.DomainByName(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Machine not found", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), dom.UUID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), dom.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("os"), "linux")...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("linked"), false)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("delete_disks"), false)...)
}

var _ resource.ResourceWithImportState = (*machineResource)(nil)
