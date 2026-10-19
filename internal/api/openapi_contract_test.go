package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/project"
)

// The OpenAPI document must be a well-formed OpenAPI 3.1 description, and what the server
// answers must follow it (#15). These checks need no network and no validator library:
// the document only uses a small part of JSON Schema, and contractChecker knows that part.

type openAPIDoc struct {
	OpenAPI    string                               `json:"openapi"`
	Info       map[string]any                       `json:"info"`
	Tags       []struct{ Name string }              `json:"tags"`
	Paths      map[string]map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas         map[string]any `json:"schemas"`
		SecuritySchemes map[string]any `json:"securitySchemes"`
	} `json:"components"`
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()
	rec := serve(t, &fakeBackend{}, http.MethodGet, "/api/v1/openapi.json")
	var doc openAPIDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

var (
	operationIDRE = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	templateRE    = regexp.MustCompile(`\{([^}]+)\}`)
	httpMethods   = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	schemaTypes   = []string{"object", "array", "string", "integer", "number", "boolean", "null"}
)

func TestOpenAPIDocumentIsValid(t *testing.T) {
	doc := loadOpenAPI(t)
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") || doc.Info["title"] == nil || doc.Info["version"] == nil {
		t.Fatalf("openapi %q, info %v", doc.OpenAPI, doc.Info)
	}
	c := contractChecker{schemas: doc.Components.Schemas}
	var tags []string
	for _, tg := range doc.Tags {
		tags = append(tags, tg.Name)
	}
	ids := map[string]string{}
	for path, ops := range doc.Paths {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("%s: a path starts with /", path)
		}
		for method, op := range ops {
			where := strings.ToUpper(method) + " " + path
			if !slices.Contains(httpMethods, method) {
				t.Errorf("%s: not an HTTP method", where)
				continue
			}
			id, _ := op["operationId"].(string)
			if !operationIDRE.MatchString(id) {
				t.Errorf("%s: operationId %q", where, id)
			} else if other, dup := ids[id]; dup {
				t.Errorf("%s: operationId %q is used by %s too", where, id, other)
			}
			ids[id] = where
			for _, tg := range anyList(op["tags"]) {
				if !slices.Contains(tags, tg.(string)) {
					t.Errorf("%s: tag %q is not declared", where, tg)
				}
			}
			var params []string
			for _, p := range anyList(op["parameters"]) {
				pm := p.(map[string]any)
				if pm["in"] == "path" {
					params = append(params, pm["name"].(string))
					if pm["required"] != true {
						t.Errorf("%s: path parameter %v must be required", where, pm["name"])
					}
				}
				c.checkSchema(t, where+" parameter", pm["schema"])
			}
			var inPath []string
			for _, m := range templateRE.FindAllStringSubmatch(path, -1) {
				inPath = append(inPath, m[1])
			}
			sort.Strings(params)
			sort.Strings(inPath)
			if !slices.Equal(params, inPath) {
				t.Errorf("%s: path parameters %v, the path has %v", where, params, inPath)
			}
			responses, _ := op["responses"].(map[string]any)
			if len(responses) == 0 {
				t.Errorf("%s: no responses", where)
			}
			for code, r := range responses {
				rm := r.(map[string]any)
				if _, ok := rm["description"].(string); !ok {
					t.Errorf("%s %s: a response needs a description", where, code)
				}
				c.checkContent(t, where+" "+code, rm["content"])
			}
			if rb, ok := op["requestBody"].(map[string]any); ok {
				c.checkContent(t, where+" request", rb["content"])
			}
			if sec, ok := op["security"]; ok {
				for _, req := range anyList(sec) {
					for name := range req.(map[string]any) {
						if doc.Components.SecuritySchemes[name] == nil {
							t.Errorf("%s: security scheme %q is not declared", where, name)
						}
					}
				}
			}
		}
	}
	for name, sch := range doc.Components.Schemas {
		c.checkSchema(t, "schema "+name, sch)
	}
}

// Every GET without parameters answers with what the document says: JSON in the declared
// shape, and errors as {"error": "..."}.
func TestAPIAnswersFollowOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	c := contractChecker{schemas: doc.Components.Schemas}
	h := newTestServer(&fakeBackend{}, &fakeLive{}).Handler()
	notJSON := map[string]bool{"/api/v1/metrics": true} // Prometheus text format
	checked := 0
	for path, ops := range doc.Paths {
		op, ok := ops["get"]
		if !ok || strings.Contains(path, "{") || strings.HasSuffix(path, "/stream") || strings.HasSuffix(path, "/events") {
			continue
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		checked++
		c.checkAnswer(t, "GET "+path, op, rec, notJSON[path])
	}
	if checked < 50 {
		t.Errorf("only %d endpoints checked", checked)
	}
}

// Projects carry a real schema: a stored project must come back exactly in its shape.
func TestProjectsFollowTheirSchema(t *testing.T) {
	n := newTestNode(t, "alpha", nil)
	if _, err := n.projects.Create(project.Project{ID: "team-a", Name: "Team A", Description: "web",
		Members: []project.Member{{Kind: "user", Name: "op", Role: "operator"}}, StoragePools: []string{"default"}, Networks: []string{"default"}}); err != nil {
		t.Fatal(err)
	}
	doc := loadOpenAPI(t)
	c := contractChecker{schemas: doc.Components.Schemas}
	admin := n.signIn(t, "onur")
	for _, path := range []string{"/api/v1/projects", "/api/v1/projects/team-a"} {
		resp, body := admin.req("GET", path, "")
		rec := httptest.NewRecorder()
		rec.Code = resp.StatusCode
		rec.HeaderMap = resp.Header
		rec.Body.WriteString(body)
		op := doc.Paths[strings.Replace(path, "team-a", "{id}", 1)]["get"]
		if !strings.Contains(fmt.Sprint(op["responses"]), "Project") {
			t.Fatalf("%s declares no Project answer", path)
		}
		c.checkAnswer(t, "GET "+path, op, rec, false)
	}
}

// contractChecker checks schemas and values against the JSON Schema subset the document uses.
type contractChecker struct{ schemas map[string]any }

func (c contractChecker) resolve(s map[string]any) (map[string]any, error) {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s, nil
	}
	name, ok := strings.CutPrefix(ref, "#/components/schemas/")
	target, found := c.schemas[name].(map[string]any)
	if !ok || !found {
		return nil, fmt.Errorf("$ref %q leads nowhere", ref)
	}
	return target, nil
}

func (c contractChecker) checkContent(t *testing.T, where string, content any) {
	t.Helper()
	for mt, m := range asMap(content) {
		if !strings.Contains(mt, "/") {
			t.Errorf("%s: media type %q", where, mt)
		}
		c.checkSchema(t, where, asMap(m)["schema"])
	}
}

func (c contractChecker) checkSchema(t *testing.T, where string, raw any) {
	t.Helper()
	s, ok := raw.(map[string]any)
	if !ok {
		t.Errorf("%s: a schema is an object, got %T", where, raw)
		return
	}
	s, err := c.resolve(s)
	if err != nil {
		t.Errorf("%s: %v", where, err)
		return
	}
	if typ, ok := s["type"]; ok {
		for _, ty := range typeList(typ) {
			if !slices.Contains(schemaTypes, ty) {
				t.Errorf("%s: type %q", where, ty)
			}
		}
	}
	for name, p := range asMap(s["properties"]) {
		c.checkSchema(t, where+"."+name, p)
	}
	if items, ok := s["items"]; ok {
		c.checkSchema(t, where+"[]", items)
	}
}

func (c contractChecker) checkAnswer(t *testing.T, where string, op map[string]any, rec *httptest.ResponseRecorder, notJSON bool) {
	t.Helper()
	responses := asMap(op["responses"])
	ok := rec.Code >= 200 && rec.Code < 300
	switch {
	case rec.Code >= 300 && rec.Code < 400, rec.Code == http.StatusNoContent, rec.Body.Len() == 0:
		return
	case notJSON && ok:
		return
	case !strings.Contains(rec.Header().Get("Content-Type"), "application/json"):
		t.Errorf("%s: %d answered %q, not JSON", where, rec.Code, rec.Header().Get("Content-Type"))
		return
	}
	var value any
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Errorf("%s: not valid JSON: %v", where, err)
		return
	}
	key := "default"
	if ok {
		key = "2XX"
	}
	schema := asMap(asMap(asMap(asMap(responses[key])["content"])["application/json"])["schema"])
	if len(schema) == 0 {
		return // the document does not describe this answer's shape
	}
	for _, e := range c.validate(value, schema, "") {
		t.Errorf("%s (%d): %s", where, rec.Code, e)
	}
}

// validate checks value against schema; a property the schema does not model is an error
// unless the object declares no properties at all (a free-form object).
func (c contractChecker) validate(value any, raw map[string]any, at string) []string {
	s, err := c.resolve(raw)
	if err != nil {
		return []string{at + ": " + err.Error()}
	}
	if at == "" {
		at = "$"
	}
	if typ, ok := s["type"]; ok {
		types := typeList(typ)
		if !slices.ContainsFunc(types, func(ty string) bool { return matchesType(value, ty) }) {
			return []string{fmt.Sprintf("%s: %T is not %v", at, value, types)}
		}
	}
	var errs []string
	switch v := value.(type) {
	case map[string]any:
		props := asMap(s["properties"])
		for k, pv := range v {
			ps, modeled := props[k]
			if !modeled {
				if len(props) > 0 {
					errs = append(errs, fmt.Sprintf("%s.%s: not in the schema", at, k))
				}
				continue
			}
			errs = append(errs, c.validate(pv, asMap(ps), at+"."+k)...)
		}
		for _, r := range anyList(s["required"]) {
			if _, ok := v[r.(string)]; !ok {
				errs = append(errs, fmt.Sprintf("%s.%s: required", at, r))
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range v {
				errs = append(errs, c.validate(e, items, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return errs
}

func matchesType(v any, ty string) bool {
	switch ty {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return false
}

func typeList(t any) []string {
	if s, ok := t.(string); ok {
		return []string{s}
	}
	var out []string
	for _, e := range anyList(t) {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func TestContractCheckerCatchesDrift(t *testing.T) {
	c := contractChecker{schemas: map[string]any{"P": map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"},
	}, "required": []any{"id"}}}}
	ref := map[string]any{"$ref": "#/components/schemas/P"}
	for _, tc := range []struct {
		json string
		errs int
	}{
		{`{"id":"a","n":2}`, 0},
		{`{"id":"a","extra":1}`, 1}, // an unmodeled field
		{`{"n":2.5}`, 2},            // not an integer, id missing
		{`{"id":3}`, 1},
		{`[1]`, 1},
	} {
		var v any
		json.Unmarshal([]byte(tc.json), &v)
		if got := c.validate(v, ref, ""); len(got) != tc.errs {
			t.Errorf("%s: %v, want %d errors", tc.json, got, tc.errs)
		}
	}
	if _, err := c.resolve(map[string]any{"$ref": "#/components/schemas/Nope"}); err == nil {
		t.Error("a dangling $ref resolved")
	}
}
