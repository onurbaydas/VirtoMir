package sdn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// HostStatus is what this host has of OVN/OVS.
type HostStatus struct {
	Installed         bool   `json:"installed"`
	OVSVersion        string `json:"ovsVersion,omitempty"`
	OVNVersion        string `json:"ovnVersion,omitempty"`
	ServiceOVS        bool   `json:"serviceOvs"`
	ServiceOVNCentral bool   `json:"serviceOvnCentral"`
	ServiceOVNHost    bool   `json:"serviceOvnHost"`
	IntegrationBridge string `json:"integrationBridge"`
	SystemID          string `json:"systemId,omitempty"`
	EncapsulationType string `json:"encapsulationType,omitempty"`
	EncapsulationIP   string `json:"encapsulationIp,omitempty"`
	// Role is "central" (runs the OVN databases) or "controller" (only ovn-controller).
	Role            string `json:"role"`
	CentralEndpoint string `json:"centralEndpoint,omitempty"`
}

// ChassisInfo is one OVN chassis (a hypervisor in the overlay).
type ChassisInfo struct {
	ID                string `json:"id"`
	Hostname          string `json:"hostname"`
	EncapsulationIP   string `json:"encapsulationIp"`
	EncapsulationType string `json:"encapsulationType"`
}

// IntegrationBridge is the bridge VM ports are plugged into.
const IntegrationBridge = "br-int"

// firstLine is the first line of out, trimmed.
func firstLine(out []byte) string {
	l, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(l)
}

// version is the last word of a --version line: "ovs-vsctl (Open vSwitch) 3.3.0" → 3.3.0.
func version(out []byte) string {
	f := strings.Fields(firstLine(out))
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// ovsGet reads external_ids:key of the Open_vSwitch row ("" when unset).
func ovsGet(ctx context.Context, r Runner, key string) string {
	out, err := r.Run(ctx, "ovs-vsctl", "--if-exists", "get", "Open_vSwitch", ".", "external_ids:"+key)
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(out)), `"`)
}

// active asks systemd whether one of the units runs (Debian and Fedora name them apart).
func active(ctx context.Context, r Runner, units ...string) bool {
	for _, u := range units {
		if out, err := r.Run(ctx, "systemctl", "is-active", u); err == nil && strings.TrimSpace(string(out)) == "active" {
			return true
		}
	}
	return false
}

// DetectHost reports this host's OVN/OVS. Without ovs-vsctl it is simply not installed.
func DetectHost(ctx context.Context, r Runner) (HostStatus, error) {
	st := HostStatus{IntegrationBridge: IntegrationBridge, Role: "controller"}
	out, err := r.Run(ctx, "ovs-vsctl", "--version")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return st, nil
		}
		return st, err
	}
	st.OVSVersion = version(out)
	if out, err := r.Run(ctx, "ovn-nbctl", "--version"); err == nil {
		st.OVNVersion = version(out)
	}
	st.Installed = st.OVNVersion != ""
	st.ServiceOVS = active(ctx, r, "openvswitch-switch", "openvswitch")
	st.ServiceOVNCentral = active(ctx, r, "ovn-central", "ovn-northd")
	st.ServiceOVNHost = active(ctx, r, "ovn-host", "ovn-controller")
	if _, err := r.Run(ctx, "ovs-vsctl", "br-exists", IntegrationBridge); err != nil {
		st.IntegrationBridge = ""
	}
	st.SystemID = ovsGet(ctx, r, "system-id")
	st.EncapsulationType = ovsGet(ctx, r, "ovn-encap-type")
	st.EncapsulationIP = ovsGet(ctx, r, "ovn-encap-ip")
	st.CentralEndpoint = ovsGet(ctx, r, "ovn-remote")
	if st.ServiceOVNCentral {
		st.Role = "central"
	}
	return st, nil
}

// EnsureIntegrationBridge creates the integration bridge if it is missing.
func EnsureIntegrationBridge(ctx context.Context, r Runner, bridge string) error {
	_, err := r.Run(ctx, "ovs-vsctl", "--may-exist", "add-br", bridge)
	return err
}

// ovsdbTable is the JSON of "ovn-sbctl --format=json list <table>".
type ovsdbTable struct {
	Headings []string            `json:"headings"`
	Data     [][]json.RawMessage `json:"data"`
}

// atom reads an OVSDB value as a string: a plain string, or the first of ["set", [...]].
func atom(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var tagged []json.RawMessage
	if json.Unmarshal(raw, &tagged) == nil && len(tagged) == 2 {
		var tag string
		_ = json.Unmarshal(tagged[0], &tag)
		switch tag {
		case "uuid":
			_ = json.Unmarshal(tagged[1], &s)
			return s
		case "set":
			var items []json.RawMessage
			if json.Unmarshal(tagged[1], &items) == nil && len(items) > 0 {
				return atom(items[0])
			}
		}
	}
	return ""
}

func (t ovsdbTable) rows() []map[string]string {
	var out []map[string]string
	for _, row := range t.Data {
		m := map[string]string{}
		for i, h := range t.Headings {
			if i < len(row) {
				m[h] = atom(row[i])
			}
		}
		out = append(out, m)
	}
	return out
}

func sbList(ctx context.Context, r Runner, table, columns string) ([]map[string]string, error) {
	out, err := r.Run(ctx, "ovn-sbctl", "--format=json", "--columns="+columns, "list", table)
	if err != nil {
		return nil, err
	}
	var t ovsdbTable
	if err := json.Unmarshal(out, &t); err != nil {
		return nil, fmt.Errorf("read ovn-sbctl %s: %w", table, err)
	}
	return t.rows(), nil
}

// ListChassis lists the overlay's chassis with their tunnel endpoint (Encap table).
func ListChassis(ctx context.Context, r Runner) ([]ChassisInfo, error) {
	chassis, err := sbList(ctx, r, "Chassis", "name,hostname")
	if err != nil {
		return nil, err
	}
	encaps, err := sbList(ctx, r, "Encap", "chassis_name,ip,type")
	if err != nil {
		return nil, err
	}
	out := make([]ChassisInfo, 0, len(chassis))
	for _, c := range chassis {
		ci := ChassisInfo{ID: c["name"], Hostname: c["hostname"]}
		for _, e := range encaps {
			if e["chassis_name"] == ci.ID && (ci.EncapsulationIP == "" || e["type"] == "geneve") {
				ci.EncapsulationIP, ci.EncapsulationType = e["ip"], e["type"]
			}
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out, nil
}
