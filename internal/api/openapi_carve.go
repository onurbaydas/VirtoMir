package api

import "strings"

// OpenAPI for pools carved from free space (an empty disk, a volume group, a zpool) and for
// network storage found and connected by VirtoMir (storconn.go), and for the datastore
// browser (files.go).

func init() {
	moreBodies = append(moreBodies, carveBody)
	moreSchemas = append(moreSchemas, carveSchemas)
	responseSchemas["GET /api/v1/storage/sources"] = listOf("sources", "PoolSource")
	responseSchemas["GET /api/v1/storage/pools/{pool}/files"] = listOf("entries", "FileEntry")
}

func carveBody(p string) (map[string]any, bool) {
	str := map[string]any{"type": "string"}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	switch {
	case strings.HasSuffix(p, "/files/folder"):
		return obj(map[string]any{"path": str}), true
	case strings.HasSuffix(p, "/files/rename"):
		return obj(map[string]any{"from": str, "to": str}), true
	case strings.HasSuffix(p, "/files/copy"):
		return obj(map[string]any{"path": str, "toPool": str, "toPath": str, "move": map[string]any{"type": "boolean"}}), true
	case strings.HasSuffix(p, "/files/upload"):
		return map[string]any{"type": "string", "format": "binary", "description": "the file; ?path= names it"}, true
	}
	switch p {
	case "/api/v1/storage/pools/carve":
		return schemaRef("CarveRequest"), true
	case "/api/v1/storage/discover":
		return schemaRef("DiscoverRequest"), true
	case "/api/v1/storage/connect":
		return schemaRef("ConnectRequest"), true
	}
	return nil, false
}

func carveSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	kind := map[string]any{"type": "string", "enum": []string{"disk", "vg", "zfs"}}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	boolean := map[string]any{"type": "boolean"}
	storage := map[string]any{"type": "string", "enum": []string{"iscsi", "nfs", "smb", "fc"}}
	return map[string]any{
		"DiscoverRequest": obj(map[string]any{"server": map[string]any{"type": "string", "description": "ask only this server (empty: scan this host's networks)"},
			"username": str, "password": str}),
		"FoundStorage": obj(map[string]any{"kind": storage, "server": str, "host": str, "name": str,
			"portals": map[string]any{"type": "array", "items": str}, "sizeBytes": integer, "detail": str,
			"needsAuth": boolean, "error": str, "pool": str}),
		"ConnectRequest": obj(map[string]any{"kind": storage, "server": str, "host": str,
			"name":     map[string]any{"type": "string", "description": "target IQN, export path or share"},
			"username": str, "password": str, "everywhere": boolean, "pool": str}),
		"FileEntry": obj(map[string]any{"name": str, "dir": boolean, "sizeBytes": integer, "allocatedBytes": integer,
			"modified": map[string]any{"type": "string", "format": "date-time"}, "format": str,
			"usedBy": map[string]any{"type": "array", "items": str}}),
		"PoolSource": obj(map[string]any{"kind": kind, "id": str, "sizeBytes": integer, "freeBytes": integer, "detail": str}),
		"CarveRequest": obj(map[string]any{"name": str, "kind": kind,
			"source":    map[string]any{"type": "string", "description": "disk path, volume group or zpool (a PoolSource id)"},
			"sizeBytes": integer}),
	}
}
