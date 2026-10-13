package virt

import (
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// HostCPU is this host's CPU as libvirt describes it (<cpu> from the capabilities), the
// input for finding a model every cluster member can run.
type HostCPU struct {
	XML      string   `json:"xml"`
	Model    string   `json:"model"`
	Vendor   string   `json:"vendor"`
	Features []string `json:"features"`
}

func (c *Client) HostCPU() (HostCPU, error) {
	l, err := c.conn()
	if err != nil {
		return HostCPU{}, err
	}
	raw, err := l.ConnectGetCapabilities()
	if err != nil {
		return HostCPU{}, classify(err)
	}
	var caps libvirtxml.Caps
	if err := caps.Unmarshal(raw); err != nil {
		return HostCPU{}, err
	}
	cpu := caps.Host.CPU
	if cpu == nil {
		return HostCPU{}, refusedf("libvirt does not describe this host's CPU.")
	}
	// Only what CPU comparison needs (no topology, cache, counters).
	slim := libvirtxml.CapsHostCPU{Arch: cpu.Arch, Model: cpu.Model, Vendor: cpu.Vendor, FeatureFlags: cpu.FeatureFlags}
	doc, err := slim.Marshal()
	if err != nil {
		return HostCPU{}, err
	}
	h := HostCPU{XML: doc, Model: cpu.Model, Vendor: cpu.Vendor, Features: []string{}}
	for _, f := range cpu.FeatureFlags {
		h.Features = append(h.Features, f.Name)
	}
	slices.Sort(h.Features)
	return h, nil
}

// SameCPU reports whether two hosts have the same CPU (model, vendor and features), the
// condition for live-migrating a machine whose CPU is "same as host" (host-passthrough).
func SameCPU(a, b HostCPU) bool {
	return a.Model == b.Model && a.Vendor == b.Vendor && slices.Equal(a.Features, b.Features)
}

// BaselineCPU is the newest named CPU model every given host supports.
func (c *Client) BaselineCPU(cpus []HostCPU) (string, error) {
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	xmls := make([]string, len(cpus))
	for i, h := range cpus {
		xmls[i] = h.XML
	}
	out, err := l.ConnectBaselineHypervisorCPU(nil, nil, nil, []string{"kvm"}, xmls, 0)
	if err != nil {
		return "", classify(err)
	}
	var cpu libvirtxml.DomainCPU
	if err := cpu.Unmarshal(out); err != nil {
		return "", err
	}
	if cpu.Model == nil || strings.TrimSpace(cpu.Model.Value) == "" {
		return "", refusedf("libvirt found no CPU model all hosts support.")
	}
	return cpu.Model.Value, nil
}
