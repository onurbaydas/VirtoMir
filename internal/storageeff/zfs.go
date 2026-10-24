package storageeff

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// zfsNameRE: a pool or dataset (as internal/virt accepts for zfs pools).
var zfsNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*(/[A-Za-z0-9_.:-]+)*$`)

// ZFSEfficiency is what compression and deduplication save on a dataset (a libvirt zfs
// pool's source; its zvols inherit the settings).
type ZFSEfficiency struct {
	Dataset       string  `json:"dataset"`
	Compression   string  `json:"compression"`
	CompressRatio float64 `json:"compressRatio"`
	Dedup         string  `json:"dedup"`
	// DedupRatio is the whole zpool's: ZFS only counts deduplication per pool.
	DedupRatio       float64 `json:"dedupRatio"`
	UsedBytes        uint64  `json:"usedBytes"`
	LogicalUsedBytes uint64  `json:"logicalUsedBytes"`
	SavedBytes       uint64  `json:"savedBytes"` // what compression saves: logical minus used
}

// ratio reads "1.48x" or "1.48".
func ratio(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "x"), 64)
	return v
}

// InspectZFS reads a dataset's compression and dedup settings and what they save.
func InspectZFS(ctx context.Context, r Runner, dataset string) (ZFSEfficiency, error) {
	if !zfsNameRE.MatchString(dataset) {
		return ZFSEfficiency{}, invalidf("%q is not a ZFS pool or dataset", dataset)
	}
	out, err := r.Run(ctx, "zfs", "get", "-H", "-p", "-o", "property,value", "compression,compressratio,dedup,used,logicalused", dataset)
	if err != nil {
		return ZFSEfficiency{}, missing(err, "ZFS")
	}
	e := ZFSEfficiency{Dataset: dataset}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		prop, val, _ := strings.Cut(line, "\t")
		switch prop {
		case "compression":
			e.Compression = val
		case "compressratio":
			e.CompressRatio = ratio(val)
		case "dedup":
			e.Dedup = val
		case "used":
			e.UsedBytes, _ = strconv.ParseUint(val, 10, 64)
		case "logicalused":
			e.LogicalUsedBytes, _ = strconv.ParseUint(val, 10, 64)
		}
	}
	if e.LogicalUsedBytes > e.UsedBytes {
		e.SavedBytes = e.LogicalUsedBytes - e.UsedBytes
	}
	pool, _, _ := strings.Cut(dataset, "/")
	out, err = r.Run(ctx, "zpool", "list", "-H", "-o", "dedupratio", pool)
	if err != nil {
		return ZFSEfficiency{}, err
	}
	e.DedupRatio = ratio(string(out))
	return e, nil
}

// ZFSSettings changes a dataset; empty fields stay as they are.
type ZFSSettings struct {
	Compression string `json:"compression,omitempty"`
	Dedup       string `json:"dedup,omitempty"`
	// AcknowledgeDedupRAM: turning dedup on needs this, after reading what it costs.
	AcknowledgeDedupRAM bool `json:"acknowledgeDedupRam,omitempty"`
}

// DedupRAMWarning is why dedup needs a second thought.
const DedupRAMWarning = "ZFS deduplication keeps a table of every block in RAM: plan about 1 to 5 GB of host memory per TB of deduplicated data, or writes slow down sharply. It only saves space when machines store the same blocks (many clones of one image); compression usually saves more for less. It applies to data written from now on."

var (
	compressions = []string{"off", "on", "lz4", "zstd", "gzip", "zle"}
	dedups       = []string{"off", "on", "verify"}
)

// SetZFS changes compression and dedup. Both only affect data written afterwards.
func SetZFS(ctx context.Context, r Runner, dataset string, s ZFSSettings) error {
	if !zfsNameRE.MatchString(dataset) {
		return invalidf("%q is not a ZFS pool or dataset", dataset)
	}
	if s.Compression != "" && !slices.Contains(compressions, s.Compression) {
		return invalidf("compression is one of %s", strings.Join(compressions, ", "))
	}
	if s.Dedup != "" && !slices.Contains(dedups, s.Dedup) {
		return invalidf("dedup is one of %s", strings.Join(dedups, ", "))
	}
	if (s.Dedup == "on" || s.Dedup == "verify") && !s.AcknowledgeDedupRAM {
		return invalidf("%s Confirm it to turn dedup on.", DedupRAMWarning)
	}
	for _, p := range [][2]string{{"compression", s.Compression}, {"dedup", s.Dedup}} {
		if p[1] == "" {
			continue
		}
		if _, err := r.Run(ctx, "zfs", "set", p[0]+"="+p[1], dataset); err != nil {
			return missing(err, "ZFS")
		}
	}
	return nil
}
