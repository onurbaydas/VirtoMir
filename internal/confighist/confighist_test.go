package confighist

import (
	"io"
	"log/slog"
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
