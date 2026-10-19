package project

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mkProject(id, name string, members ...Member) Project {
	return Project{
		ID:      id,
		Name:    name,
		Members: members,
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		p    Project
		want string // empty: must fail with no specific message check
	}{
		{"valid", mkProject("lab", "Lab"), ""},
		{"id too short", mkProject("ab", "A B"), "project ID"},
		{"id too long", mkProject(strings.Repeat("a", 41), "Long"), "project ID"},
		{"id uppercase", mkProject("Lab", "Upper"), "project ID"},
		{"id leading dash", mkProject("-lab", "Dash"), "project ID"},
		{"id trailing dash", mkProject("lab-", "Dash"), "project ID"},
		{"id underscore", mkProject("la_b", "US"), "project ID"},
		{"no name", mkProject("lab", "  "), "name"},
		{"bad member kind", mkProject("lab", "Lab", Member{"team", "alice", "viewer"}), "member"},
		{"bad role", mkProject("lab", "Lab", Member{"user", "alice", "boss"}), "member"},
		{"empty member name", mkProject("lab", "Lab", Member{"user", "  ", "admin"}), "member"},
		{"duplicate member", mkProject("lab", "Lab", Member{"user", "alice", "viewer"}, Member{"user", "alice", "admin"}), "twice"},
		{"same name different kind ok", mkProject("lab", "Lab", Member{"user", "alice", "viewer"}, Member{"group", "alice", "admin"}), ""},
		{"negative limits", func() Project {
			p := mkProject("lab", "Lab")
			p.Limits.Machines = -1
			return p
		}(), "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := testStore(t)
			_, err := s.Create(c.p)
			if c.want == "" {
				if err != nil {
					t.Fatalf("expected project %q to be created, got %v", c.p.ID, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected creation of %q to fail", c.p.ID)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestCreateDuplicate(t *testing.T) {
	s := testStore(t)
	if _, err := s.Create(mkProject("lab", "Lab")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(mkProject("lab2", "lab")); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("duplicate name must be rejected, got %v", err)
	}
	if _, err := s.Create(mkProject("lab", "Other")); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("duplicate ID must be rejected, got %v", err)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	p := mkProject("lab", "Lab",
		Member{"user", "alice", "admin"},
		Member{"group", "devs", "operator"},
	)
	p.Description = "the lab"
	p.Limits = Limits{Machines: 10, VCPUs: 40, MemoryMiB: 65536, DiskGiB: 500}
	p.StoragePools = []string{"default"}
	p.Networks = []string{"default"}
	p.Created = created
	if _, err := s.Create(p); err != nil {
		t.Fatal(err)
	}

	// Round-trip: reopen from disk.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("lab")
	if !ok {
		t.Fatal("project lost after reopen")
	}
	if got.Name != "Lab" || got.Description != "the lab" || got.Created != created {
		t.Fatalf("round-trip changed the project: %+v", got)
	}
	if len(got.Members) != 2 || got.Members[0] != (Member{"user", "alice", "admin"}) || got.Members[1] != (Member{"group", "devs", "operator"}) {
		t.Fatalf("members did not survive: %+v", got.Members)
	}
	if !reflect.DeepEqual(got.Limits, Limits{Machines: 10, VCPUs: 40, MemoryMiB: 65536, DiskGiB: 500}) {
		t.Fatalf("limits did not survive: %+v", got.Limits)
	}

	// Delete persists too.
	if err := s2.Delete("lab"); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.Get("lab"); ok {
		t.Fatal("deleted project came back")
	}
}

func TestUpdate(t *testing.T) {
	s := testStore(t)
	if _, err := s.Create(mkProject("lab", "Lab", Member{"user", "alice", "viewer"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(mkProject("prod", "Prod")); err != nil {
		t.Fatal(err)
	}
	// Update an absent project.
	if err := s.Update(mkProject("nope", "Nope")); err == nil {
		t.Fatal("updating an absent project must fail")
	}
	// Rename onto an existing name.
	if err := s.Update(mkProject("lab", "Prod")); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("duplicate name on update must be rejected, got %v", err)
	}
	// Change name, description and members.
	next := mkProject("lab", "Laboratory", Member{"user", "alice", "admin"})
	next.Description = "renamed"
	if err := s.Update(next); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("lab")
	if got.Name != "Laboratory" || got.Description != "renamed" || len(got.Members) != 1 || got.Members[0].Role != "admin" {
		t.Fatalf("update did not stick: %+v", got)
	}
}

func TestListSorted(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if _, err := s.Create(mkProject(id, strings.ToUpper(id))); err != nil {
			t.Fatal(err)
		}
	}
	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(list))
	}
	if list[0].ID != "alpha" || list[1].ID != "mid" || list[2].ID != "zeta" {
		t.Fatalf("List is not sorted by ID: %+v", list)
	}
}

func TestRoleFor(t *testing.T) {
	s := testStore(t)
	p := mkProject("lab", "Lab",
		Member{"user", "alice", "viewer"},
		Member{"group", "devs", "operator"},
		Member{"group", "admins", "admin"},
	)
	if _, err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		user   string
		groups []string
		want   string
		wantOK bool
	}{
		{"direct", "alice", nil, "viewer", true},
		{"via group", "bob", []string{"devs"}, "operator", true},
		{"highest role wins", "carol", []string{"devs", "admins"}, "admin", true},
		{"highest role wins reversed", "carol", []string{"admins", "devs"}, "admin", true},
		{"no membership", "dave", []string{"hr"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			role, ok := s.RoleFor("lab", c.user, c.groups)
			if ok != c.wantOK || role != c.want {
				t.Fatalf("RoleFor(%q, %v) = (%q, %v), want (%q, %v)", c.user, c.groups, role, ok, c.want, c.wantOK)
			}
		})
	}
	// An absent project has no roles.
	if role, ok := s.RoleFor("nope", "alice", nil); ok || role != "" {
		t.Fatalf("RoleFor on an absent project = (%q, %v)", role, ok)
	}
}

func TestProjectsFor(t *testing.T) {
	s := testStore(t)
	if _, err := s.Create(mkProject("lab", "Lab", Member{"group", "devs", "operator"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(mkProject("prod", "Prod", Member{"user", "alice", "admin"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(mkProject("people", "HR", Member{"group", "hr", "viewer"})); err != nil {
		t.Fatal(err)
	}
	// A developer sees lab only; alice sees lab (via group) and prod directly.
	dev := s.ProjectsFor("bob", []string{"devs"})
	if len(dev) != 1 || dev[0].ID != "lab" {
		t.Fatalf("ProjectsFor(bob, devs) = %+v", dev)
	}
	alice := s.ProjectsFor("alice", []string{"devs"})
	if len(alice) != 2 || alice[0].ID != "lab" || alice[1].ID != "prod" {
		t.Fatalf("ProjectsFor(alice, devs) = %+v", alice)
	}
	if none := s.ProjectsFor("eve", []string{"sales"}); len(none) != 0 {
		t.Fatalf("ProjectsFor(eve, sales) = %+v", none)
	}
}

func TestLimitsExceeded(t *testing.T) {
	l := Limits{Machines: 4, VCPUs: 8, MemoryMiB: 16384, DiskGiB: 100}
	usage := Usage{Machines: 2, VCPUs: 4, MemoryMiB: 8192, DiskGiB: 50}
	cases := []struct {
		name   string
		demand Usage
		want   string // empty: within limits
	}{
		{"within", Usage{Machines: 1, VCPUs: 2, MemoryMiB: 2048, DiskGiB: 10}, ""},
		{"exactly at the limit", Usage{Machines: 2, VCPUs: 4, MemoryMiB: 8192, DiskGiB: 50}, ""},
		{"machines", Usage{Machines: 3}, "machines"},
		{"vcpus", Usage{VCPUs: 5}, "vCPUs"},
		{"memory", Usage{MemoryMiB: 8193}, "memory"},
		{"disk", Usage{DiskGiB: 51}, "disk space"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := l.Exceeded(usage, c.demand)
			if c.want == "" {
				if err != nil {
					t.Fatalf("within limits, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s to be exceeded", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
	// Zero limits are unlimited.
	if err := (Limits{}).Exceeded(usage, Usage{Machines: 1000, VCPUs: 1000, MemoryMiB: 1 << 40, DiskGiB: 1 << 40}); err != nil {
		t.Fatalf("unlimited project rejected demand: %v", err)
	}
	// Limits validate through Create as well.
	s := testStore(t)
	bad := mkProject("lab", "Lab")
	bad.Limits.MemoryMiB = -1
	if _, err := s.Create(bad); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative limits must be rejected, got %v", err)
	}
}

func TestOpenBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("a broken projects.json must fail to open")
	}
}

func TestOpenRejectsBadData(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			"duplicate IDs",
			`[{"id":"lab","name":"Lab"},{"id":"lab","name":"Other"}]`,
			`"lab" appears twice`,
		},
		{
			"duplicate names",
			`[{"id":"lab","name":"Lab"},{"id":"lab2","name":"lab"}]`,
			"exists already",
		},
		{
			"invalid member",
			`[{"id":"lab","name":"Lab","members":[{"kind":"team","name":"alice","role":"viewer"}]}]`,
			"member",
		},
		{
			"negative limit",
			`[{"id":"lab","name":"Lab","limits":{"machines":-1}}]`,
			"negative",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "projects.json")
			if err := os.WriteFile(path, []byte(c.json), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Open(path)
			if err == nil {
				t.Fatal("Open must reject invalid projects.json")
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "lab") {
				t.Fatalf("error %q does not name the bad project and mention %q", err, c.want)
			}
		})
	}
}

func TestWhitespaceTrimmed(t *testing.T) {
	s := testStore(t)
	p := mkProject("lab", "  Lab  ", Member{"user", "  alice  ", "admin"})
	if _, err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("lab")
	if got.Name != "Lab" {
		t.Fatalf("project name was not trimmed: %q", got.Name)
	}
	if len(got.Members) != 1 || got.Members[0].Name != "alice" {
		t.Fatalf("member name was not trimmed: %+v", got.Members)
	}
	// RoleFor resolves by the trimmed name.
	if role, ok := s.RoleFor("lab", "alice", nil); !ok || role != "admin" {
		t.Fatalf("RoleFor(alice) = (%q, %v)", role, ok)
	}
	next := mkProject("lab", "Laboratory", Member{"group", " devs ", "operator"})
	if err := s.Update(next); err != nil {
		t.Fatal(err)
	}
	if role, ok := s.RoleFor("lab", "bob", []string{"devs"}); !ok || role != "operator" {
		t.Fatalf("RoleFor(bob, devs) after trimmed update = (%q, %v)", role, ok)
	}
	// Open trims and validates the same way.
	path := filepath.Join(t.TempDir(), "projects.json")
	if err := os.WriteFile(path, []byte(`[{"id":"lab","name":"Lab","members":[{"kind":"user","name":" alice ","role":"admin"}]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := s2.RoleFor("lab", "alice", nil); !ok || role != "admin" {
		t.Fatalf("RoleFor(alice) after Open = (%q, %v)", role, ok)
	}
}

func TestConcurrent(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				id := fmt.Sprintf("p-%d-%d", w, i)
				if _, err := s.Create(mkProject(id, id, Member{"user", "alice", "viewer"})); err != nil {
					t.Errorf("Create(%s): %v", id, err)
					return
				}
				if _, ok := s.Get(id); !ok {
					t.Errorf("Get(%s) after Create failed", id)
					return
				}
				if list := s.ProjectsFor("alice", nil); len(list) == 0 {
					t.Errorf("ProjectsFor(alice) is empty")
					return
				}
				if _, ok := s.RoleFor(id, "alice", nil); !ok {
					t.Errorf("RoleFor(%s, alice) failed", id)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if list := s.List(); len(list) != 8*25 {
		t.Fatalf("expected %d projects, got %d", 8*25, len(list))
	}
}

func TestLimitsExceededGPUs(t *testing.T) {
	l := Limits{GPUs: map[string]int{"l40s": 4, "a100": 0}}
	for _, tc := range []struct {
		name          string
		used, demand  map[string]int
		wantErr, pool string
	}{
		{"under", map[string]int{"l40s": 2}, map[string]int{"l40s": 2}, "", ""},
		{"over", map[string]int{"l40s": 3}, map[string]int{"l40s": 2}, "GPU quota of 4", "l40s"},
		{"unlimited pool", nil, map[string]int{"a100": 64}, "", ""},
		{"pool not named", nil, map[string]int{"t4": 8}, "", ""},
		{"no GPU asked", map[string]int{"l40s": 9}, nil, "", ""},
	} {
		err := l.Exceeded(Usage{GPUs: tc.used}, Usage{GPUs: tc.demand})
		if (err != nil) != (tc.wantErr != "") || (err != nil && (!strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), tc.pool))) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if (Limits{}).Exceeded(Usage{}, Usage{GPUs: map[string]int{"x": 1}}) != nil {
		t.Error("a project without GPU quotas limited GPUs")
	}
}
