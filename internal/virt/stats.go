package virt

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// DomainStats are the cumulative counters of one running domain from libvirt's bulk stats
// (virConnectGetAllDomainStats). Rates are derived from two samples by internal/live.
type DomainStats struct {
	UUID       string
	CPUTimeNs  uint64
	VCPUTimeNs []uint64 // index = vCPU number; only online vCPUs have a value
	Balloon    BalloonStats
	Disks      []DiskCounters
	Nets       []NetCounters
}

// BalloonStats in KiB. Guest-reported values (Usable, Available) need the balloon driver
// and a stats period in the guest; they are 0 otherwise.
type BalloonStats struct {
	CurrentKiB   uint64
	MaximumKiB   uint64
	RSSKiB       uint64 // host memory the QEMU process really uses
	UsableKiB    uint64 // guest: memory it could use without swapping
	AvailableKiB uint64 // guest: total memory it sees
}

type DiskCounters struct {
	Name                 string // target dev, e.g. vda
	RdBytes, WrBytes     uint64
	RdReqs, WrReqs       uint64
	RdTimesNs, WrTimesNs uint64
	// Timed: libvirt reported rd.times and wr.times, so latency can be computed.
	Timed bool
}

type NetCounters struct {
	Name             string // host-side interface, e.g. vnet2
	RxBytes, TxBytes uint64
	RxPkts, TxPkts   uint64
	RxErrs, TxErrs   uint64
	RxDrop, TxDrop   uint64
}

const statsTypes = libvirt.DomainStatsCPUTotal | libvirt.DomainStatsBalloon | libvirt.DomainStatsVCPU |
	libvirt.DomainStatsInterface | libvirt.DomainStatsBlock

// DomainStats returns bulk counters for all running domains in one RPC.
// Domains busy with a job (e.g. migration) are skipped instead of blocking the sampler.
func (c *Client) DomainStats() ([]DomainStats, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	recs, err := l.ConnectGetAllDomainStats(nil, uint32(statsTypes),
		uint32(libvirt.ConnectGetAllDomainsStatsActive|libvirt.ConnectGetAllDomainsStatsNowait))
	if err != nil {
		return nil, fmt.Errorf("domain stats: %w", err)
	}
	out := make([]DomainStats, 0, len(recs))
	for _, r := range recs {
		out = append(out, parseStats(formatUUID(r.Dom.UUID), r.Params))
	}
	return out, nil
}

// parseStats maps libvirt's flat "group.index.field" parameters onto DomainStats.
// Unknown fields are ignored, so newer libvirt versions keep working.
func parseStats(uuid string, params []libvirt.TypedParam) DomainStats {
	s := DomainStats{UUID: uuid}
	disks := map[int]*DiskCounters{}
	timed := map[int]int{} // disk index -> number of *.times fields seen
	nets := map[int]*NetCounters{}
	maxDisk, maxNet := -1, -1

	for _, p := range params {
		group, rest, _ := strings.Cut(p.Field, ".")
		switch group {
		case "cpu":
			if rest == "time" {
				s.CPUTimeNs = paramUint(p.Value)
			}
		case "balloon":
			v := paramUint(p.Value)
			switch rest {
			case "current":
				s.Balloon.CurrentKiB = v
			case "maximum":
				s.Balloon.MaximumKiB = v
			case "rss":
				s.Balloon.RSSKiB = v
			case "usable":
				s.Balloon.UsableKiB = v
			case "available":
				s.Balloon.AvailableKiB = v
			}
		case "vcpu":
			idx, field, ok := indexed(rest)
			if !ok || field != "time" {
				continue
			}
			for len(s.VCPUTimeNs) <= idx {
				s.VCPUTimeNs = append(s.VCPUTimeNs, 0)
			}
			s.VCPUTimeNs[idx] = paramUint(p.Value)
		case "block":
			idx, field, ok := indexed(rest)
			if !ok {
				continue
			}
			d := disks[idx]
			if d == nil {
				d = &DiskCounters{}
				disks[idx] = d
				maxDisk = max(maxDisk, idx)
			}
			switch field {
			case "name":
				d.Name = paramString(p.Value)
			case "rd.bytes":
				d.RdBytes = paramUint(p.Value)
			case "wr.bytes":
				d.WrBytes = paramUint(p.Value)
			case "rd.reqs":
				d.RdReqs = paramUint(p.Value)
			case "wr.reqs":
				d.WrReqs = paramUint(p.Value)
			case "rd.times":
				d.RdTimesNs = paramUint(p.Value)
				timed[idx]++
			case "wr.times":
				d.WrTimesNs = paramUint(p.Value)
				timed[idx]++
			}
		case "net":
			idx, field, ok := indexed(rest)
			if !ok {
				continue
			}
			n := nets[idx]
			if n == nil {
				n = &NetCounters{}
				nets[idx] = n
				maxNet = max(maxNet, idx)
			}
			v := paramUint(p.Value)
			switch field {
			case "name":
				n.Name = paramString(p.Value)
			case "rx.bytes":
				n.RxBytes = v
			case "tx.bytes":
				n.TxBytes = v
			case "rx.pkts":
				n.RxPkts = v
			case "tx.pkts":
				n.TxPkts = v
			case "rx.errs":
				n.RxErrs = v
			case "tx.errs":
				n.TxErrs = v
			case "rx.drop":
				n.RxDrop = v
			case "tx.drop":
				n.TxDrop = v
			}
		}
	}
	for i := 0; i <= maxDisk; i++ {
		if d := disks[i]; d != nil && d.Name != "" {
			d.Timed = timed[i] == 2
			s.Disks = append(s.Disks, *d)
		}
	}
	for i := 0; i <= maxNet; i++ {
		if n := nets[i]; n != nil && n.Name != "" {
			s.Nets = append(s.Nets, *n)
		}
	}
	return s
}

// indexed splits "3.rd.bytes" into (3, "rd.bytes"). Plain fields like "count" are rejected.
func indexed(s string) (int, string, bool) {
	head, field, ok := strings.Cut(s, ".")
	if !ok {
		return 0, "", false
	}
	n, err := strconv.Atoi(head)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, field, true
}

func paramUint(v libvirt.TypedParamValue) uint64 {
	switch x := v.I.(type) {
	case uint64:
		return x
	case int64:
		return uint64(max(x, 0))
	case uint32:
		return uint64(x)
	case int32:
		return uint64(max(x, 0))
	case float64:
		return uint64(max(x, 0))
	}
	return 0
}

func paramString(v libvirt.TypedParamValue) string {
	s, _ := v.I.(string)
	return s
}
