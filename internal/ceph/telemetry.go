package ceph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HealthCheck is one problem Ceph reports.
type HealthCheck struct {
	Name     string   `json:"name"`
	Severity string   `json:"severity"` // HEALTH_WARN or HEALTH_ERR
	Summary  string   `json:"summary"`
	Detail   []string `json:"detail,omitempty"`
}

// ClusterHealth is Ceph's overall verdict and its checks.
type ClusterHealth struct {
	Status string        `json:"status"` // HEALTH_OK, HEALTH_WARN or HEALTH_ERR
	Checks []HealthCheck `json:"checks"`
}

// Summary is the checks' summaries in one line.
func (h ClusterHealth) Summary() string {
	s := ""
	for i, c := range h.Checks {
		if i > 0 {
			s += "; "
		}
		s += c.Summary
	}
	return s
}

// PoolCapacity is one pool's share of the cluster.
type PoolCapacity struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	StoredBytes uint64  `json:"storedBytes"`
	Objects     uint64  `json:"objects"`
	PercentUsed float64 `json:"percentUsed"`
	MaxAvail    uint64  `json:"maxAvailBytes"`
	// Compressed is the raw space compressed data takes, CompressedFrom what it was before
	// (BlueStore compression, ADR-0074).
	Compressed     uint64 `json:"compressedBytes"`
	CompressedFrom uint64 `json:"compressedFromBytes"`
}

// CapacityReport is the raw capacity and what each pool stores.
type CapacityReport struct {
	TotalBytes  uint64         `json:"totalBytes"`
	UsedBytes   uint64         `json:"usedBytes"`
	AvailBytes  uint64         `json:"availBytes"`
	PercentUsed float64        `json:"percentUsed"`
	Pools       []PoolCapacity `json:"pools"`
}

// OSDNode is a node of the CRUSH tree: the root, hosts and OSDs.
type OSDNode struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`             // root, host, osd, …
	Status      string  `json:"status,omitempty"` // up or down (OSDs)
	In          bool    `json:"in"`
	Weight      float64 `json:"weight"`
	DeviceClass string  `json:"deviceClass,omitempty"`
	Children    []int   `json:"children,omitempty"`
}

// PGSummary counts the placement groups by state.
type PGSummary struct {
	Total  int            `json:"total"`
	States map[string]int `json:"states"`
}

// Daemons counts monitors, managers and OSDs.
type Daemons struct {
	Mons       int      `json:"mons"`
	Quorum     []string `json:"quorum"`
	MgrUp      bool     `json:"mgrUp"`
	OSDs       int      `json:"osds"`
	OSDsUp     int      `json:"osdsUp"`
	OSDsIn     int      `json:"osdsIn"`
	NooutIsSet bool     `json:"noout"`
}

// Telemetry is the cluster at one moment.
type Telemetry struct {
	FSID      string         `json:"fsid"`
	Health    ClusterHealth  `json:"health"`
	Daemons   Daemons        `json:"daemons"`
	Capacity  CapacityReport `json:"capacity"`
	OSDTree   []OSDNode      `json:"osdTree"`
	PGs       PGSummary      `json:"pgs"`
	Collected time.Time      `json:"collected"`
}

type statusDoc struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Severity string `json:"severity"`
			Summary  struct {
				Message string `json:"message"`
			} `json:"summary"`
			Detail []struct {
				Message string `json:"message"`
			} `json:"detail"`
		} `json:"checks"`
	} `json:"health"`
	QuorumNames []string `json:"quorum_names"`
	MonMap      struct {
		NumMons int `json:"num_mons"`
	} `json:"monmap"`
	OSDMap struct {
		NumOSDs   int `json:"num_osds"`
		NumUpOSDs int `json:"num_up_osds"`
		NumInOSDs int `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		NumPGs     int `json:"num_pgs"`
		PGsByState []struct {
			StateName string `json:"state_name"`
			Count     int    `json:"count"`
		} `json:"pgs_by_state"`
	} `json:"pgmap"`
	MgrMap struct {
		Available bool `json:"available"`
	} `json:"mgrmap"`
}

func parseHealth(raw []byte) (ClusterHealth, error) {
	var d statusDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return ClusterHealth{}, fmt.Errorf("read ceph health: %w", err)
	}
	h := ClusterHealth{Status: d.Health.Status, Checks: []HealthCheck{}}
	for name, c := range d.Health.Checks {
		hc := HealthCheck{Name: name, Severity: c.Severity, Summary: c.Summary.Message}
		for _, x := range c.Detail {
			hc.Detail = append(hc.Detail, x.Message)
		}
		h.Checks = append(h.Checks, hc)
	}
	// Errors first, then by name, so the list does not jump around.
	sort.Slice(h.Checks, func(i, j int) bool {
		if h.Checks[i].Severity != h.Checks[j].Severity {
			return h.Checks[i].Severity == "HEALTH_ERR"
		}
		return h.Checks[i].Name < h.Checks[j].Name
	})
	return h, nil
}

// Health asks Ceph how it is, with the details of each check.
func Health(ctx context.Context, r Runner) (ClusterHealth, error) {
	out, err := r.Run(ctx, "ceph", "health", "detail", "-f", "json")
	if err != nil {
		return ClusterHealth{}, missing(err, "The ceph command")
	}
	// health detail is the "health" object of ceph status on its own.
	return parseHealth(append(append([]byte(`{"health":`), out...), '}'))
}

func parseDF(raw []byte) (CapacityReport, error) {
	var d struct {
		Stats struct {
			Total uint64 `json:"total_bytes"`
			Avail uint64 `json:"total_avail_bytes"`
			Used  uint64 `json:"total_used_raw_bytes"`
		} `json:"stats"`
		Pools []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Stats struct {
				Stored      uint64  `json:"stored"`
				Objects     uint64  `json:"objects"`
				PercentUsed float64 `json:"percent_used"`
				MaxAvail    uint64  `json:"max_avail"`
				Compressed  uint64  `json:"compress_bytes_used"`
				Under       uint64  `json:"compress_under_bytes"`
			} `json:"stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return CapacityReport{}, fmt.Errorf("read ceph df: %w", err)
	}
	c := CapacityReport{TotalBytes: d.Stats.Total, UsedBytes: d.Stats.Used, AvailBytes: d.Stats.Avail, Pools: []PoolCapacity{}}
	if c.TotalBytes > 0 {
		c.PercentUsed = float64(c.UsedBytes) / float64(c.TotalBytes) * 100
	}
	for _, p := range d.Pools {
		// ceph df gives percent_used as a fraction.
		c.Pools = append(c.Pools, PoolCapacity{ID: p.ID, Name: p.Name, StoredBytes: p.Stats.Stored, Objects: p.Stats.Objects,
			PercentUsed: p.Stats.PercentUsed * 100, MaxAvail: p.Stats.MaxAvail, Compressed: p.Stats.Compressed, CompressedFrom: p.Stats.Under})
	}
	return c, nil
}

// Capacity reads ceph df detail.
func Capacity(ctx context.Context, r Runner) (CapacityReport, error) {
	out, err := r.Run(ctx, "ceph", "df", "detail", "-f", "json")
	if err != nil {
		return CapacityReport{}, missing(err, "The ceph command")
	}
	return parseDF(out)
}

func parseOSDTree(raw []byte) ([]OSDNode, error) {
	var d struct {
		Nodes []struct {
			ID          int     `json:"id"`
			Name        string  `json:"name"`
			Type        string  `json:"type"`
			Status      string  `json:"status"`
			Reweight    float64 `json:"reweight"`
			CrushWeight float64 `json:"crush_weight"`
			DeviceClass string  `json:"device_class"`
			Children    []int   `json:"children"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("read ceph osd tree: %w", err)
	}
	out := make([]OSDNode, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		node := OSDNode{ID: n.ID, Name: n.Name, Type: n.Type, Status: n.Status, Weight: n.CrushWeight, DeviceClass: n.DeviceClass,
			Children: n.Children}
		if n.Type == "osd" {
			node.In = n.Reweight > 0
		}
		out = append(out, node)
	}
	return out, nil
}

// OSDTree reads the CRUSH tree.
func OSDTree(ctx context.Context, r Runner) ([]OSDNode, error) {
	out, err := r.Run(ctx, "ceph", "osd", "tree", "-f", "json")
	if err != nil {
		return nil, missing(err, "The ceph command")
	}
	return parseOSDTree(out)
}

// Collect reads status, health detail, df and the OSD tree.
func Collect(ctx context.Context, r Runner, now time.Time) (Telemetry, error) {
	out, err := r.Run(ctx, "ceph", "status", "-f", "json")
	if err != nil {
		return Telemetry{}, missing(err, "The ceph command")
	}
	var st statusDoc
	if err := json.Unmarshal(out, &st); err != nil {
		return Telemetry{}, fmt.Errorf("read ceph status: %w", err)
	}
	t := Telemetry{FSID: st.FSID, Collected: now.UTC(),
		Daemons: Daemons{Mons: st.MonMap.NumMons, Quorum: st.QuorumNames, MgrUp: st.MgrMap.Available,
			OSDs: st.OSDMap.NumOSDs, OSDsUp: st.OSDMap.NumUpOSDs, OSDsIn: st.OSDMap.NumInOSDs},
		PGs: PGSummary{Total: st.PGMap.NumPGs, States: map[string]int{}}}
	if t.Daemons.Quorum == nil {
		t.Daemons.Quorum = []string{}
	}
	for _, p := range st.PGMap.PGsByState {
		t.PGs.States[p.StateName] += p.Count
	}
	if t.Health, err = Health(ctx, r); err != nil {
		return Telemetry{}, err
	}
	for _, c := range t.Health.Checks {
		if c.Name == "OSDMAP_FLAGS" && strings.Contains(c.Summary, "noout") {
			t.Daemons.NooutIsSet = true
		}
	}
	if t.Capacity, err = Capacity(ctx, r); err != nil {
		return Telemetry{}, err
	}
	if t.OSDTree, err = OSDTree(ctx, r); err != nil {
		return Telemetry{}, err
	}
	return t, nil
}
