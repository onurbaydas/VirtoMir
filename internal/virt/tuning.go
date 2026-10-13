package virt

import (
	"reflect"
	"regexp"
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// Tuning is the performance part of the settings (P4): which host CPUs each vCPU and
// QEMU's own threads may run on, huge pages, and which NUMA nodes memory comes from.
type Tuning struct {
	// Pinning has one entry per vCPU (index = vCPU): a host CPU set like "2", "4-5" or
	// "0,2"; "" lets the vCPU run anywhere.
	Pinning     []string `json:"pinning"`
	EmulatorPin string   `json:"emulatorPin"`
	HugePages   bool     `json:"hugePages"`
	// NUMAMode: "" (no policy), strict, preferred, interleave or restrictive; NUMANodes is
	// the host node set, e.g. "0" or "0-1".
	NUMAMode  string `json:"numaMode"`
	NUMANodes string `json:"numaNodes"`
}

var (
	cpusetRE  = regexp.MustCompile(`^\^?[0-9]+(-[0-9]+)?(,\^?[0-9]+(-[0-9]+)?)*$`)
	numaModes = []string{"", "strict", "preferred", "interleave", "restrictive"}
)

func tuningFromXML(x *libvirtxml.Domain, vcpus uint) Tuning {
	t := Tuning{Pinning: make([]string, vcpus)}
	if x.CPUTune != nil {
		for _, p := range x.CPUTune.VCPUPin {
			if p.VCPU < vcpus {
				t.Pinning[p.VCPU] = p.CPUSet
			}
		}
		if x.CPUTune.EmulatorPin != nil {
			t.EmulatorPin = x.CPUTune.EmulatorPin.CPUSet
		}
	}
	t.HugePages = x.MemoryBacking != nil && x.MemoryBacking.MemoryHugePages != nil
	if x.NUMATune != nil && x.NUMATune.Memory != nil {
		t.NUMAMode, t.NUMANodes = x.NUMATune.Memory.Mode, x.NUMATune.Memory.Nodeset
	}
	return t
}

func validTuning(t Tuning, vcpus uint) error {
	if uint(len(t.Pinning)) > vcpus {
		return invalidf("Pinning lists %d vCPUs, but the machine has %d.", len(t.Pinning), vcpus)
	}
	for i, p := range t.Pinning {
		if p != "" && !cpusetRE.MatchString(p) {
			return invalidf("vCPU %d: %q is not a CPU set; use numbers and ranges like 2, 4-5 or 0,2.", i, p)
		}
	}
	if t.EmulatorPin != "" && !cpusetRE.MatchString(t.EmulatorPin) {
		return invalidf("Emulator threads: %q is not a CPU set.", t.EmulatorPin)
	}
	if !slices.Contains(numaModes, t.NUMAMode) {
		return invalidf("Unknown NUMA memory mode %q.", t.NUMAMode)
	}
	if t.NUMAMode != "" && !cpusetRE.MatchString(t.NUMANodes) {
		return invalidf("Name the host NUMA nodes memory may come from, e.g. 0 or 0-1.")
	}
	return nil
}

// applyTuning writes t into x, touching only what differs from old so other <cputune>,
// <memoryBacking> and <numatune> settings survive.
func applyTuning(x *libvirtxml.Domain, t, old Tuning) {
	if !slices.Equal(t.Pinning, old.Pinning) || t.EmulatorPin != old.EmulatorPin {
		ct := orNew(x.CPUTune)
		ct.VCPUPin = nil
		for i, p := range t.Pinning {
			if p != "" {
				ct.VCPUPin = append(ct.VCPUPin, libvirtxml.DomainCPUTuneVCPUPin{VCPU: uint(i), CPUSet: p})
			}
		}
		ct.EmulatorPin = nil
		if t.EmulatorPin != "" {
			ct.EmulatorPin = &libvirtxml.DomainCPUTuneEmulatorPin{CPUSet: t.EmulatorPin}
		}
		x.CPUTune = ct
		if reflect.DeepEqual(*ct, libvirtxml.DomainCPUTune{}) {
			x.CPUTune = nil
		}
	}
	if t.HugePages != old.HugePages {
		mb := orNew(x.MemoryBacking)
		mb.MemoryHugePages = nil
		if t.HugePages {
			mb.MemoryHugePages = &libvirtxml.DomainMemoryHugepages{}
		}
		x.MemoryBacking = mb
		if reflect.DeepEqual(*mb, libvirtxml.DomainMemoryBacking{}) {
			x.MemoryBacking = nil
		}
	}
	if t.NUMAMode != old.NUMAMode || t.NUMANodes != old.NUMANodes {
		nt := orNew(x.NUMATune)
		nt.Memory = nil
		if t.NUMAMode != "" {
			nt.Memory = &libvirtxml.DomainNUMATuneMemory{Mode: t.NUMAMode, Nodeset: strings.TrimSpace(t.NUMANodes)}
		}
		x.NUMATune = nt
		if nt.Memory == nil && len(nt.MemNodes) == 0 {
			x.NUMATune = nil
		}
	}
}
