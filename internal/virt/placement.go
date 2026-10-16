package virt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// Placement is where a running machine's vCPUs sit on the host right now, next to where
// they are allowed to sit (P5: the topology view overlays this on the host's CPUs).
type Placement struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	// VCPUs has one entry per vCPU, in order.
	VCPUs []VCPUPlacement `json:"vcpus"`
	// NUMANodes is the memory node set from the machine's NUMA policy ("" = no policy).
	NUMANodes string `json:"numaNodes,omitempty"`
}

type VCPUPlacement struct {
	// CPU is the host CPU the vCPU last ran on (-1 = unknown).
	CPU int `json:"cpu"`
	// Pinned is the host CPU set it is pinned to ("" = may run anywhere), PinnedCPUs the same
	// spelled out.
	Pinned     string `json:"pinned,omitempty"`
	PinnedCPUs []int  `json:"pinnedCpus,omitempty"`
}

// Placements lists the running machines' vCPU placement. A machine that stops between
// listing and asking is left out.
func (c *Client) Placements() ([]Placement, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	out := make([]Placement, 0, len(doms))
	for _, d := range doms {
		x, err := c.parseDomain(l, d, false)
		if err != nil {
			continue
		}
		n := uint(0)
		if x.VCPU != nil {
			n = x.VCPU.Value
		}
		t := tuningFromXML(x, n)
		p := Placement{UUID: formatUUID(d.UUID), Name: d.Name, NUMANodes: t.NUMANodes, VCPUs: make([]VCPUPlacement, n)}
		for i := range p.VCPUs {
			p.VCPUs[i] = VCPUPlacement{CPU: -1, Pinned: t.Pinning[i], PinnedCPUs: expandCPUSet(t.Pinning[i])}
		}
		if info, _, err := l.DomainGetVcpus(d, int32(n), 0); err == nil {
			for _, v := range info {
				if int(v.Number) < len(p.VCPUs) {
					p.VCPUs[v.Number].CPU = int(v.CPU)
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// expandCPUSet turns a libvirt CPU set ("0-3,^2,8") into its CPUs, in order.
func expandCPUSet(set string) []int {
	on := map[int]bool{}
	max := -1
	for _, part := range strings.Split(set, ",") {
		neg := strings.HasPrefix(part, "^")
		part = strings.TrimPrefix(part, "^")
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		for i := a; i <= b && i < 1<<16; i++ {
			on[i] = !neg
			if i > max {
				max = i
			}
		}
	}
	var out []int
	for i := 0; i <= max; i++ {
		if on[i] {
			out = append(out, i)
		}
	}
	return out
}
