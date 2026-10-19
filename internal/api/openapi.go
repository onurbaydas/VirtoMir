package api

import (
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/onurbaydas/virtomir/internal/auth"
)

// routeMux is the ServeMux that also remembers every route, so the OpenAPI description at
// GET /api/v1/openapi.json always matches what the server really serves.
type routeMux struct {
	*http.ServeMux
	routes []route
}

type route struct {
	method, path, handler string
}

func newRouteMux() *routeMux { return &routeMux{ServeMux: http.NewServeMux()} }

func (m *routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(pattern, h)
	if method, path, ok := strings.Cut(pattern, " "); ok && strings.HasPrefix(path, "/api/") {
		m.routes = append(m.routes, route{method: method, path: path, handler: handlerName(h)})
	}
}

// handlerName turns "…/api.(*Server).listDomains-fm" into "listDomains".
func handlerName(h any) string {
	f := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
	if f == nil {
		return ""
	}
	n := f.Name()
	if i := strings.LastIndex(n, "."); i >= 0 {
		n = n[i+1:]
	}
	n = strings.TrimSuffix(n, "-fm")
	if strings.HasPrefix(n, "func") { // an inline closure says nothing
		return ""
	}
	return n
}

// camelWords splits "addDHCPHost" into add, DHCP, Host.
func camelWords(s string) []string {
	r := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(r); i++ {
		lowerToUpper := unicode.IsUpper(r[i]) && !unicode.IsUpper(r[i-1])
		acronymEnd := unicode.IsUpper(r[i]) && unicode.IsUpper(r[i-1]) && i+1 < len(r) && unicode.IsLower(r[i+1])
		if lowerToUpper || acronymEnd {
			words = append(words, string(r[start:i]))
			start = i
		}
	}
	if start < len(r) {
		words = append(words, string(r[start:]))
	}
	return words
}

// sentence makes "listDomains" readable: "List domains".
func sentence(name string) string {
	words := camelWords(name)
	for i, w := range words {
		if i > 0 && !(len(w) > 1 && strings.ToUpper(w) == w) { // keep acronyms (XML, DHCP)
			words[i] = strings.ToLower(w)
		}
	}
	s := strings.Join(words, " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

var pathParam = regexp.MustCompile(`\{([a-zA-Z]+)(\.\.\.)?\}`)

// tagOf groups endpoints by their first path segment after /api/v1.
func tagOf(path string) string {
	rest := strings.TrimPrefix(path, "/api/v1/")
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" {
		return "general"
	}
	return seg
}

// projectSchema describes a project (ADR-0060) in the OpenAPI document; machines carry
// their project as the "project" field (empty = global).
func projectSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          str(),
			"name":        str(),
			"description": str(),
			"members": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "user or group"},
				"name": str(),
				"role": map[string]any{"type": "string", "description": "viewer, operator or admin"},
			}}},
			"limits":       map[string]any{"type": "object", "description": "machines, vcpus, memoryMiB, diskGiB; zero = unlimited"},
			"storagePools": map[string]any{"type": "array", "items": str()},
			"networks":     map[string]any{"type": "array", "items": str()},
			"created":      str(),
		},
	}
}

// catalogSchemas describe the self-service catalog (ADR-0065) in the OpenAPI document.
func catalogSchemas() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "number"} }
	return map[string]any{
		"SizePreset": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":        str(),
				"name":      str(),
				"vcpus":     map[string]any{"type": "integer"},
				"memoryMiB": map[string]any{"type": "integer", "format": "int64"},
				"diskGiB":   map[string]any{"type": "integer", "format": "int64"},
			},
		},
		"InputField": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":      str(),
				"label":    str(),
				"type":     map[string]any{"type": "string", "description": "string, password, ssh_key, number or select"},
				"required": map[string]any{"type": "boolean"},
				"default":  str(),
				"options":  map[string]any{"type": "array", "items": str()},
			},
		},
		"Blueprint": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                str(),
				"name":              str(),
				"description":       str(),
				"icon":              str(),
				"sourceType":        map[string]any{"type": "string", "description": "template or image"},
				"sourceRef":         str(),
				"targetPool":        str(),
				"presets":           map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/SizePreset"}},
				"defaultPresetId":   str(),
				"allowedNetworks":   map[string]any{"type": "array", "items": str()},
				"cloudInitTemplate": str(),
				"cloudInitFields":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/InputField"}},
				"defaultSshKeys":    map[string]any{"type": "array", "items": str(), "description": "SSH public keys added to every machine provisioned from this image blueprint"},
				"allowedProjects":   map[string]any{"type": "array", "items": str()},
				"requireApproval":   map[string]any{"type": "boolean"},
				"defaultLeaseDays":  map[string]any{"type": "integer"},
				"maxLeaseDays":      map[string]any{"type": "integer"},
				"created":           str(),
				"updated":           str(),
			},
		},
		"CatalogRequest": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":            str(),
				"blueprintId":   str(),
				"projectId":     str(),
				"requester":     str(),
				"vmName":        str(),
				"presetId":      str(),
				"vcpus":         map[string]any{"type": "integer"},
				"memoryMiB":     map[string]any{"type": "integer", "format": "int64"},
				"diskGiB":       map[string]any{"type": "integer", "format": "int64"},
				"network":       str(),
				"fieldValues":   map[string]any{"type": "object", "additionalProperties": str()},
				"sshKeys":       map[string]any{"type": "array", "items": str(), "description": "public SSH keys injected into cloud-init sign-in (image blueprints)"},
				"password":      map[string]any{"type": "string", "format": "password", "description": "plaintext sign-in password; accepted on POST, hashed and never returned"},
				"leaseDays":     map[string]any{"type": "integer"},
				"status":        map[string]any{"type": "string", "description": "pending_approval, approved, rejected, provisioning, active, expired or failed"},
				"createdAt":     str(),
				"reviewedAt":    str(),
				"reviewedBy":    str(),
				"reviewComment": str(),
				"machineUuid":   str(),
				"errorMessage":  str(),
			},
		},
		"PricingConfig": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"vcpuHour":     num(),
				"ramGiBHour":   num(),
				"diskGiBMonth": num(),
				"currency":     str(),
			},
		},
		"ShowbackReport": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"period":        map[string]any{"type": "string", "description": "YYYY-MM"},
				"year":          map[string]any{"type": "integer"},
				"month":         map[string]any{"type": "integer"},
				"generatedAt":   str(),
				"currency":      str(),
				"pricing":       map[string]any{"$ref": "#/components/schemas/PricingConfig"},
				"totalCost":     num(),
				"totalMachines": map[string]any{"type": "integer"},
				"projects": map[string]any{"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"projectId":      str(),
						"projectName":    str(),
						"machineCount":   map[string]any{"type": "integer"},
						"totalVcpus":     map[string]any{"type": "integer"},
						"totalMemoryMiB": map[string]any{"type": "integer", "format": "int64"},
						"totalDiskGiB":   map[string]any{"type": "integer", "format": "int64"},
						"vcpuCost":       num(),
						"ramCost":        num(),
						"diskCost":       num(),
						"totalCost":      num(),
					},
				}},
			},
		},
	}
}

func complianceResultSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          str(),
			"title":       str(),
			"category":    str(),
			"severity":    map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
			"status":      map[string]any{"type": "string", "enum": []string{"pass", "fail", "warn", "error"}},
			"detail":      str(),
			"remediation": str(),
			"benchmark":   str(),
		},
	}
}

// contentItemSchema describes a content library item (ADR-0063) in OpenAPI.
func contentItemSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":           str(),
			"name":         str(),
			"type":         map[string]any{"type": "string", "description": "template, iso, or image"},
			"description":  str(),
			"sourceHost":   str(),
			"keepVersions": map[string]any{"type": "integer"},
			"syncPolicy": map[string]any{"type": "object", "properties": map[string]any{
				"mode":  map[string]any{"type": "string", "description": "all, selected, or manual"},
				"hosts": map[string]any{"type": "array", "items": str()},
			}},
			"created": str(),
			"updated": str(),
			"versions": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"id":          str(),
				"version":     str(),
				"description": str(),
				"sha256":      str(),
				"size":        map[string]any{"type": "integer"},
				"format":      str(),
				"sourceHost":  str(),
				"sourcePool":  str(),
				"sourceVol":   str(),
				"created":     str(),
			}}},
		},
	}
}

func hostComplianceSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "integer"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"hostId":       str(),
			"hostName":     str(),
			"evaluatedAt":  map[string]any{"type": "string", "format": "date-time"},
			"score":        num(),
			"passedCount":  num(),
			"failedCount":  num(),
			"warningCount": num(),
			"totalCount":   num(),
			"results": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/components/schemas/complianceResult"},
			},
		},
	}
}

func fipsInfoSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kernelEnabled": boolean(),
			"enforced":      boolean(),
			"tlsMinVersion": str(),
			"cipherSuites":  map[string]any{"type": "array", "items": str()},
			"curves":        map[string]any{"type": "array", "items": str()},
			"description":   str(),
		},
	}
}

func auditVerificationSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"valid":         boolean(),
			"verifiedCount": num(),
			"firstSeq":      num(),
			"lastSeq":       num(),
			"headHash":      str(),
			"tamperedSeq":   num(),
			"tamperedIndex": num(),
			"reason":        str(),
		},
	}
}

// openAPI builds an OpenAPI 3.1 document of the registered routes: path, method, parameters,
// the role each needs and how to authenticate. Bodies are JSON unless noted; the types are
// the ones in web/src/lib/api.ts.
// responseSchemas are the success answers the document describes; openapi_test.go checks
// the server's real answers against them, without fields they do not model (#15).
var responseSchemas = map[string]any{
	"GET /api/v1/projects": map[string]any{"type": "object", "properties": map[string]any{
		"projects": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Project"}},
	}},
	"GET /api/v1/projects/{id}": map[string]any{"$ref": "#/components/schemas/Project"},
	"GET /api/v1/gpu/pools": map[string]any{"type": "object", "properties": map[string]any{
		"pools": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/GPUPoolView"}},
	}},
	"GET /api/v1/gpu/pools/{id}": map[string]any{"$ref": "#/components/schemas/GPUPoolView"},
	"GET /api/v1/gpu/pools/{id}/availability": map[string]any{"type": "object", "properties": map[string]any{
		"hosts": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/GPUResource"}},
	}},
}

// gpuSchemas describe GPU pools (ADR-0071).
func gpuSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	pool := map[string]any{
		"id": str, "name": str, "description": str, "model": str,
		"kind":    map[string]any{"type": "string", "enum": []any{"whole", "mdev", "mig"}},
		"profile": map[string]any{"type": "string", "description": "the vGPU type (mdev) or MIG profile (mig)"},
		"perHost": integer, "created": str,
	}
	view := map[string]any{"total": integer, "used": integer, "free": integer,
		"hosts": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/GPUResource"}}}
	for k, v := range pool {
		view[k] = v
	}
	return map[string]any{
		"GPUPool":     map[string]any{"type": "object", "properties": pool, "required": []any{"id", "name", "kind"}},
		"GPUPoolView": map[string]any{"type": "object", "properties": view},
		"GPUResource": map[string]any{"type": "object", "properties": map[string]any{
			"hostId": str, "hostName": str, "poolId": str, "model": str, "kind": str,
			"total": integer, "used": integer, "free": integer, "reserved": integer, "telemetry": str,
		}},
	}
}

func (s *Server) openAPI(routes []route) map[string]any {
	sorted := append([]route(nil), routes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].path != sorted[j].path {
			return sorted[i].path < sorted[j].path
		}
		return sorted[i].method < sorted[j].method
	})
	paths := map[string]map[string]any{}
	tags := map[string]bool{}
	for _, r := range sorted {
		if strings.HasPrefix(r.path, "/api/v1/cluster/peer/") || strings.HasPrefix(r.path, "/api/v1/migrate/") {
			continue // host-to-host protocol, not for people or scripts
		}
		p := pathParam.ReplaceAllString(r.path, "{$1}")
		op := map[string]any{
			"operationId": r.handler,
			"tags":        []string{tagOf(r.path)},
			"responses": map[string]any{
				"2XX": map[string]any{"description": "success"},
				"default": map[string]any{"description": "error", "content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}},
				}},
			},
		}
		if sch, ok := responseSchemas[r.method+" "+p]; ok {
			op["responses"].(map[string]any)["2XX"] = map[string]any{"description": "success", "content": map[string]any{
				"application/json": map[string]any{"schema": sch},
			}}
		}
		tags[tagOf(r.path)] = true
		if sum := sentence(r.handler); sum != "" {
			op["summary"] = sum
		}
		role := requiredRole(r.method, p)
		if role == auth.NoAccess {
			op["security"] = []any{}
			op["description"] = "No sign-in needed."
		} else {
			op["description"] = "Needs role: " + role.String() + "."
		}
		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(r.path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if r.method == http.MethodPost || r.method == http.MethodPut || r.method == http.MethodPatch {
			body := map[string]any{"type": "object"}
			if strings.HasPrefix(p, "/api/v1/projects") {
				// Projects (ADR-0060): bodies are whole Project objects.
				body = map[string]any{"$ref": "#/components/schemas/Project"}
			}
			if strings.HasPrefix(p, "/api/v1/gpu/pools") {
				body = map[string]any{"$ref": "#/components/schemas/GPUPool"}
			}
			if strings.HasPrefix(p, "/api/v1/content/items") {
				body = map[string]any{"$ref": "#/components/schemas/ContentItem"}
			}
			if strings.HasPrefix(p, "/api/v1/domains/") && strings.HasSuffix(p, "/project") {
				body = map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string", "description": "the project the machine moves into; empty makes it global again"}}}
			}
			switch {
			case strings.HasPrefix(p, "/api/v1/catalog/blueprints"):
				body = map[string]any{"$ref": "#/components/schemas/Blueprint"}
			case p == "/api/v1/catalog/requests":
				body = map[string]any{"$ref": "#/components/schemas/CatalogRequest"}
			case strings.HasPrefix(p, "/api/v1/catalog/requests/") && strings.HasSuffix(p, "/approve"),
				strings.HasPrefix(p, "/api/v1/catalog/requests/") && strings.HasSuffix(p, "/reject"):
				body = map[string]any{"type": "object", "properties": map[string]any{"comment": map[string]any{"type": "string"}}}
			case strings.HasPrefix(p, "/api/v1/catalog/requests/") && strings.HasSuffix(p, "/extend"):
				body = map[string]any{"type": "object", "properties": map[string]any{"days": map[string]any{"type": "integer"}}}
			case p == "/api/v1/catalog/pricing":
				body = map[string]any{"$ref": "#/components/schemas/PricingConfig"}
			}
			op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": body}}}
		}
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(r.method)] = op
	}
	var tagList []any
	names := make([]string, 0, len(tags))
	for t := range tags {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		tagList = append(tagList, map[string]any{"name": t})
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "VirtoMir API",
			"version": s.version,
			"description": "Everything the VirtoMir web UI does goes through this API. Authenticate with an API token " +
				"(Account → API tokens) as `Authorization: Bearer <token>`; its role is capped by its owner's. " +
				"In a cluster, /api/v1/hosts/{host}/… reaches the same endpoint on another member.",
		},
		"servers": []any{map[string]any{"url": "/"}},
		"tags":    tagList,
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"token":   map[string]any{"type": "http", "scheme": "bearer"},
				"session": map[string]any{"type": "apiKey", "in": "cookie", "name": auth.CookieName},
			},
			"schemas": map[string]any{
				"Error":             map[string]any{"type": "object", "properties": map[string]any{"error": map[string]any{"type": "string"}}},
				"Project":           projectSchema(),
				"ContentItem":       contentItemSchema(),
				"complianceResult":  complianceResultSchema(),
				"ComplianceResult":  complianceResultSchema(),
				"hostCompliance":    hostComplianceSchema(),
				"HostCompliance":    hostComplianceSchema(),
				"fipsInfo":          fipsInfoSchema(),
				"FIPSInfo":          fipsInfoSchema(),
				"auditVerification": auditVerificationSchema(),
				"AuditVerification": auditVerificationSchema(),
			},
		},
		"security": []any{map[string]any{"token": []any{}}, map[string]any{"session": []any{}}},
	}
	for name, sch := range gpuSchemas() {
		doc["components"].(map[string]any)["schemas"].(map[string]any)[name] = sch
	}
	for name, sch := range catalogSchemas() { // self-service catalog (ADR-0065)
		doc["components"].(map[string]any)["schemas"].(map[string]any)[name] = sch
	}
	return doc
}
