package api

import (
	"slices"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestDisksToCopy(t *testing.T) {
	nfs := virt.LiveDisk{Target: "vda", Path: "/shared/a.qcow2", Writable: true, Network: true}
	local := virt.LiveDisk{Target: "vdb", Path: "/images/b.qcow2", Writable: true}
	iso := virt.LiveDisk{Target: "sda", Path: "/iso/x.iso", CDROM: true}
	cases := []struct {
		name   string
		disks  []virt.LiveDisk
		checks []virt.PathCheck
		copy   []string
		err    string
	}{
		{"shared + copied", []virt.LiveDisk{nfs, local},
			[]virt.PathCheck{{Exists: true, Network: true}, {Pool: "images"}}, []string{"vdb"}, ""},
		{"same path but not on NFS there is never shared", []virt.LiveDisk{nfs},
			[]virt.PathCheck{{Exists: true, Network: false}}, nil, "will not overwrite"},
		{"local file in the way", []virt.LiveDisk{local},
			[]virt.PathCheck{{Exists: true}}, nil, "will not overwrite"},
		{"no pool for the copy", []virt.LiveDisk{local},
			[]virt.PathCheck{{}}, nil, "no storage pool at /images"},
		{"NFS here, missing there: copy", []virt.LiveDisk{nfs},
			[]virt.PathCheck{{Pool: "shared"}}, []string{"vda"}, ""},
		{"ISO missing there", []virt.LiveDisk{iso},
			[]virt.PathCheck{{}}, nil, "Eject it first"},
		{"ISO present there", []virt.LiveDisk{iso},
			[]virt.PathCheck{{Exists: true}}, nil, ""},
	}
	for _, c := range cases {
		got, err := disksToCopy("b", c.disks, c.checks)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.copy) {
			t.Errorf("%s: = %v, %v; want %v", c.name, got, err, c.copy)
		}
	}
}
