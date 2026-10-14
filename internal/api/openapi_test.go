package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOpenAPI(t *testing.T) {
	rec := serve(t, &fakeBackend{}, http.MethodGet, "/api/v1/openapi.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var doc struct {
		OpenAPI string
		Paths   map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string
			Description string
			Parameters  []struct{ Name, In string }
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	get := doc.Paths["/api/v1/domains/{uuid}"]["get"]
	if get.OperationID != "getDomain" || get.Summary != "Get domain" || get.Description != "Needs role: viewer." {
		t.Errorf("GET domain = %+v", get)
	}
	if len(get.Parameters) != 1 || get.Parameters[0].Name != "uuid" || get.Parameters[0].In != "path" {
		t.Errorf("parameters = %+v", get.Parameters)
	}
	if d := doc.Paths["/api/v1/domains/{uuid}"]["delete"].Description; d != "Needs role: admin." {
		t.Errorf("DELETE domain: %q", d)
	}
	if d := doc.Paths["/api/v1/auth/login"]["post"].Description; d != "No sign-in needed." {
		t.Errorf("login: %q", d)
	}
	for p := range doc.Paths {
		if len(p) > 20 && p[:20] == "/api/v1/cluster/peer" {
			t.Errorf("host-to-host route listed: %s", p)
		}
	}
	if _, ok := doc.Paths["/api/v1/metrics"]; !ok {
		t.Error("metrics missing")
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{"listDomains": "List domains", "getDomainXML": "Get domain XML", "addDHCPHost": "Add DHCP host"} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}
