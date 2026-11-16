package virt

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ovaWith(t *testing.T, entries ...tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, h := range entries {
		data := []byte(strings.Repeat("d", int(h.Size)))
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		h.Mode = 0o644
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func roomOf(t *testing.T, free, total uint64) {
	t.Helper()
	old := importSpace
	importSpace = func(string) (uint64, uint64, bool) { return free, total, true }
	t.Cleanup(func() { importSpace = old })
}

// An OVA is someone else's archive: its files land in the import folder under their base
// names, whatever paths and links it holds.
func TestUnpackOVAStaysInItsFolder(t *testing.T) {
	roomOf(t, 50<<30, 100<<30)
	top := t.TempDir()
	dir := filepath.Join(top, "import")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ova := ovaWith(t,
		tar.Header{Name: "vm.ovf", Size: 5},
		tar.Header{Name: "../../escaped.vmdk", Size: 3},
		tar.Header{Name: "/etc/cron.d/planted", Size: 3},
		tar.Header{Name: "sub/dir/disk1.vmdk", Size: 4},
		tar.Header{Name: ".hidden", Size: 2},
		tar.Header{Name: "link.vmdk", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"},
		tar.Header{Name: "hard.vmdk", Typeflag: tar.TypeLink, Linkname: "../../etc/shadow"},
		tar.Header{Name: "adir/", Typeflag: tar.TypeDir},
	)
	ovf, err := unpackOVA(context.Background(), ova, dir)
	if err != nil || string(ovf) != "ddddd" {
		t.Fatalf("ovf = %q, %v", ovf, err)
	}
	var names []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if !e.Type().IsRegular() {
			t.Errorf("%s is not a plain file", e.Name())
		}
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "disk1.vmdk escaped.vmdk planted" {
		t.Errorf("unpacked %v", names)
	}
	if es, _ := os.ReadDir(top); len(es) != 1 {
		t.Errorf("something landed outside the import folder: %v", es)
	}
}

// A file that does not fit in the pool with room to spare is refused before it is written:
// an import never fills the pool under the running machines.
func TestUnpackOVALeavesRoomInThePool(t *testing.T) {
	dir := t.TempDir()
	roomOf(t, 3<<30, 100<<30) // 2 GiB stay free: 1 GiB to use
	// The header says 4 GiB (as a sparse entry would, with next to no data behind it).
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "huge.vmdk", Typeflag: tar.TypeReg, Size: 4 << 30, Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	_, err := unpackOVA(context.Background(), &buf, dir)
	var refused *OperationError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "huge.vmdk") {
		t.Fatalf("err = %v", err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("a refused file was started: %v", es)
	}
	// What fits is unpacked.
	if _, err := unpackOVA(context.Background(), ovaWith(t, tar.Header{Name: "vm.ovf", Size: 1}, tar.Header{Name: "small.vmdk", Size: 1 << 10}), dir); err != nil {
		t.Errorf("a small disk: %v", err)
	}
	// A pool already below its reserve takes nothing.
	roomOf(t, 1<<30, 100<<30)
	if _, err := unpackOVA(context.Background(), ovaWith(t, tar.Header{Name: "tiny.vmdk", Size: 1}), dir); !errors.As(err, &refused) {
		t.Errorf("into a full pool: %v", err)
	}
}

func TestUnpackOVARefusesWhatIsNoArchive(t *testing.T) {
	roomOf(t, 50<<30, 100<<30)
	var inv *InvalidError
	if _, err := unpackOVA(context.Background(), strings.NewReader(strings.Repeat("not a tar archive ", 100)), t.TempDir()); !errors.As(err, &inv) {
		t.Errorf("err = %v", err)
	}
	// A cancelled import stops.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := unpackOVA(ctx, ovaWith(t, tar.Header{Name: "disk.vmdk", Size: 1 << 16}), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
