package dsbrowse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pool(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "isos"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "web.qcow2"), []byte("QFI\xfbrest"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "disk"), []byte("QFI\xfb"), 0o644)) // no extension: magic
	must(t, os.WriteFile(filepath.Join(root, "isos", "debian.iso"), []byte("x"), 0o644))
	thin, err := os.Create(filepath.Join(root, "thin.raw"))
	must(t, err)
	must(t, thin.Truncate(64<<20)) // 64 MiB, nothing written
	thin.Close()
	return root
}

// The tests do not depend on the room of the machine they run on: a 100 GiB pool, half free.
func init() { setRoom(50 << 30) }

func setRoom(free uint64) {
	freeSpace = func(string) (uint64, uint64, error) { return free, 100 << 30, nil }
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	root := pool(t)
	users := map[string][]string{filepath.Join(root, "web.qcow2"): {"web"}, filepath.Join(root, "isos", "debian.iso"): {"installer"}}
	es, err := List(root, "", users)
	must(t, err)
	var got []string
	for _, e := range es {
		got = append(got, e.Name+":"+e.Format+":"+strings.Join(e.UsedBy, ","))
	}
	if strings.Join(got, " ") != "isos::installer disk:qcow2: thin.raw:raw: web.qcow2:qcow2:web" {
		t.Errorf("list = %v", got)
	}
	if thin := es[2]; thin.Size != 64<<20 || thin.Allocated >= thin.Size {
		t.Errorf("a thin file: size %d allocated %d", thin.Size, thin.Allocated)
	}
	if es, err := List(root, "isos", users); err != nil || len(es) != 1 || es[0].Name != "debian.iso" {
		t.Errorf("subfolder: %v %v", es, err)
	}
}

func TestNothingLeavesThePool(t *testing.T) {
	root := pool(t)
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600))
	must(t, os.Symlink(outside, filepath.Join(root, "escape")))
	for _, p := range []string{"../", "isos/../../x", "/etc", "escape/secret"} {
		if _, err := List(root, p, nil); err == nil {
			t.Errorf("listed %q", p)
		}
		if f, err := Open(root, p); err == nil {
			f.Close()
			t.Errorf("opened %q", p)
		}
	}
	if _, err := Upload(root, "escape/planted", strings.NewReader("x"), 1); err == nil {
		t.Error("wrote through a symbolic link out of the pool")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); err == nil {
		t.Error("file planted outside the pool")
	}
}

func TestMachineDisksStayPut(t *testing.T) {
	root := pool(t)
	users := map[string][]string{filepath.Join(root, "isos", "debian.iso"): {"installer"}}
	for name, err := range map[string]error{
		"rename":        Rename(root, "isos/debian.iso", "d.iso", users),
		"rename folder": Rename(root, "isos", "media", users),
		"remove":        Remove(root, "isos/debian.iso", users),
		"move":          Copy(context.Background(), root, "isos/debian.iso", t.TempDir(), "d.iso", true, users, nil),
	} {
		if !errors.Is(err, ErrInUse) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "isos", "debian.iso")); err != nil {
		t.Error("the machine's disk moved")
	}
}

func TestFolderRenameRemove(t *testing.T) {
	root := pool(t)
	must(t, Mkdir(root, "templates"))
	if err := Mkdir(root, "templates"); !errors.Is(err, ErrInvalid) {
		t.Errorf("second mkdir: %v", err)
	}
	if err := Rename(root, "web.qcow2", "disk", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("rename over a file: %v", err)
	}
	must(t, Rename(root, "web.qcow2", "templates/web.qcow2", nil))
	if err := Remove(root, "templates", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("removed a folder that is not empty: %v", err)
	}
	must(t, Remove(root, "templates/web.qcow2", nil))
	must(t, Remove(root, "templates", nil))
	if err := Remove(root, "", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("removed the pool: %v", err)
	}
}

func TestUploadNeverReplaces(t *testing.T) {
	root := pool(t)
	if _, err := Upload(root, "web.qcow2", strings.NewReader("new"), 3); !errors.Is(err, ErrInvalid) {
		t.Errorf("replaced a file: %v", err)
	}
	n, err := Upload(root, "isos/alpine.iso", strings.NewReader("alpine"), -1)
	if err != nil || n != 6 {
		t.Fatalf("upload: %d %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "isos", "alpine.iso")); string(b) != "alpine" {
		t.Errorf("content %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "isos", ".alpine.iso.part")); !os.IsNotExist(err) {
		t.Error("part file left behind")
	}
}

func TestCopyKeepsThinImagesThin(t *testing.T) {
	root, other := pool(t), t.TempDir()
	var progress []string
	must(t, Copy(context.Background(), root, "thin.raw", other, "thin.raw", false, nil, func(s string) { progress = append(progress, s) }))
	st, err := os.Stat(filepath.Join(other, "thin.raw"))
	must(t, err)
	if st.Size() != 64<<20 {
		t.Errorf("size %d", st.Size())
	}
	es, _ := List(other, "", nil)
	if es[0].Allocated > 1<<20 {
		t.Errorf("the copy of a thin image takes %d bytes", es[0].Allocated)
	}
	if progress[len(progress)-1] != "100 %" {
		t.Errorf("progress %v", progress)
	}
	must(t, Copy(context.Background(), root, "web.qcow2", other, "web.qcow2", true, nil, nil))
	if _, err := os.Stat(filepath.Join(root, "web.qcow2")); !os.IsNotExist(err) {
		t.Error("move left the source")
	}
	if err := Copy(context.Background(), root, "thin.raw", other, "thin.raw", false, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("copied over a file: %v", err)
	}
}

// A machine that reaches its disk through a symbolic link uses the file behind the link, and
// a pool whose directory is a link holds the disks libvirt names by their real path.
func TestDisksBehindLinksStayPut(t *testing.T) {
	root := pool(t)
	must(t, os.Symlink("web.qcow2", filepath.Join(root, "current.qcow2")))
	users := map[string][]string{filepath.Join(root, "current.qcow2"): {"web"}}
	for name, err := range map[string]error{
		"remove the file behind the link": Remove(root, "web.qcow2", users),
		"rename the file behind the link": Rename(root, "web.qcow2", "old.qcow2", users),
		"remove the link":                 Remove(root, "current.qcow2", users),
		"move the file behind the link":   Copy(context.Background(), root, "web.qcow2", t.TempDir(), "w.qcow2", true, users, nil),
	} {
		if !errors.Is(err, ErrInUse) {
			t.Errorf("%s: %v", name, err)
		}
	}
	es, err := List(root, "", users)
	must(t, err)
	for _, e := range es {
		if (e.Name == "web.qcow2" || e.Name == "current.qcow2") && strings.Join(e.UsedBy, ",") != "web" {
			t.Errorf("%s used by %v", e.Name, e.UsedBy)
		}
	}

	// A link to a disk in use is not the disk: it can go.
	must(t, os.Symlink("web.qcow2", filepath.Join(root, "spare.qcow2")))
	must(t, Remove(root, "spare.qcow2", users))
	if _, err := os.Stat(filepath.Join(root, "web.qcow2")); err != nil {
		t.Error("removing a link removed the disk")
	}

	// The pool's directory is itself a link; libvirt names the disk by its real path.
	linked := filepath.Join(t.TempDir(), "images")
	must(t, os.Symlink(root, linked))
	real, err := filepath.EvalSymlinks(root)
	must(t, err)
	users = map[string][]string{filepath.Join(real, "isos", "debian.iso"): {"installer"}}
	if err := Remove(linked, "isos/debian.iso", users); !errors.Is(err, ErrInUse) {
		t.Errorf("removed a disk of a pool behind a link: %v", err)
	}
	if err := Rename(linked, "isos", "media", users); !errors.Is(err, ErrInUse) {
		t.Errorf("renamed the folder of a disk of a pool behind a link: %v", err)
	}
}

func TestUploadsAndCopiesLeaveRoom(t *testing.T) {
	root, other := pool(t), t.TempDir()
	t.Cleanup(func() { setRoom(50 << 30) })

	setRoom(3 << 30) // 2 GiB stay free: 1 GiB to use
	if _, err := Upload(root, "big.iso", strings.NewReader("x"), 2<<30); !errors.Is(err, ErrNoSpace) {
		t.Errorf("upload larger than the room: %v", err)
	}
	if es, _ := os.ReadDir(root); len(es) != 4 {
		t.Errorf("a refused upload left something behind: %v", es)
	}
	if _, err := Upload(root, "small.iso", strings.NewReader("x"), 1); err != nil {
		t.Errorf("upload that fits: %v", err)
	}
	// thin.raw is 64 MiB of nothing: it fits where 64 MiB of data would not.
	setRoom(2<<30 + 1<<20)
	if err := Copy(context.Background(), root, "thin.raw", other, "thin.raw", false, nil, nil); err != nil {
		t.Errorf("copy of a thin image: %v", err)
	}

	setRoom(1 << 30) // less than the reserve
	if _, err := Upload(root, "any.iso", strings.NewReader("x"), -1); !errors.Is(err, ErrNoSpace) {
		t.Errorf("upload of unknown size into a full pool: %v", err)
	}
	if err := Copy(context.Background(), root, "web.qcow2", other, "web.qcow2", false, nil, nil); !errors.Is(err, ErrNoSpace) {
		t.Errorf("copy into a full pool: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "web.qcow2")); !os.IsNotExist(err) {
		t.Error("a refused copy left a file")
	}
}

// A pool that runs low while a file is written stops the write and keeps nothing of it.
func TestUploadStopsWhenThePoolRunsLow(t *testing.T) {
	root := pool(t)
	t.Cleanup(func() { setRoom(50 << 30) })
	calls := 0
	freeSpace = func(string) (uint64, uint64, error) {
		if calls++; calls > 1 {
			return 1 << 30, 100 << 30, nil // another writer took the room meanwhile
		}
		return 50 << 30, 100 << 30, nil
	}
	data := strings.NewReader(strings.Repeat("x", (guardEvery+1)*chunk))
	if _, err := Upload(root, "big.iso", data, -1); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("upload went on in a pool running low: %v", err)
	}
	for _, name := range []string{"big.iso", ".big.iso.part"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("%s left behind", name)
		}
	}
}

// A part file that is already there (a transfer running, or one cut off by a crash) is
// named, so it can be deleted; it is never written over.
func TestTransferSaysWhenItsPartFileIsThere(t *testing.T) {
	root, other := pool(t), t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, ".new.iso.part"), []byte("half"), 0o644))
	_, err := Upload(root, "new.iso", strings.NewReader("x"), 1)
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), ".new.iso.part") {
		t.Errorf("upload: %v", err)
	}
	must(t, os.WriteFile(filepath.Join(other, ".web.qcow2.part"), []byte("half"), 0o644))
	err = Copy(context.Background(), root, "web.qcow2", other, "web.qcow2", false, nil, nil)
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), ".web.qcow2.part") {
		t.Errorf("copy: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".new.iso.part")); string(b) != "half" {
		t.Error("another transfer's part file was touched")
	}
	// Deleted, the transfer works.
	must(t, Remove(root, ".new.iso.part", nil))
	if _, err := Upload(root, "new.iso", strings.NewReader("x"), 1); err != nil {
		t.Errorf("after deleting the part file: %v", err)
	}
}
