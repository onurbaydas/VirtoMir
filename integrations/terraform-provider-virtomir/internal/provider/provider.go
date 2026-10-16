// Package provider is the Terraform provider for VirtoMir (ADR-0049).
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

type virtomirProvider struct{ version string }

// New returns the provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &virtomirProvider{version: version} }
}

type providerModel struct {
	URL      types.String `tfsdk:"url"`
	Token    types.String `tfsdk:"token"`
	Insecure types.Bool   `tfsdk:"insecure"`
	Host     types.String `tfsdk:"host"`
}

func (p *virtomirProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "virtomir"
	resp.Version = p.version
}

func (p *virtomirProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage KVM/QEMU/libvirt machines, networks and storage pools through VirtoMir.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{Optional: true,
				Description: "Address of a VirtoMir host, e.g. https://hv1:8420. Default: VIRTOMIR_URL."},
			"token": schema.StringAttribute{Optional: true, Sensitive: true,
				Description: "API token (Account → API tokens), admin role for creating things. Default: VIRTOMIR_TOKEN."},
			"insecure": schema.BoolAttribute{Optional: true,
				Description: "Accept the host's self-signed certificate. Default: VIRTOMIR_INSECURE=1."},
			"host": schema.StringAttribute{Optional: true,
				Description: "In a cluster: the member (id or name) to manage through url. Default: the host at url."},
		},
	}
}

func (p *virtomirProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	url := os.Getenv("VIRTOMIR_URL")
	if !m.URL.IsNull() && !m.URL.IsUnknown() {
		url = m.URL.ValueString()
	}
	token := os.Getenv("VIRTOMIR_TOKEN")
	if !m.Token.IsNull() && !m.Token.IsUnknown() {
		token = m.Token.ValueString()
	}
	insecure := os.Getenv("VIRTOMIR_INSECURE") == "1"
	if !m.Insecure.IsNull() && !m.Insecure.IsUnknown() {
		insecure = m.Insecure.ValueBool()
	}
	if url == "" {
		resp.Diagnostics.AddError("VirtoMir address missing", "Set url in the provider block or VIRTOMIR_URL.")
		return
	}
	c, err := client.New(url, token, insecure)
	if err != nil {
		resp.Diagnostics.AddError("Bad VirtoMir address", err.Error())
		return
	}
	if !m.Host.IsNull() {
		c.Host = m.Host.ValueString()
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *virtomirProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newMachine, newNetwork, newPool}
}

func (p *virtomirProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{newHostData}
}

// clientFrom is the shared Configure of resources and data sources.
func clientFrom(data any) *client.Client {
	c, _ := data.(*client.Client)
	return c
}
