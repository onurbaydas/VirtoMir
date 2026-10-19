package gpool

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Assignment is one GPU a machine gets: a whole card (PCI address), a vGPU slice that
// exists (MDevUUID) or one to make (Parent card + Type).
type Assignment struct {
	PCI      string `json:"pci,omitempty"`
	MDevUUID string `json:"mdevUuid,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Type     string `json:"type,omitempty"`
}

// ErrNoCapacity: the host does not have enough free GPUs of the pool.
var ErrNoCapacity = fmt.Errorf("%w: not enough free GPUs", ErrBusy)

// Allocate picks count GPUs of pool p on host h: free cards for whole pools; for vGPU
// pools, unused slices first, then new slices on the cards with the most room.
// MIG instances reach a machine only through an NVIDIA vGPU type, so a MIG pool shows
// capacity but hands out nothing; make a vGPU pool with the MIG-backed type instead.
func Allocate(p Pool, h HostHardware, count int) ([]Assignment, error) {
	if count < 1 {
		return nil, fmt.Errorf("%w: ask for one GPU or more", ErrInvalid)
	}
	if p.PerHost > 0 && count > p.PerHost {
		return nil, fmt.Errorf("%w: pool %s gives a machine at most %d GPUs", ErrInvalid, p.ID, p.PerHost)
	}
	var out []Assignment
	switch p.Kind {
	case KindWhole:
		for _, g := range h.GPUs {
			pci := pciAt(h.PCI, g.Address)
			used := len(g.UsedBy) > 0 || (pci != nil && len(pci.UsedBy) > 0)
			if len(out) < count && p.Matches(g, pci) && g.PassThroughReady && !used {
				out = append(out, Assignment{PCI: g.Address})
			}
		}
	case KindMDev:
		matching := map[string]bool{}
		type room struct {
			parent string
			free   int
		}
		var rooms []room
		for _, g := range h.GPUs {
			if !p.Matches(g, pciAt(h.PCI, g.Address)) {
				continue
			}
			matching[strings.ToLower(g.Address)] = true
			for _, t := range g.MDevTypes {
				if t.ID == p.Profile && t.Available > 0 {
					rooms = append(rooms, room{g.Address, int(t.Available)})
				}
			}
		}
		for _, m := range h.MDevs {
			if len(out) < count && m.Type == p.Profile && len(m.UsedBy) == 0 && matching[strings.ToLower(m.Parent)] {
				out = append(out, Assignment{MDevUUID: m.UUID})
			}
		}
		slices.SortStableFunc(rooms, func(a, b room) int { return cmp.Compare(b.free, a.free) })
		for _, r := range rooms {
			for n := 0; n < r.free && len(out) < count; n++ {
				out = append(out, Assignment{Parent: r.parent, Type: p.Profile})
			}
		}
	case KindMIG:
		return nil, fmt.Errorf("%w: MIG instances reach a machine through an NVIDIA vGPU type; make a vGPU (mdev) pool with the MIG-backed type", ErrInvalid)
	}
	if len(out) < count {
		return nil, fmt.Errorf("%w: %s has %d free GPUs of pool %s, the machine asks for %d", ErrNoCapacity, cmp.Or(h.HostName, "this host"), len(out), p.ID, count)
	}
	return out, nil
}

// Candidate is a host that can give a machine its GPUs.
type Candidate struct {
	HostID    string `json:"hostId"`
	HostName  string `json:"hostName"`
	Available int    `json:"available"` // free minus reserved
}

// Candidates lists the hosts with count free GPUs of pool p (after reservations), the one
// with the most first; reserved(host) is how many are promised already.
func Candidates(p Pool, hosts []HostHardware, count int, reserved func(host string) int) []Candidate {
	var out []Candidate
	for _, h := range hosts {
		r := Availability(p, h)
		free := r.Free - reserved(h.HostID)
		if p.Kind == KindMIG || free < count || (p.PerHost > 0 && count > p.PerHost) {
			continue
		}
		out = append(out, Candidate{HostID: h.HostID, HostName: h.HostName, Available: free})
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Available, a.Available), strings.Compare(a.HostName, b.HostName))
	})
	return out
}
