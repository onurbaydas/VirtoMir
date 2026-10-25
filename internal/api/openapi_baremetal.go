package api

import "strings"

// OpenAPI for bare-metal onboarding (ADR-0077).

func init() {
	moreBodies = append(moreBodies, baremetalBody)
	moreSchemas = append(moreSchemas, baremetalSchemas)
	responseSchemas["GET /api/v1/baremetal"] = map[string]any{"type": "object", "properties": map[string]any{
		"enabled":  map[string]any{"type": "boolean"},
		"settings": schemaRef("NetbootSettings"),
		"hosts":    map[string]any{"type": "array", "items": schemaRef("BaremetalHost")},
		"status": map[string]any{"type": "object", "properties": map[string]any{
			"installerReady": map[string]any{"type": "boolean"}, "listening": map[string]any{"type": "boolean"},
			"error": map[string]any{"type": "string"}, "ipmitool": map[string]any{"type": "boolean"},
		}},
	}}
}

func baremetalBody(p string) (map[string]any, bool) {
	switch {
	case !strings.HasPrefix(p, "/api/v1/baremetal/"):
		return nil, false
	case p == "/api/v1/baremetal/settings":
		return schemaRef("NetbootSettings"), true
	case strings.HasSuffix(p, "/power"):
		return map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"status", "off", "reboot", "pxe"}}}}, true
	case strings.HasPrefix(p, "/api/v1/baremetal/hosts") && !strings.HasSuffix(p, "/install") && !strings.HasSuffix(p, "/cancel"):
		return schemaRef("BaremetalHostInput"), true
	}
	return map[string]any{"type": "object"}, true
}

func baremetalSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	bmc := obj(map[string]any{"driver": str, "endpoint": str, "username": str, "password": map[string]any{"type": "string", "description": "write-only"}, "insecureTls": boolean})
	static := obj(map[string]any{"address": str, "gateway": str, "dns": strs})
	return map[string]any{
		"NetbootSettings": obj(map[string]any{"enabled": boolean, "address": str, "port": integer, "mode": str, "interface": str, "mirror": str, "repo": str}),
		"BaremetalHostInput": obj(map[string]any{"name": str, "mac": str, "bmc": bmc, "disk": str, "static": static, "sshKeys": strs,
			"password": map[string]any{"type": "string", "description": "of the user virtomir; write-only, empty keeps the stored one"}}),
		"BaremetalHost": obj(map[string]any{"id": str, "name": str, "mac": str, "bmc": bmc, "disk": str, "static": static, "sshKeys": strs,
			"state": str, "message": str, "created": str, "updated": str,
			"events": map[string]any{"type": "array", "items": obj(map[string]any{"at": str, "message": str, "error": boolean})}}),
	}
}
