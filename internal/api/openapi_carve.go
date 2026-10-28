package api

// OpenAPI for pools carved from free space (an empty disk, a volume group, a zpool).

func init() {
	moreBodies = append(moreBodies, carveBody)
	moreSchemas = append(moreSchemas, carveSchemas)
	responseSchemas["GET /api/v1/storage/sources"] = listOf("sources", "PoolSource")
}

func carveBody(p string) (map[string]any, bool) {
	if p == "/api/v1/storage/pools/carve" {
		return schemaRef("CarveRequest"), true
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
	return map[string]any{
		"PoolSource": obj(map[string]any{"kind": kind, "id": str, "sizeBytes": integer, "freeBytes": integer, "detail": str}),
		"CarveRequest": obj(map[string]any{"name": str, "kind": kind,
			"source":    map[string]any{"type": "string", "description": "disk path, volume group or zpool (a PoolSource id)"},
			"sizeBytes": integer}),
	}
}
