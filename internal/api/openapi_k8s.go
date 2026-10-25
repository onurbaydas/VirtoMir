package api

import "strings"

// OpenAPI for Kubernetes clusters (ADR-0076) and running programs inside machines.

func init() {
	moreBodies = append(moreBodies, k8sBody)
	moreSchemas = append(moreSchemas, k8sSchemas)
	responseSchemas["GET /api/v1/k8s/clusters"] = map[string]any{"type": "object", "properties": map[string]any{
		"enabled":        map[string]any{"type": "boolean"},
		"clusters":       map[string]any{"type": "array", "items": schemaRef("KubernetesCluster")},
		"versions":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"defaultVersion": map[string]any{"type": "string"},
		"cnis":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}}
	responseSchemas["GET /api/v1/k8s/clusters/{id}"] = schemaRef("KubernetesCluster")
	responseSchemas["GET /api/v1/k8s/clusters/{id}/kubeconfig"] = map[string]any{"type": "string", "description": "an admin kubeconfig (YAML) with a new client certificate"}
	responseSchemas["POST /api/v1/domains/{uuid}/guest/exec"] = schemaRef("GuestExecResult")
}

func k8sBody(p string) (map[string]any, bool) {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	switch {
	case strings.HasSuffix(p, "/guest/exec"):
		return obj(map[string]any{"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}), true
	case !strings.HasPrefix(p, "/api/v1/k8s/"):
		return nil, false
	case p == "/api/v1/k8s/clusters":
		return schemaRef("KubernetesSpec"), true
	case strings.HasSuffix(p, "/scale"):
		return obj(map[string]any{"workers": map[string]any{"type": "integer"}}), true
	}
	return map[string]any{"type": "object"}, true
}

func k8sSchemas() map[string]any {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	strs := map[string]any{"type": "array", "items": str}
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	size := obj(map[string]any{"vcpus": integer, "memoryMiB": integer, "diskGiB": integer})
	return map[string]any{
		"KubernetesSpec": obj(map[string]any{"name": str, "project": str, "version": str, "controlPlanes": integer, "workers": integer,
			"controlPlane": size, "worker": size, "image": str, "storagePool": str, "network": str, "ipPool": str, "vip": str,
			"cni": str, "podCidr": str, "serviceCidr": str, "sshKeys": strs, "hosts": strs}),
		"KubernetesNode": obj(map[string]any{"name": str, "role": str, "hostId": str, "uuid": str, "ip": str, "state": str, "version": str, "message": str}),
		"KubernetesCluster": obj(map[string]any{"id": str, "spec": schemaRef("KubernetesSpec"), "state": str, "message": str, "version": str,
			"upgradeTo": str, "endpoint": str, "reservedVip": str, "ruleId": str,
			"nodes":     map[string]any{"type": "array", "items": schemaRef("KubernetesNode")},
			"events":    map[string]any{"type": "array", "items": obj(map[string]any{"at": str, "message": str, "error": boolean})},
			"createdBy": str, "createdByRole": str, "created": str, "updated": str}),
		"GuestExecResult": obj(map[string]any{"exitCode": integer, "stdout": str, "stderr": str}),
	}
}
