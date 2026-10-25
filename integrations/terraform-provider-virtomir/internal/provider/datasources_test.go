package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

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
			_, _ = w.Write([]byte(`{"domains":[{"uuid":"u1","name":"web","state":"running","vcpus":2,"maxMemoryKiB":2097152,"autostart":true},{"uuid":"t1","name":"debian-13-base","state":"shutoff","vcpus":2,"maxMemoryKiB":2097152,"template":true}]}`))
		case "/api/v1/networks":
			_, _ = w.Write([]byte(`{"networks":[{"name":"default","mode":"nat","bridge":"virbr0","subnet":"192.168.122.0/24","active":true}]}`))
		case "/api/v1/storage/pools":
			_, _ = w.Write([]byte(`{"pools":[{"name":"images","type":"dir","path":"/var/lib/libvirt/images","active":true}]}`))
		case "/api/v1/projects/team-a":
			_, _ = w.Write([]byte(`{"id":"team-a","name":"Team A","storagePools":["images"],"networks":null,"members":[],"usage":{"machines":3}}`))
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
	if _, err := readMachine(ctx, c, "", ""); err == nil {
		t.Error("no id or name accepted")
	}
	if _, err := readMachine(ctx, c, "", "nope"); err == nil {
		t.Error("unknown machine found")
	}
	n, err := readNetwork(ctx, c, "default")
	if err != nil || n.Mode.ValueString() != "nat" || n.Subnet.ValueString() != "192.168.122.0/24" {
		t.Errorf("network %+v %v", n, err)
	}
	p, err := readPool(ctx, c, "images")
	if err != nil || p.Path.ValueString() != "/var/lib/libvirt/images" || !p.Active.ValueBool() {
		t.Errorf("pool %+v %v", p, err)
	}
	pr, err := readProject(ctx, c, "team-a")
	if err != nil || pr.Name.ValueString() != "Team A" || pr.Machines.ValueInt64() != 3 || len(pr.Networks.Elements()) != 0 || len(pr.StoragePools.Elements()) != 1 {
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
