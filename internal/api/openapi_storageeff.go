package api

import "strings"

// OpenAPI for storage efficiency, tiers and NVMe/TCP (ADR-0074).

func init() {
	moreBodies = append(moreBodies, storageEffBody)
	moreSchemas = append(moreSchemas, storageEffSchemas)
	responseSchemas["GET /api/v1/storage/tiers"] = map[string]any{"type": "object", "properties": map[string]any{
		"tiers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "pool name → nvme, ssd or hdd"},
	}}
	responseSchemas["GET /api/v1/storage/nvme"] = listOf("subsystems", "NVMeSubsystem")
	responseSchemas["GET /api/v1/storage/pools/{pool}/efficiency"] = schemaRef("PoolEfficiency")
}

func storageEffBody(p string) (map[string]any, bool) {
	str := map[string]any{"type": "string"}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	switch {
	case strings.HasSuffix(p, "/zfs") && strings.HasPrefix(p, "/api/v1/storage/pools/"):
		return schemaRef("ZFSSettings"), true
	case strings.HasSuffix(p, "/compression") && strings.HasPrefix(p, "/api/v1/ceph/pools/"):
		return obj(map[string]any{"mode": str, "algorithm": str}), true
	case strings.HasSuffix(p, "/tier") && strings.HasPrefix(p, "/api/v1/domains/"),
		strings.HasPrefix(p, "/api/v1/storage/tiers/"):
		return obj(map[string]any{"tier": str}), true
	case strings.HasSuffix(p, "/sparsify"):
		return map[string]any{"type": "object", "description": "no body"}, true
	case p == "/api/v1/storage/nvme/discover":
		return obj(map[string]any{"portal": str, "port": map[string]any{"type": "integer"}}), true
	case p == "/api/v1/storage/nvme/connect":
		return schemaRef("NVMeTarget"), true
	case p == "/api/v1/storage/nvme/disconnect":
		return obj(map[string]any{"nqn": str}), true
	}
	return nil, false
}

func storageEffSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	number := map[string]any{"type": "number"}
	boolean := map[string]any{"type": "boolean"}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{
		"ZFSEfficiency": obj(map[string]any{"dataset": str, "compression": str, "compressRatio": number, "dedup": str, "dedupRatio": number,
			"usedBytes": integer, "logicalUsedBytes": integer, "savedBytes": integer}),
		"ZFSSettings":    obj(map[string]any{"compression": str, "dedup": str, "acknowledgeDedupRam": boolean}),
		"PoolEfficiency": obj(map[string]any{"pool": str, "kind": str, "zfs": schemaRef("ZFSEfficiency"), "ceph": schemaRef("RBDPool"), "note": str}),
		"NVMeTarget":     obj(map[string]any{"portal": str, "port": integer, "nqn": str}),
		"NVMeSubsystem": obj(map[string]any{"nqn": str, "transport": str, "address": str, "state": str, "managed": boolean,
			"namespaces": map[string]any{"type": "array", "items": obj(map[string]any{"device": str, "nsid": integer, "sizeBytes": integer, "model": str, "serial": str})}}),
	}
}
