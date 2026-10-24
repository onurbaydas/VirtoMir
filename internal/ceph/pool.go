package ceph

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// PoolSpec is an RBD pool to create. Erasure-coded pools keep their data in <name>-data
// and the image metadata in <name> (RBD needs a replicated pool for that); libvirt only
// ever sees <name>.
type PoolSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`                  // replicated or erasure
	ReplicaSize int    `json:"replicaSize,omitempty"` // replicated: copies, default 3
	ECK         int    `json:"ecK,omitempty"`         // erasure: data chunks, default 4
	ECM         int    `json:"ecM,omitempty"`         // erasure: coding chunks, default 2
	DeviceClass string `json:"deviceClass,omitempty"` // ssd, hdd or nvme: only OSDs of that class
	PGNum       int    `json:"pgNum,omitempty"`       // first placement group count; the autoscaler adjusts it
}

var cephPoolRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,58}$`)

// Normalize fills the defaults and checks the rest.
func (p *PoolSpec) Normalize() error {
	if !cephPoolRE.MatchString(p.Name) {
		return invalidf("the pool name is letters, digits, dots, dashes and underscores")
	}
	if p.PGNum == 0 {
		p.PGNum = 32
	}
	if p.PGNum < 1 || p.PGNum > 4096 || p.PGNum&(p.PGNum-1) != 0 {
		return invalidf("the placement group count is a power of two up to 4096")
	}
	switch p.DeviceClass {
	case "", "ssd", "hdd", "nvme":
	default:
		return invalidf("the device class is ssd, hdd or nvme")
	}
	switch p.Type {
	case "", "replicated":
		p.Type = "replicated"
		if p.ReplicaSize == 0 {
			p.ReplicaSize = 3
		}
		if p.ReplicaSize < 1 || p.ReplicaSize > 5 {
			return invalidf("a replicated pool keeps 1 to 5 copies")
		}
		p.ECK, p.ECM = 0, 0
	case "erasure":
		if p.ECK == 0 {
			p.ECK = 4
		}
		if p.ECM == 0 {
			p.ECM = 2
		}
		if p.ECK < 2 || p.ECK > 16 || p.ECM < 1 || p.ECM > 8 {
			return invalidf("erasure coding needs 2 to 16 data and 1 to 8 coding chunks")
		}
		p.ReplicaSize = 0
	default:
		return invalidf("the pool type is replicated or erasure")
	}
	return nil
}

func run(ctx context.Context, r Runner, args ...string) error {
	_, err := r.Run(ctx, "ceph", args...)
	return err
}

// CreateRBDPool creates a pool for RBD images.
func CreateRBDPool(ctx context.Context, r Runner, spec PoolSpec) error {
	if err := spec.Normalize(); err != nil {
		return err
	}
	pgs := strconv.Itoa(spec.PGNum)
	meta := spec.Name
	if spec.Type == "erasure" {
		profile := "ec-" + spec.Name
		args := []string{"osd", "erasure-code-profile", "set", profile, "k=" + strconv.Itoa(spec.ECK), "m=" + strconv.Itoa(spec.ECM),
			"crush-failure-domain=host"}
		if spec.DeviceClass != "" {
			args = append(args, "crush-device-class="+spec.DeviceClass)
		}
		data := spec.Name + "-data"
		for _, a := range [][]string{
			args,
			{"osd", "pool", "create", data, pgs, pgs, "erasure", profile},
			{"osd", "pool", "set", data, "allow_ec_overwrites", "true"},
			{"osd", "pool", "application", "enable", data, "rbd"},
		} {
			if err := run(ctx, r, a...); err != nil {
				return missing(err, "The ceph command")
			}
		}
	}
	if err := run(ctx, r, "osd", "pool", "create", meta, pgs, pgs, "replicated"); err != nil {
		return missing(err, "The ceph command")
	}
	size := spec.ReplicaSize
	if spec.Type == "erasure" {
		size = 3
	}
	steps := [][]string{{"osd", "pool", "set", meta, "size", strconv.Itoa(size)}}
	if size == 1 {
		// Ceph refuses size 1 unless told it is meant.
		steps = [][]string{{"config", "set", "global", "mon_allow_pool_size_one", "true"},
			{"osd", "pool", "set", meta, "size", "1", "--yes-i-really-mean-it"}}
	}
	if spec.DeviceClass != "" {
		rule := "replicated-" + spec.DeviceClass
		steps = append(steps, []string{"osd", "crush", "rule", "create-replicated", rule, "default", "host", spec.DeviceClass},
			[]string{"osd", "pool", "set", meta, "crush_rule", rule})
	}
	steps = append(steps, []string{"osd", "pool", "application", "enable", meta, "rbd"})
	for _, a := range steps {
		if err := run(ctx, r, a...); err != nil {
			return err
		}
	}
	if _, err := r.Run(ctx, "rbd", "pool", "init", meta); err != nil {
		return missing(err, "The rbd command")
	}
	if spec.Type == "erasure" {
		if _, err := r.Run(ctx, "rbd", "config", "pool", "set", meta, "rbd_default_data_pool", spec.Name+"-data"); err != nil {
			return err
		}
	}
	return nil
}

// PoolDetail is an RBD pool and what it stores.
type PoolDetail struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"` // replicated or erasure
	Size        int     `json:"size"` // copies (replicated) or k+m (erasure)
	MinSize     int     `json:"minSize"`
	ECProfile   string  `json:"ecProfile,omitempty"`
	DataPool    string  `json:"dataPool,omitempty"` // erasure: where the data goes
	StoredBytes uint64  `json:"storedBytes"`
	PercentUsed float64 `json:"percentUsed"`
	MaxAvail    uint64  `json:"maxAvailBytes"`
}

type rawPool struct {
	ID          int                        `json:"pool_id"`
	Name        string                     `json:"pool_name"`
	Type        int                        `json:"type"` // 1 replicated, 3 erasure
	Size        int                        `json:"size"`
	MinSize     int                        `json:"min_size"`
	ECProfile   string                     `json:"erasure_code_profile"`
	Application map[string]json.RawMessage `json:"application_metadata"`
}

// ListRBDPools lists the pools with the rbd application; an erasure-coded pool shows as
// its metadata pool with the data pool's usage.
func ListRBDPools(ctx context.Context, r Runner) ([]PoolDetail, error) {
	out, err := r.Run(ctx, "ceph", "osd", "pool", "ls", "detail", "-f", "json")
	if err != nil {
		return nil, missing(err, "The ceph command")
	}
	var raw []rawPool
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("read ceph osd pool ls: %w", err)
	}
	capa, err := Capacity(ctx, r)
	if err != nil {
		return nil, err
	}
	usage := map[string]PoolCapacity{}
	for _, p := range capa.Pools {
		usage[p.Name] = p
	}
	byName := map[string]rawPool{}
	for _, p := range raw {
		byName[p.Name] = p
	}
	pools := []PoolDetail{}
	for _, p := range raw {
		_, rbd := p.Application["rbd"]
		if meta, isData := strings.CutSuffix(p.Name, "-data"); !rbd || (p.Type == 3 && isData && byName[meta].Name != "") {
			continue // not RBD, or the data half of an erasure-coded pool
		}
		d := PoolDetail{ID: p.ID, Name: p.Name, Type: "replicated", Size: p.Size, MinSize: p.MinSize}
		u := usage[p.Name]
		if p.Type == 3 {
			d.Type, d.ECProfile = "erasure", p.ECProfile
		}
		if data, ok := byName[p.Name+"-data"]; ok && data.Type == 3 {
			d.Type, d.ECProfile, d.DataPool, d.Size, d.MinSize = "erasure", data.ECProfile, data.Name, data.Size, data.MinSize
			u = usage[data.Name]
		}
		d.StoredBytes, d.PercentUsed, d.MaxAvail = u.StoredBytes, u.PercentUsed, u.MaxAvail
		pools = append(pools, d)
	}
	return pools, nil
}

// DeleteRBDPool removes a pool (and its data pool), with all images in it. Ceph only
// deletes pools while mon_allow_pool_delete is on, so it is switched on just for this.
func DeleteRBDPool(ctx context.Context, r Runner, name string, dataPool string) error {
	if !cephPoolRE.MatchString(name) || (dataPool != "" && !cephPoolRE.MatchString(dataPool)) {
		return invalidf("%q is not a pool name", name)
	}
	if err := run(ctx, r, "config", "set", "mon", "mon_allow_pool_delete", "true"); err != nil {
		return missing(err, "The ceph command")
	}
	defer func() {
		_ = run(context.WithoutCancel(ctx), r, "config", "set", "mon", "mon_allow_pool_delete", "false")
	}()
	for _, p := range []string{name, dataPool} {
		if p == "" {
			continue
		}
		if err := run(ctx, r, "osd", "pool", "rm", p, p, "--yes-i-really-really-mean-it"); err != nil {
			return err
		}
	}
	return nil
}

// LibvirtPool is the libvirt pool spec for an RBD pool of the cluster.
func LibvirtPool(cfg ClusterConfig, cephPool, libvirtName string) virt.PoolSpec {
	return virt.PoolSpec{Name: libvirtName, Type: "rbd", Monitors: slices.Clone(cfg.MonEndpoints), CephPool: cephPool,
		CephUser: cfg.ClientUser, CephKey: cfg.ClientKey}
}
