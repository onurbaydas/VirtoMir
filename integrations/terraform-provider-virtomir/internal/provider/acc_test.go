package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests run Terraform itself against the provider. They run only with TF_ACC=1:
//
//	TF_ACC=1 go test ./internal/provider -run TestAcc                 # against a fake host
//	TF_ACC=1 VIRTOMIR_URL=https://hv1:8420 VIRTOMIR_TOKEN=… VIRTOMIR_INSECURE=1 \
//	  [VIRTOMIR_ACC_MACHINE=web-01] [VIRTOMIR_ACC_PROJECT=team-a] go test ./internal/provider -run TestAccHost
//
// terraform-plugin-testing finds terraform on PATH or downloads it.

var accFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"virtomir": providerserver.NewProtocol6WithError(New("test")()),
}

// TestAccDataSourcesFake reads all four data sources through Terraform from a fake host.
func TestAccDataSourcesFake(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC=1 runs Terraform")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, ok := map[string]string{
			"/api/v1/domains":       `{"domains":[{"uuid":"u1","name":"web","state":"running","vcpus":2,"maxMemoryKiB":2097152,"autostart":true,"project":"team-a"}]}`,
			"/api/v1/domains/u1":    `{"uuid":"u1","devices":[{"group":"network","addresses":["192.168.122.31/24"]}]}`,
			"/api/v1/networks":      `{"networks":[{"name":"default","mode":"nat","bridge":"virbr0","subnet":"192.168.122.1/24","active":true}]}`,
			"/api/v1/storage/pools": `{"pools":[{"name":"default","type":"dir","path":"/var/lib/libvirt/images","active":true,"capacityBytes":1000,"allocationBytes":400,"availableBytes":600}]}`,
			"/api/v1/projects/team-a": `{"id":"team-a","name":"Team A","storagePools":["default"],"networks":["default"],"members":[],
				"limits":{"machines":10,"vcpus":40,"memoryMiB":65536,"diskGiB":500},"usage":{"machines":1}}`,
		}[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			body = `{"error":"not found"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "virtomir" {
  url   = "` + srv.URL + `"
  token = "tok"
}
data "virtomir_machine" "web" { name = "web" }
data "virtomir_network" "default" { name = "default" }
data "virtomir_pool" "default" { name = "default" }
data "virtomir_project" "team" { id = "team-a" }
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.virtomir_machine.web", "id", "u1"),
				resource.TestCheckResourceAttr("data.virtomir_machine.web", "state", "running"),
				resource.TestCheckResourceAttr("data.virtomir_machine.web", "memory_mb", "2048"),
				resource.TestCheckResourceAttr("data.virtomir_machine.web", "project", "team-a"),
				resource.TestCheckResourceAttr("data.virtomir_machine.web", "addresses.0", "192.168.122.31/24"),
				resource.TestCheckResourceAttr("data.virtomir_network.default", "ipv4_cidr", "192.168.122.0/24"),
				resource.TestCheckResourceAttr("data.virtomir_network.default", "bridge", "virbr0"),
				resource.TestCheckResourceAttr("data.virtomir_pool.default", "available_bytes", "600"),
				resource.TestCheckResourceAttr("data.virtomir_project.team", "name", "Team A"),
				resource.TestCheckResourceAttr("data.virtomir_project.team", "limits.vcpus", "40"),
				resource.TestCheckResourceAttr("data.virtomir_project.team", "machines", "1"),
			),
		}},
	})
}

// TestAccHost reads the data sources from a real host (VIRTOMIR_URL): its default network
// and pool, and a machine and a project when they are named.
func TestAccHost(t *testing.T) {
	if os.Getenv("TF_ACC") == "" || os.Getenv("VIRTOMIR_URL") == "" {
		t.Skip("TF_ACC=1 and VIRTOMIR_URL run this against a host")
	}
	cfg := `
data "virtomir_host" "this" {}
data "virtomir_network" "default" { name = "default" }
data "virtomir_pool" "default" { name = "default" }
`
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttrSet("data.virtomir_host.this", "hostname"),
		resource.TestCheckResourceAttrSet("data.virtomir_network.default", "mode"),
		resource.TestCheckResourceAttrSet("data.virtomir_pool.default", "capacity_bytes"),
	}
	if m := os.Getenv("VIRTOMIR_ACC_MACHINE"); m != "" {
		cfg += `data "virtomir_machine" "m" { name = "` + m + `" }` + "\n"
		checks = append(checks, resource.TestCheckResourceAttrSet("data.virtomir_machine.m", "id"))
	}
	if p := os.Getenv("VIRTOMIR_ACC_PROJECT"); p != "" {
		cfg += `data "virtomir_project" "p" { id = "` + p + `" }` + "\n"
		checks = append(checks, resource.TestCheckResourceAttrSet("data.virtomir_project.p", "name"))
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps:                    []resource.TestStep{{Config: cfg, Check: resource.ComposeAggregateTestCheckFunc(checks...)}},
	})
}
