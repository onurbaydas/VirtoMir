package content

import (
	"path/filepath"
	"testing"
)

// Names, versions, formats and source volumes from a publisher never leave the pool.
func TestFileNamesStayInPool(t *testing.T) {
	for _, c := range []struct{ name, version, format string }{
		{"../../etc", "1", "iso"},
		{"x", "1/../../../../etc/passwd", "iso"},
		{"x", "1", "iso/../../y"},
		{"x\\y", "1", "iso"},
	} {
		if f, err := FileName(c.name, c.version, c.format); err == nil {
			t.Errorf("FileName(%q, %q, %q) = %q, want an error", c.name, c.version, c.format, f)
		}
	}
	if f, err := FileName("debian netinst", "12.0", "iso"); err != nil || f != "content-debian netinst-v12.0.iso" {
		t.Errorf("FileName = %q, %v", f, err)
	}
	for _, v := range []string{"../shadow", "/etc/shadow", "a/b", "..", ".hidden"} {
		if _, err := VolFile(v); err == nil {
			t.Errorf("VolFile(%q) accepted", v)
		}
	}
	s, err := Open(filepath.Join(t.TempDir(), "content.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateItem(Item{Name: "x", Type: TypeISO, Versions: []ItemVersion{{Version: "1", Format: "iso", SourceVol: "../../etc/shadow"}}}); err == nil {
		t.Error("CreateItem took a source volume outside its pool")
	}
	if _, err := s.CreateItem(Item{Name: "../x", Type: TypeISO, Versions: []ItemVersion{{Version: "1", Format: "iso"}}}); err == nil {
		t.Error("CreateItem took a name with a path in it")
	}
}
