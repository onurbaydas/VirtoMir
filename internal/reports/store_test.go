package reports

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestListAndDelete(t *testing.T) {
	s, now := open(t)
	first, err := s.Generate(Capacity, HTML, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(time.Minute) }
	second, err := s.Generate(Inventory, CSV, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	if l := s.List(); len(l) != 2 || l[0].ID != second.ID || l[1].ID != first.ID {
		t.Fatalf("List = %v, want newest first", l)
	}

	if err := s.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.file(first)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the report's file is still there: %v", err)
	}
	if l := s.List(); len(l) != 1 || l[0].ID != second.ID {
		t.Errorf("List after Delete = %v", l)
	}
	if err := s.Delete("no-such-report"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Delete of an unknown ID = %v", err)
	}

	// The list survives opening the store again.
	again, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if l := again.List(); len(l) != 1 || l[0].ID != second.ID {
		t.Errorf("reopened List = %v", l)
	}
}

func TestSchedulesAndDeleteSchedule(t *testing.T) {
	s, now := open(t)
	on, err := s.PutSchedule(Schedule{Name: "Daily inventory", Kind: Inventory, Format: CSV, Every: "daily", At: "06:00", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := s.PutSchedule(Schedule{Name: "Paused", Kind: Capacity, Format: HTML, Every: "weekly", At: "06:00"})
	if err != nil {
		t.Fatal(err)
	}
	list := s.Schedules()
	if len(list) != 2 {
		t.Fatalf("Schedules = %v", list)
	}
	for _, sc := range list {
		switch sc.ID {
		case on.ID:
			// 2026-10-16 09:00: the next daily 06:00 is tomorrow.
			if sc.NextRun == nil || !sc.NextRun.Equal(on.Next(now)) || sc.NextRun.Day() != 17 {
				t.Errorf("next run of the enabled schedule = %v", sc.NextRun)
			}
		case off.ID:
			if sc.NextRun != nil {
				t.Errorf("a disabled schedule has a next run: %v", sc.NextRun)
			}
		}
	}

	if err := s.DeleteSchedule(on.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule("rs-unknown"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DeleteSchedule of an unknown ID = %v", err)
	}
	again, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if l := again.Schedules(); len(l) != 1 || l[0].ID != off.ID {
		t.Errorf("after reopening: %v", l)
	}
}
