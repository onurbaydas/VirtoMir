package confighist

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeSrc struct{ xml map[string]string }

func (f *fakeSrc) DomainUUIDs() ([]string, error) {
	var out []string
	for u := range f.xml {
		out = append(out, u)
	}
	return out, nil
}
func (f *fakeSrc) DomainXML(u string, _ bool) (string, error) { return f.xml[u], nil }

const u1 = "2d860e5c-844a-4c8a-bd06-282d61d6ea52"

func TestCaptureOnlyChanges(t *testing.T) {
	src := &fakeSrc{xml: map[string]string{u1: "<domain>v1</domain>"}}
	s, err := Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }

	s.Sweep()
	s.Sweep() // unchanged: nothing new
	src.xml[u1] = "<domain>v2</domain>"
	if err := s.Capture(u1, "onur"); err != nil {
		t.Fatal(err)
	}
	src.xml[u1] = "<domain>v3</domain>"
	s.Sweep()

	vs, err := s.List(u1)
	if err != nil || len(vs) != 3 {
		t.Fatalf("List = %+v, %v", vs, err)
	}
	if vs[0].Source != "outside" || vs[1].Source != "virtomir" || vs[1].User != "onur" || vs[2].Source != "first seen" {
		t.Errorf("sources/users: %+v", vs)
	}
	if x, err := s.Get(u1, vs[1].ID); err != nil || x != "<domain>v2</domain>" {
		t.Errorf("Get = %q, %v", x, err)
	}
	if _, err := s.Get(u1, "../../etc/passwd"); err == nil {
		t.Error("path escape accepted")
	}
	if _, err := s.List("../x"); err == nil {
		t.Error("bad uuid accepted")
	}
}

func TestPrune(t *testing.T) {
	src := &fakeSrc{xml: map[string]string{}}
	s, _ := Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for i := 0; i < Keep+5; i++ {
		src.xml[u1] = "<domain>" + time.Duration(i).String() + "</domain>"
		_ = s.Capture(u1, "x")
	}
	vs, _ := s.List(u1)
	if len(vs) != Keep {
		t.Errorf("kept %d versions, want %d", len(vs), Keep)
	}
}

func quietStore(t *testing.T, src Source) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The clock is set back (by hand, by NTP, a dead battery at boot): the versions saved after
// that still come after the ones saved before, and the right one is compared against when
// the daemon starts again.
func TestVersionsKeepTheirOrderWhenTheClockGoesBack(t *testing.T) {
	src := &fakeSrc{xml: map[string]string{u1: "<domain>v1</domain>"}}
	s := quietStore(t, src)
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return clock }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Capture(u1, "onur"))
	clock = clock.Add(-48 * time.Hour) // two days back
	src.xml[u1] = "<domain>v2</domain>"
	must(s.Capture(u1, "onur"))
	src.xml[u1] = "<domain>v3</domain>"
	must(s.Capture(u1, "onur")) // the same instant as v2

	vs, _ := s.List(u1)
	if len(vs) != 3 {
		t.Fatalf("versions: %+v", vs)
	}
	for i, want := range []string{"<domain>v3</domain>", "<domain>v2</domain>", "<domain>v1</domain>"} {
		if x, _ := s.Get(u1, vs[i].ID); x != want {
			t.Errorf("version %d is %q, want %q", i, x, want)
		}
	}
	if !vs[0].Time.Before(vs[2].Time) {
		t.Errorf("the saved time is the clock's, wrong or not: %v, %v", vs[0].Time, vs[2].Time)
	}

	// A new daemon on the same folder compares against v3, the newest: nothing is saved twice.
	again, err := Open(s.dir, src, s.log)
	must(err)
	again.now = s.now
	again.Sweep()
	if vs, _ := again.List(u1); len(vs) != 3 {
		t.Errorf("after a restart: %d versions", len(vs))
	}
}

// A version whose note is gone or torn is still listed, with the time its ID holds.
func TestVersionWithoutItsNote(t *testing.T) {
	src := &fakeSrc{xml: map[string]string{u1: "<domain>v1</domain>"}}
	s := quietStore(t, src)
	at := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return at }
	if err := s.Capture(u1, "onur"); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.List(u1)
	note := filepath.Join(s.dir, u1, vs[0].ID+".json")
	for name, damage := range map[string]func() error{
		"torn":    func() error { return os.WriteFile(note, []byte(`{"id":"`), 0o640) },
		"empty":   func() error { return os.WriteFile(note, nil, 0o640) },
		"another": func() error { return os.WriteFile(note, []byte(`{"id":"1","time":"2020-01-01T00:00:00Z"}`), 0o640) },
		"missing": func() error { return os.Remove(note) },
	} {
		if err := damage(); err != nil {
			t.Fatal(err)
		}
		got, _ := s.List(u1)
		if len(got) != 1 || got[0].ID != vs[0].ID || !got[0].Time.Equal(at) || got[0].Size != len("<domain>v1</domain>") || got[0].Source != "outside" {
			t.Errorf("%s note: %+v", name, got)
		}
	}
}

// An empty answer is not a definition: it is neither saved nor taken for "nothing changed".
func TestEmptyDefinitionIsNotAVersion(t *testing.T) {
	src := &fakeSrc{xml: map[string]string{u1: "<domain>v1</domain>"}}
	s := quietStore(t, src)
	if err := s.Capture(u1, "onur"); err != nil {
		t.Fatal(err)
	}
	src.xml[u1] = " \n"
	if err := s.CaptureVia(u1, "", ViaScan); err == nil {
		t.Error("an empty definition was accepted")
	}
	src.xml[u1] = "<domain>v1</domain>"
	s.Sweep()
	if vs, _ := s.List(u1); len(vs) != 1 {
		t.Errorf("versions: %+v", vs)
	}
}

// Deleted machines are forgotten in memory; their versions stay on disk.
func TestSweepForgetsMachinesThatAreGone(t *testing.T) {
	const u2 = "9f1c2b3a-0000-4c8a-bd06-282d61d6ea52"
	src := &fakeSrc{xml: map[string]string{u1: "<domain>a</domain>", u2: "<domain>b</domain>"}}
	s := quietStore(t, src)
	_ = s.Capture(u1, "onur")
	_ = s.Capture(u2, "onur")
	delete(src.xml, u2)
	s.Sweep()
	s.mu.Lock()
	_, cached := s.last[u2]
	_, api := s.byAPI[u2]
	_, kept := s.last[u1]
	s.mu.Unlock()
	if cached || api || !kept {
		t.Errorf("cached=%v api=%v kept=%v", cached, api, kept)
	}
	if vs, _ := s.List(u2); len(vs) != 1 {
		t.Errorf("the deleted machine's versions: %+v", vs)
	}
}
