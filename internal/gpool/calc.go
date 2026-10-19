package gpool

import (
	"strings"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// GPUResource is how much of a pool one host has.
type GPUResource struct {
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
	PoolID   string `json:"poolId"`
	Model    string `json:"model"`
	Kind     string `json:"kind"`
	Total    int    `json:"total"`
	Used     int    `json:"used"`
	Free     int    `json:"free"`
	// Reserved are free ones promised to machines being created right now.
	Reserved  int    `json:"reserved,omitempty"`
	Telemetry string `json:"telemetry,omitempty"`
}

// HostHardware is what one host reports: its GPUs (hostinfo) and its PCI devices and vGPU
// slices with the machines that hold them (libvirt).
type HostHardware struct {
	HostID   string          `json:"hostId"`
	HostName string          `json:"hostName"`
	GPUs     []hostinfo.GPU  `json:"gpus"`
	PCI      []virt.HostPCI  `json:"pci"`
	MDevs    []virt.HostMDev `json:"mdevs"`
}

// Matches reports whether a card belongs to pool p by its model.
func (p Pool) Matches(g hostinfo.GPU, pci *virt.HostPCI) bool {
	if p.Model == "" {
		return true
	}
	m := strings.ToLower(p.Model)
	if strings.Contains(strings.ToLower(g.Name), m) {
		return true
	}
	return pci != nil && strings.Contains(strings.ToLower(pci.Product), m)
}

func pciAt(list []virt.HostPCI, addr string) *virt.HostPCI {
	for i := range list {
		if strings.EqualFold(list[i].Address, addr) {
			return &list[i]
		}
	}
	return nil
}

// Availability counts a pool on one host.
//   - whole: matching cards; used when a machine holds the card, free when it can be passed
//     through (IOMMU group) and nobody holds it;
//   - mdev: slices of type Profile on matching cards: the ones that exist (used when a
//     machine holds them) plus the ones the card can still make;
//   - mig: MIG instances of profile Profile, from the card's own count.
func Availability(p Pool, h HostHardware) GPUResource {
	r := GPUResource{HostID: h.HostID, HostName: h.HostName, PoolID: p.ID, Model: p.Model, Kind: p.Kind}
	for _, g := range h.GPUs {
		pci := pciAt(h.PCI, g.Address)
		if !p.Matches(g, pci) {
			continue
		}
		if r.Telemetry == "" {
			r.Telemetry = g.Telemetry
		}
		switch p.Kind {
		case KindWhole:
			used := len(g.UsedBy) > 0 || (pci != nil && len(pci.UsedBy) > 0)
			r.Total++
			switch {
			case used:
				r.Used++
			case g.PassThroughReady:
				r.Free++
			}
		case KindMDev:
			for _, t := range g.MDevTypes {
				if t.ID == p.Profile {
					r.Free += int(t.Available)
				}
			}
			for _, m := range h.MDevs {
				if m.Type != p.Profile || !strings.EqualFold(m.Parent, g.Address) {
					continue
				}
				if len(m.UsedBy) > 0 {
					r.Used++
				} else {
					r.Free++
				}
			}
		case KindMIG:
			for _, mp := range g.MIGProfiles {
				if mp.ID == p.Profile {
					r.Free += mp.Free
					r.Used += mp.Count - mp.Free
				}
			}
		}
	}
	if p.Kind != KindWhole {
		r.Total = r.Used + r.Free
	}
	return r
}

// ClusterAvailability counts every pool on every host.
func ClusterAvailability(pools []Pool, hosts []HostHardware) map[string][]GPUResource {
	out := map[string][]GPUResource{}
	for _, p := range pools {
		res := []GPUResource{}
		for _, h := range hosts {
			if r := Availability(p, h); r.Total > 0 {
				res = append(res, r)
			}
		}
		out[p.ID] = res
	}
	return out
}
