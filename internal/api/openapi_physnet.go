package api

import "strings"

// OpenAPI for the physical network: SR-IOV, BGP, IP pools and dynamic DNS (ADR-0075).

func init() {
	moreBodies = append(moreBodies, physnetBody)
	moreSchemas = append(moreSchemas, physnetSchemas)
	responseSchemas["GET /api/v1/physnet/sriov"] = listOf("devices", "SRIOVDevice")
	responseSchemas["GET /api/v1/physnet/bgp"] = schemaRef("BGPView")
	responseSchemas["GET /api/v1/physnet/ip-pools"] = listOf("pools", "IPPool")
	responseSchemas["GET /api/v1/physnet/ip-pools/{id}"] = schemaRef("IPPool")
	responseSchemas["GET /api/v1/physnet/dns"] = map[string]any{"type": "object", "properties": map[string]any{
		"config": schemaRef("DNSConfig"), "hasSecret": map[string]any{"type": "boolean"},
	}}
}

func physnetBody(p string) (map[string]any, bool) {
	str := map[string]any{"type": "string"}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	switch {
	case !strings.HasPrefix(p, "/api/v1/physnet/"):
		return nil, false
	case strings.HasPrefix(p, "/api/v1/physnet/sriov/"):
		return obj(map[string]any{"vfs": map[string]any{"type": "integer"}, "persistent": map[string]any{"type": "boolean"}}), true
	case p == "/api/v1/physnet/bgp":
		return schemaRef("BGPConfig"), true
	case strings.HasSuffix(p, "/check"):
		return obj(map[string]any{"ip": str}), true
	case strings.HasSuffix(p, "/allocations"):
		return obj(map[string]any{"ip": str, "name": str}), true
	case strings.HasPrefix(p, "/api/v1/physnet/ip-pools"):
		return schemaRef("IPPool"), true
	case p == "/api/v1/physnet/dns":
		return schemaRef("DNSConfig"), true
	}
	return map[string]any{"type": "object"}, true
}

func physnetSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	arr := func(name string) map[string]any { return map[string]any{"type": "array", "items": schemaRef(name)} }
	return map[string]any{
		"SRIOVDevice": obj(map[string]any{"interface": str, "pci": str, "driver": str, "totalVfs": integer, "currentVfs": integer, "persistent": boolean,
			"vfs": map[string]any{"type": "array", "items": obj(map[string]any{"index": integer, "pci": str, "netdev": str, "mac": str})}}),
		"BGPNeighbor": obj(map[string]any{"address": str, "remoteAs": integer, "description": str, "password": str, "bfd": boolean}),
		"BGPConfig": obj(map[string]any{"enabled": boolean, "localAs": integer, "routerId": str, "neighbors": arr("BGPNeighbor"), "announce": strs,
			"evpn": boolean, "appliedAs": integer}),
		"BGPView": obj(map[string]any{"config": schemaRef("BGPConfig"), "error": str,
			"frr":      obj(map[string]any{"installed": boolean, "version": str, "bgpd": boolean}),
			"sessions": map[string]any{"type": "array", "items": obj(map[string]any{"address": str, "remoteAs": integer, "description": str, "state": str, "uptime": str, "prefixesReceived": integer, "prefixesSent": integer, "families": strs})}}),
		"IPAllocation": obj(map[string]any{"ip": str, "mac": str, "vmUuid": str, "vmName": str, "hostname": str, "host": str, "created": str}),
		"IPPool": obj(map[string]any{"id": str, "name": str, "description": str, "network": str, "cidr": str, "gateway": str, "rangeStart": str, "rangeEnd": str,
			"dnsServers": strs, "domain": str, "reserved": strs, "allocations": arr("IPAllocation"), "created": str, "updated": str}),
		"DNSConfig": obj(map[string]any{"enabled": boolean, "server": str, "zone": str, "reverseZones": strs, "keyName": str, "algorithm": str,
			"secret": map[string]any{"type": "string", "description": "write-only; empty keeps the stored one"}, "ttl": integer}),
	}
}
