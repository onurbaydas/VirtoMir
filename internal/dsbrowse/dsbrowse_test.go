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
	if _, err := Upload(root, "escape/planted", strings.NewReader("x")); err == nil {
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
	if _, err := Upload(root, "web.qcow2", strings.NewReader("new")); !errors.Is(err, ErrInvalid) {
		t.Errorf("replaced a file: %v", err)
	}
	n, err := Upload(root, "isos/alpine.iso", strings.NewReader("alpine"))
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
