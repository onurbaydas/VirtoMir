package api

// OpenAPI for the three lists every client starts with: machines, networks and storage pools.

func init() {
	moreSchemas = append(moreSchemas, listSchemas)
	responseSchemas["GET /api/v1/domains"] = listOf("domains", "Domain")
	responseSchemas["GET /api/v1/networks"] = listOf("networks", "VNetwork")
	responseSchemas["GET /api/v1/storage/pools"] = listOf("pools", "Pool")
}

func listSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{
		"Domain": obj(map[string]any{
			"uuid": str, "name": str,
			"id":           map[string]any{"type": "integer", "description": "libvirt's ID, only while it runs"},
			"state":        map[string]any{"type": "string", "enum": []string{"running", "blocked", "paused", "shutdown", "shutoff", "crashed", "pmsuspended", "nostate"}},
			"vcpus":        integer,
			"memoryKiB":    integer,
			"maxMemoryKiB": integer,
			"cpuTimeNs":    integer,
			"persistent":   boolean, "autostart": boolean,
			"template":   map[string]any{"type": "boolean", "description": "never starts; new machines are made from it"},
			"linkedFrom": map[string]any{"type": "string", "description": "UUID of the template its disks are overlays on"},
			"owner":      map[string]any{"type": "string", "description": "who created it through VirtoMir"},
			"project":    map[string]any{"type": "string", "description": "its project (empty: global)"},
			"kind":       map[string]any{"type": "string", "enum": []string{"vm", "container"}},
			"gpuPool":    str, "gpuCount": integer,
		}),
		"VNetwork": obj(map[string]any{
			"name": str, "uuid": str, "active": boolean, "autostart": boolean, "persistent": boolean,
			"mode":      map[string]any{"type": "string", "description": "nat, route, open, isolated, bridge, macvtap modes or hostdev"},
			"bridge":    str,
			"subnet":    map[string]any{"type": "string", "description": "gateway/prefix, e.g. 192.168.122.1/24"},
			"dhcpStart": str, "dhcpEnd": str, "domain": str,
			"subnet6": str, "dhcp6Start": str, "dhcp6End": str,
			"usedBy":    map[string]any{"type": "array", "items": str, "description": "names of the machines on it"},
			"dhcpHosts": map[string]any{"type": "array", "items": obj(map[string]any{"mac": str, "ip": str, "name": str})},
			"vni":       integer, "peers": strs, "clusterWide": boolean, "pf": str,
		}),
		"Pool": obj(map[string]any{
			"name": str, "uuid": str,
			"type":   map[string]any{"type": "string", "description": "dir, logical, netfs, iscsi, rbd, zfs, …"},
			"path":   str,
			"source": map[string]any{"type": "string", "description": "Ceph pool, volume group, zpool, host:export or target IQN"},
			"active": boolean, "autostart": boolean,
			"capacityBytes": integer, "allocationBytes": integer, "availableBytes": integer,
			"volumes": integer,
			"origin":  map[string]any{"type": "string", "description": "how VirtoMir made it (carved, smb, …), if it did"},
		}),
	}
}
