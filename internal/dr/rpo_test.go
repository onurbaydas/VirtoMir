package dr

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var rpoNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func rpoGroup(rpo, created int) ProtectionGroup {
	g := validGroup()
	g.RPOMinutes = rpo
	g.Created = rpoNow.Add(-time.Duration(created) * time.Minute)
	return g
}

func TestCalculateRPO(t *testing.T) {
	lastSync := rpoNow.Add(-10 * time.Minute)
	status := CalculateRPO(rpoGroup(15, 100), lastSync, rpoNow)
	if status.LagMinutes != 10 {
		t.Errorf("lag %d minutes, want 10", status.LagMinutes)
	}
	if status.Breached {
		t.Error("tagged as breached within the RPO target")
	}
	if status.CompliancePercent != 100.0 {
		t.Errorf("compliance %g, want 100 (ahead of target clamps at 100)", status.CompliancePercent)
	}
	if !status.LastSync.Equal(lastSync) {
		t.Errorf("last sync not kept: %v", status.LastSync)
	}

	// Breach just past the boundary.
	status = CalculateRPO(rpoGroup(15, 100), rpoNow.Add(-16*time.Minute), rpoNow)
	if !status.Breached || status.LagMinutes != 16 {
		t.Errorf("breach at 16 lag: breached=%v lag=%d", status.Breached, status.LagMinutes)
	}
	// At exactly the target: not breached.
	status = CalculateRPO(rpoGroup(15, 100), rpoNow.Add(-15*time.Minute), rpoNow)
	if status.Breached || status.CompliancePercent != 100.0 {
		t.Errorf("boundary at 15 lag: breached=%v compliance=%g", status.Breached, status.CompliancePercent)
	}

	// No last sync: lag counts from the group's creation.
	status = CalculateRPO(rpoGroup(30, 45), time.Time{}, rpoNow)
	if status.LagMinutes != 45 || !status.Breached {
		t.Errorf("lag from creation: lag=%d breached=%v", status.LagMinutes, status.Breached)
	}
	if !status.LastSync.IsZero() {
		t.Error("a zero last sync should stay zero")
	}

	// Fresh group with no sync yet at all is compliant until the RPO passes.
	status = CalculateRPO(rpoGroup(30, 5), time.Time{}, rpoNow)
	if status.LagMinutes != 5 || status.Breached {
		t.Errorf("fresh group: lag=%d breached=%v", status.LagMinutes, status.Breached)
	}

	// Compliance never drops below 0.
	status = CalculateRPO(rpoGroup(15, 100), rpoNow.Add(-3*time.Hour), rpoNow)
	if gc := status.CompliancePercent; gc < 0 || gc > 15.0/180.0*100.0 {
		t.Errorf("compliance out of range: %g", gc)
	}

	// Lag 0 (synced just now): no division by zero, fully within target.
	status = CalculateRPO(rpoGroup(15, 100), rpoNow, rpoNow)
	if status.LagMinutes != 0 || status.CompliancePercent != 100.0 {
		t.Errorf("zero lag: lag=%d compliance=%g", status.LagMinutes, status.CompliancePercent)
	}
}

type testNotifier struct {
	messages []string
}

func (n *testNotifier) Notice(subject, message string) {
	n.messages = append(n.messages, subject+"\x00"+message)
}

func TestCheckRPOCompliance(t *testing.T) {
	path := ""
	s, err := Open(path) // empty path never writes; the store functions without a file
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	breached, _ := s.CreateProtectionGroup(rpoGroup(15, 2*100)) // long past its RPO
	healthy, _ := s.CreateProtectionGroup(func() ProtectionGroup {
		g := rpoGroup(60, 100)
		g.Name = "healthy"
		g.VMUUIDs = []string{"e5a1a2f1-8c37-4a0e-9d52-3d0c8a1d93f1"}
		return g
	}())

	lastSyncs := map[string]time.Time{
		breached.ID: rpoNow.Add(-90 * time.Minute),
		healthy.ID:  rpoNow.Add(-5 * time.Minute),
	}
	fn := func(group ProtectionGroup) (time.Time, error) { return lastSyncs[group.ID], nil }
	notify := &testNotifier{}

	statuses := CheckRPOCompliance(s, fn, notify, rpoNow)
	if len(statuses) != 2 {
		t.Fatalf("expected 2 RPO statuses, got %d", len(statuses))
	}
	if len(notify.messages) != 1 {
		t.Fatalf("expected one notice, got %v", notify.messages)
	}
	subject, message, _ := strings.Cut(notify.messages[0], "\x00")
	if subject != "dr:rpo:"+breached.ID {
		t.Errorf("notice subject %q", subject)
	}
	if !strings.Contains(message, breached.Name) || !strings.Contains(message, "90 minutes") {
		t.Errorf("notice message %q", message)
	}
	if got, _ := s.GetProtectionGroup(breached.ID); got.Status != StatusRPOBreached {
		t.Errorf("breached group status %q", got.Status)
	}
	if got, _ := s.GetProtectionGroup(healthy.ID); got.Status != StatusProtected {
		t.Errorf("healthy group status %q", got.Status)
	}
	for _, st := range statuses {
		if st.GroupID == breached.ID && (!st.Breached || st.LagMinutes != 90) {
			t.Errorf("breached status: %+v", st)
		}
	}

	// A group the replication layer cannot answer for is marked error and left
	// out of the status list.
	delete(lastSyncs, breached.ID)
	fn = func(group ProtectionGroup) (time.Time, error) {
		t, ok := lastSyncs[group.ID]
		if !ok {
			return time.Time{}, errors.New("no data")
		}
		return t, nil
	}
	notify.messages = nil
	statuses = CheckRPOCompliance(s, fn, notify, rpoNow)
	if len(statuses) != 1 || statuses[0].GroupID != healthy.ID {
		t.Errorf("unknown-sync group not skipped: %+v", statuses)
	}
	if len(notify.messages) != 0 {
		t.Errorf("unexpected notices: %v", notify.messages)
	}
	if got, _ := s.GetProtectionGroup(breached.ID); got.Status != StatusError {
		t.Errorf("group with no replication data has status %q, want %q", got.Status, StatusError)
	}

	// Once it syncs again, a breached group returns to protected — and stays there.
	lastSyncs[breached.ID] = rpoNow.Add(-2 * time.Minute)
	fn = func(group ProtectionGroup) (time.Time, error) { return lastSyncs[group.ID], nil }
	CheckRPOCompliance(s, fn, notify, rpoNow)
	if got, _ := s.GetProtectionGroup(breached.ID); got.Status != StatusProtected {
		t.Errorf("recovered group status %q", got.Status)
	}
	notify.messages = nil
	CheckRPOCompliance(s, fn, notify, rpoNow)
	if len(notify.messages) != 0 {
		t.Errorf("notices for a healthy group: %v", notify.messages)
	}
}

func TestCheckRPOComplianceSkipsDisabled(t *testing.T) {
	s, _ := Open("")
	group, _ := s.CreateProtectionGroup(rpoGroup(15, 200))
	group.Enabled = false
	_ = s.UpdateProtectionGroup(group)
	notify := &testNotifier{}
	statuses := CheckRPOCompliance(s, func(group ProtectionGroup) (time.Time, error) { return time.Time{}, nil }, notify, rpoNow)
	if len(statuses) != 0 {
		t.Errorf("disabled group produced statuses: %+v", statuses)
	}
	if len(notify.messages) != 0 {
		t.Errorf("disabled group produced notices: %v", notify.messages)
	}
}
