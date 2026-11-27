package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

func TestDataSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/domains":
			_, _ = w.Write([]byte(`{"domains":[{"uuid":"u1","name":"web","state":"running","vcpus":2,"maxMemoryKiB":2097152,"autostart":true,"project":"team-a"},{"uuid":"t1","name":"debian-13-base","state":"shutoff","vcpus":2,"maxMemoryKiB":2097152,"template":true}]}`))
		case "/api/v1/domains/u1":
			_, _ = w.Write([]byte(`{"uuid":"u1","devices":[{"group":"storage","kind":"disk"},{"group":"network","kind":"interface","addresses":["192.168.122.31/24","fe80::5054:ff:fe11:2233/64"]}]}`))
		case "/api/v1/domains/t1":
			_, _ = w.Write([]byte(`{"uuid":"t1","devices":[{"group":"network","kind":"interface"}]}`))
		case "/api/v1/networks":
			_, _ = w.Write([]byte(`{"networks":[{"name":"default","mode":"nat","bridge":"virbr0","subnet":"192.168.122.1/24","active":true}]}`))
		case "/api/v1/storage/pools":
			_, _ = w.Write([]byte(`{"pools":[{"name":"images","type":"dir","path":"/var/lib/libvirt/images","active":true,"capacityBytes":1000,"allocationBytes":400,"availableBytes":600}]}`))
		case "/api/v1/projects/team-a":
			_, _ = w.Write([]byte(`{"id":"team-a","name":"Team A","storagePools":["images"],"networks":null,"members":[],"limits":{"machines":10,"vcpus":40,"memoryMiB":65536,"diskGiB":500},"usage":{"machines":3}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	defer srv.Close()
	c, err := client.New(srv.URL, "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	m, err := readMachine(ctx, c, "", "debian-13-base")
	if err != nil || m.ID.ValueString() != "t1" || !m.Template.ValueBool() || m.MemoryMB.ValueInt64() != 2048 {
		t.Errorf("machine by name %+v %v", m, err)
	}
	if len(m.Addresses.Elements()) != 0 || m.Project.ValueString() != "" {
		t.Errorf("template addresses/project %+v", m)
	}
	web, err := readMachine(ctx, c, "u1", "")
	var addrs []string
	if err == nil {
		web.Addresses.ElementsAs(ctx, &addrs, false)
	}
	if err != nil || web.Project.ValueString() != "team-a" || len(addrs) != 2 || addrs[0] != "192.168.122.31/24" {
		t.Errorf("machine by id %+v %v %v", web, addrs, err)
	}
	if _, err := readMachine(ctx, c, "", ""); err == nil {
		t.Error("no id or name accepted")
	}
	if _, err := readMachine(ctx, c, "", "nope"); err == nil {
		t.Error("unknown machine found")
	}
	n, err := readNetwork(ctx, c, "default")
	if err != nil || n.Mode.ValueString() != "nat" || n.Subnet.ValueString() != "192.168.122.1/24" || n.IPv4CIDR.ValueString() != "192.168.122.0/24" {
		t.Errorf("network %+v %v", n, err)
	}
	p, err := readPool(ctx, c, "images")
	if err != nil || p.Path.ValueString() != "/var/lib/libvirt/images" || !p.Active.ValueBool() ||
		p.CapacityBytes.ValueInt64() != 1000 || p.AllocatedBytes.ValueInt64() != 400 || p.AvailableBytes.ValueInt64() != 600 {
		t.Errorf("pool %+v %v", p, err)
	}
	pr, err := readProject(ctx, c, "team-a")
	if err != nil || pr.Name.ValueString() != "Team A" || pr.Machines.ValueInt64() != 3 || len(pr.Networks.Elements()) != 0 || len(pr.StoragePools.Elements()) != 1 ||
		pr.Limits == nil || pr.Limits.VCPUs.ValueInt64() != 40 || pr.Limits.DiskGiB.ValueInt64() != 500 {
		t.Errorf("project %+v %v", pr, err)
	}
	if _, err := readProject(ctx, c, "nope"); !errors.Is(err, client.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}

	names := map[string]bool{}
	for _, fn := range (&virtomirProvider{}).DataSources(ctx) {
		var resp datasource.MetadataResponse
		fn().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "virtomir"}, &resp)
		var sr datasource.SchemaResponse
		fn().Schema(ctx, datasource.SchemaRequest{}, &sr)
		if sr.Diagnostics.HasError() {
			t.Errorf("%s schema: %v", resp.TypeName, sr.Diagnostics)
		}
		names[resp.TypeName] = true
	}
	for _, want := range []string{"virtomir_host", "virtomir_machine", "virtomir_network", "virtomir_pool", "virtomir_project"} {
		if !names[want] {
			t.Errorf("no data source %s", want)
		}
	}
}

// Each data source's model fits its schema: what Read sets into the state is what Terraform
// gets (a field missing or mistyped fails here, not first in terraform plan).
func TestDataSourceModelsFitTheirSchemas(t *testing.T) {
	ctx := context.Background()
	str := types.StringValue
	i := types.Int64Value
	list, _ := types.ListValueFrom(ctx, types.StringType, []string{"192.168.122.31/24"})
	empty, _ := types.ListValueFrom(ctx, types.StringType, []string{})
	for _, c := range []struct {
		ds    datasource.DataSource
		model any
	}{
		{newMachineData(), &machineDataModel{ID: str("u1"), Name: str("web"), State: str("running"), VCPUs: i(2), MemoryMB: i(2048),
			Autostart: types.BoolValue(true), Template: types.BoolValue(false), Project: str("team-a"), Addresses: list}},
		{newNetworkData(), &networkDataModel{Name: str("default"), Mode: str("nat"), Bridge: str("virbr0"), Subnet: str("192.168.122.1/24"),
			Subnet6: str(""), IPv4CIDR: str("192.168.122.0/24"), Active: types.BoolValue(true)}},
		{newPoolData(), &poolDataModel{Name: str("default"), Type: str("dir"), Path: str("/x"), Active: types.BoolValue(true),
			CapacityBytes: i(1), AllocatedBytes: i(1), AvailableBytes: i(0)}},
		{newProjectData(), &projectDataModel{ID: str("team-a"), Name: str("Team A"), Description: str(""), StoragePools: empty, Networks: empty,
			Machines: i(1), Limits: &projectLimitsModel{Machines: i(10), VCPUs: i(40), MemoryMiB: i(65536), DiskGiB: i(500)}}},
	} {
		var sr datasource.SchemaResponse
		c.ds.Schema(ctx, datasource.SchemaRequest{}, &sr)
		state := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		if diags := state.Set(ctx, c.model); diags.HasError() {
			t.Errorf("%T: %v", c.ds, diags)
		}
	}
}
