package virt

import (
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
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
	// CPUWeight is the machine's share of CPU time against other machines when the host is
	// busy (libvirt <shares>; 0 = default). CPULimit caps each vCPU at that percent of one
	// host CPU (0 = no cap), even when the host is idle.
	CPUWeight uint `json:"cpuWeight"`
	CPULimit  uint `json:"cpuLimit"`
}

// cpuPeriod is the scheduling period (µs) CPULimit's quota is measured against.
const cpuPeriod = 100000

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
		if x.CPUTune.Shares != nil {
			t.CPUWeight = x.CPUTune.Shares.Value
		}
		if p, q := x.CPUTune.Period, x.CPUTune.Quota; p != nil && q != nil && p.Value > 0 && q.Value > 0 {
			t.CPULimit = uint(uint64(q.Value) * 100 / p.Value)
		}
	}
	t.HugePages = x.MemoryBacking != nil && x.MemoryBacking.MemoryHugePages != nil
	if x.NUMATune != nil && x.NUMATune.Memory != nil {
		t.NUMAMode, t.NUMANodes = x.NUMATune.Memory.Mode, x.NUMATune.Memory.Nodeset
	}
	return t
}

// schedParams are the live scheduler settings for t: no weight is the default 1024, no
// limit an unlimited quota.
func schedParams(t Tuning) []libvirt.TypedParam {
	weight := uint64(t.CPUWeight)
	if weight == 0 {
		weight = 1024
	}
	quota := int64(-1)
	if t.CPULimit > 0 {
		quota = int64(t.CPULimit) * cpuPeriod / 100
	}
	return []libvirt.TypedParam{
		{Field: "cpu_shares", Value: *libvirt.NewTypedParamValueUllong(weight)},
		{Field: "vcpu_period", Value: *libvirt.NewTypedParamValueUllong(cpuPeriod)},
		{Field: "vcpu_quota", Value: *libvirt.NewTypedParamValueLlong(quota)},
	}
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
	if t.CPUWeight != 0 && (t.CPUWeight < 2 || t.CPUWeight > 262144) {
		return invalidf("CPU weight must be between 2 and 262144 (1024 is normal), or 0 for the default.")
	}
	if t.CPULimit > 100 {
		return invalidf("The CPU limit is a percentage of one host CPU per vCPU: 1 to 100, or 0 for no limit.")
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
	if t.CPUWeight != old.CPUWeight || t.CPULimit != old.CPULimit {
		ct := orNew(x.CPUTune)
		ct.Shares, ct.Period, ct.Quota = nil, nil, nil
		if t.CPUWeight > 0 {
			ct.Shares = &libvirtxml.DomainCPUTuneShares{Value: t.CPUWeight}
		}
		if t.CPULimit > 0 {
			ct.Period = &libvirtxml.DomainCPUTunePeriod{Value: cpuPeriod}
			ct.Quota = &libvirtxml.DomainCPUTuneQuota{Value: int64(t.CPULimit) * cpuPeriod / 100}
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
