package lcm

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The pre-checks are plain functions of what the members report, so they are tested
// without a cluster.

// QuorumSafeWithout reports whether high availability keeps its quorum while targetHost is
// away. members are the hosts that stay up (hosts already in maintenance left out); the
// rule is the one of internal/ha (ADR-0059): every member and a reachable witness vote, and
// a quorum is a majority of all votes.
func QuorumSafeWithout(members []string, targetHost string, haEnabled bool, totalVotes int, witnessReachable bool) (bool, string) {
	if !haEnabled {
		return true, ""
	}
	votes := 0
	for _, m := range members {
		if m != targetHost {
			votes++
		}
	}
	if witnessReachable {
		votes++
	}
	required := totalVotes/2 + 1
	if votes < required {
		return false, fmt.Sprintf("high availability would lose its quorum while this host is away (%d of %d votes, %d needed); add a witness or a member first", votes, totalVotes, required)
	}
	return true, ""
}

// VM is a machine on the host being emptied.
type VM struct {
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	MemKiB        uint64 `json:"memKiB"`
	Running       bool   `json:"running"`
	HasHostDevice bool   `json:"hasHostDevice"`
}

// Target is a member that can take machines, with its free memory.
type Target struct {
	HostID  string `json:"hostId"`
	Name    string `json:"name"`
	FreeKiB int64  `json:"freeKiB"`
}

// PlannedMove is one machine going to one member.
type PlannedMove struct {
	VMUUID         string `json:"vmUuid"`
	VMName         string `json:"vmName"`
	TargetHostID   string `json:"targetHostId"`
	TargetHostName string `json:"targetHostName"`
	MemKiB         uint64 `json:"memKiB"`
}

// EvacuationFeasible plans where every running machine goes, biggest first, to the member
// with the most free memory that its placement rules allow (as the Maintenance dialog
// does). Stopped machines stay. Any problem means the host cannot be emptied.
func EvacuationFeasible(vms []VM, targets []Target, checkPlacement func(vmUUID, targetHostID string) (hard, soft []string)) (moves []PlannedMove, problems []string) {
	free := map[string]int64{}
	for _, t := range targets {
		free[t.HostID] = t.FreeKiB
	}
	running := slices.DeleteFunc(slices.Clone(vms), func(v VM) bool { return !v.Running })
	slices.SortStableFunc(running, func(a, b VM) int { return cmp.Compare(b.MemKiB, a.MemKiB) })
	for _, v := range running {
		if v.HasHostDevice {
			problems = append(problems, v.Name+" has a host device (PCI/USB/vGPU): stop it or remove the device first")
			continue
		}
		best := -1
		for i, t := range targets {
			if free[t.HostID] < int64(v.MemKiB) {
				continue
			}
			if checkPlacement != nil {
				if hard, _ := checkPlacement(v.UUID, t.HostID); len(hard) > 0 {
					continue
				}
			}
			if best < 0 || free[t.HostID] > free[targets[best].HostID] {
				best = i
			}
		}
		if best < 0 {
			problems = append(problems, fmt.Sprintf("%s (%d MiB) fits on no other member that its placement rules allow", v.Name, v.MemKiB>>10))
			continue
		}
		t := targets[best]
		free[t.HostID] -= int64(v.MemKiB)
		moves = append(moves, PlannedMove{VMUUID: v.UUID, VMName: v.Name, TargetHostID: t.HostID, TargetHostName: t.Name, MemKiB: v.MemKiB})
	}
	return moves, problems
}

// RunningJobsSafe: a host with background jobs (backups, copies) is not taken down.
func RunningJobsSafe(running int) (bool, string) {
	if running > 0 {
		return false, fmt.Sprintf("host has %d running background jobs or backups; wait for them to finish", running)
	}
	return true, ""
}

type semver struct {
	dev  bool
	part [3]int
}

func parseVersion(v string) semver {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	fields := strings.Split(core, ".")
	if v == "" || v == "dev" || len(fields) != 3 {
		return semver{dev: true}
	}
	var s semver
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return semver{dev: true}
		}
		s.part[i] = n
	}
	return s
}

// compareVersions orders versions; a development build counts as the newest.
func compareVersions(a, b string) int {
	va, vb := parseVersion(a), parseVersion(b)
	switch {
	case va.dev && vb.dev:
		return 0
	case va.dev:
		return 1
	case vb.dev:
		return -1
	}
	return slices.Compare(va.part[:], vb.part[:])
}

// AtLeast reports whether version have is want or newer.
func AtLeast(have, want string) bool { return want == "" || compareVersions(have, want) >= 0 }

// SameMajor reports whether all versions share a major version (development builds fit any).
func SameMajor(versions []string) bool {
	major := -1
	for _, v := range versions {
		s := parseVersion(v)
		if s.dev {
			continue
		}
		if major >= 0 && s.part[0] != major {
			return false
		}
		major = s.part[0]
	}
	return true
}

// MinVersion is the oldest of versions ("" for none).
func MinVersion(versions []string) string {
	min := ""
	for _, v := range versions {
		if min == "" || compareVersions(v, min) < 0 {
			min = v
		}
	}
	return min
}

// FeatureAllowed reports whether a cluster whose oldest member runs minCluster may use a
// feature that needs required.
func FeatureAllowed(minCluster, required string) bool {
	return minCluster == "" || parseVersion(minCluster).dev || AtLeast(minCluster, required)
}
