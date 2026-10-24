package api

import "strings"

// moreBodies and moreSchemas let a feature's file describe its request bodies and schemas
// in the OpenAPI document (from its init); openAPI asks them all.
var (
	moreBodies  []func(path string) (map[string]any, bool)
	moreSchemas []func() map[string]any
)

// OpenAPI for hyper-converged Ceph and storage policies (ADR-0073).

func init() {
	moreBodies = append(moreBodies, cephBody)
	moreSchemas = append(moreSchemas, cephSchemas)
	responseSchemas["GET /api/v1/ceph/status"] = schemaRef("CephStatus")
	responseSchemas["GET /api/v1/ceph/disks"] = listOf("disks", "CephDisk")
	responseSchemas["GET /api/v1/ceph/hosts"] = listOf("hosts", "CephHost")
	responseSchemas["GET /api/v1/ceph/pools"] = listOf("pools", "RBDPool")
	responseSchemas["GET /api/v1/storage/policies"] = listOf("policies", "StoragePolicy")
}

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

// cephBody is the request body of a Ceph or storage policy write.
func cephBody(p string) (map[string]any, bool) {
	str := map[string]any{"type": "string"}
	switch {
	case p == "/api/v1/ceph/bootstrap":
		return schemaRef("CephBootstrap"), true
	case p == "/api/v1/ceph/pools":
		return schemaRef("RBDPoolSpec"), true
	case p == "/api/v1/ceph/hosts":
		return map[string]any{"type": "object", "properties": map[string]any{"member": str}}, true
	case p == "/api/v1/ceph/osds":
		return map[string]any{"type": "object", "properties": map[string]any{"hostname": str, "disk": str}}, true
	case p == "/api/v1/ceph/noout":
		return map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}}, true
	case strings.HasPrefix(p, "/api/v1/ceph/maintenance/"):
		return map[string]any{"type": "object", "properties": map[string]any{"hostname": str}}, true
	case strings.HasPrefix(p, "/api/v1/storage/policies"):
		return schemaRef("StoragePolicy"), true
	}
	return nil, false
}

func cephSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	number := map[string]any{"type": "number"}
	boolean := map[string]any{"type": "boolean"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{
		"CephStatus": obj(map[string]any{
			"installed": boolean, "cephadm": boolean, "managed": boolean, "error": str,
			"managedBy": obj(map[string]any{"id": str, "name": str, "fsid": str}),
			"config": obj(map[string]any{"fsid": str, "bootstrapHost": str, "monEndpoints": strs, "clientUser": str,
				"sshPublicKey": str, "maintenanceHosts": strs, "noout": boolean, "created": str, "updated": str}),
			"telemetry": map[string]any{"type": "object", "description": "health, daemons, capacity, OSD tree and placement groups"},
		}),
		"CephDisk": obj(map[string]any{"path": str, "name": str, "sizeBytes": integer, "rotational": boolean, "deviceType": str,
			"mountPoint": str, "fsType": str, "partitions": strs, "empty": boolean, "refusalReason": str}),
		"CephHost":      obj(map[string]any{"hostname": str, "ip": str, "labels": strs, "status": str, "memberId": str}),
		"CephBootstrap": obj(map[string]any{"monIp": str, "clusterNetwork": str, "initialOsdDisk": str, "singleHostDefaults": boolean}),
		"RBDPoolSpec": obj(map[string]any{"name": str, "type": str, "replicaSize": integer, "ecK": integer, "ecM": integer,
			"deviceClass": str, "pgNum": integer, "libvirtPool": map[string]any{"type": "string", "description": "a libvirt pool to create for it on every member"}}),
		"RBDPool": obj(map[string]any{"id": integer, "name": str, "type": str, "size": integer, "minSize": integer, "ecProfile": str,
			"dataPool": str, "storedBytes": integer, "percentUsed": number, "maxAvailBytes": integer, "libvirtPool": str,
			"compressionMode": str, "compressionAlgorithm": str, "compressedBytes": integer, "compressedFromBytes": integer}),
		"StoragePolicy": obj(map[string]any{"id": str, "name": str, "description": str, "pool": str, "default": boolean,
			"created": str, "updated": str}),
	}
}
