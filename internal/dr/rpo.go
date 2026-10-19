package dr

import (
	"fmt"
	"math"
	"time"
)

// AlertNotifier delivers news (not alerts): nothing but ENGINE.Notice, so DR can
// post through the alert channels (internal/alerts).
type AlertNotifier interface {
	Notice(subject, message string)
}

// CalculateRPO computes how far a group's replication lags behind its RPO
// target: without a last sync it counts from the group's creation, otherwise
// from the last successful sync. Compliance is how close the lag is to the
// target (100 % means within it, 0 % unusably late).
func CalculateRPO(group ProtectionGroup, lastSync, now time.Time) RPOStatus {
	from := group.Created
	if !lastSync.IsZero() {
		from = lastSync
	}
	lag := int(now.Sub(from).Minutes())
	compliance := math.Max(0.0, math.Min(100.0, float64(group.RPOMinutes)/float64(max(1, lag))*100.0))
	return RPOStatus{
		GroupID:           group.ID,
		RPOMinutes:        group.RPOMinutes,
		LastSync:          lastSync,
		LagMinutes:        lag,
		Breached:          lag > group.RPOMinutes,
		CompliancePercent: compliance,
	}
}

// CheckRPOCompliance runs the RPO check over every enabled protection group:
// a breached group is marked rpo_breached and a notice is sent, a compliant
// group is marked protected again. The getReplicationLastSync hook asks the
// replication layer (internal/zfsrep) when the group last synced.
func CheckRPOCompliance(store *Store, getReplicationLastSync func(group ProtectionGroup) (time.Time, error), notifier AlertNotifier, now time.Time) []RPOStatus {
	var out []RPOStatus
	for _, group := range store.ListProtectionGroups() {
		if !group.Enabled {
			continue
		}
		lastSync, err := getReplicationLastSync(group)
		if err != nil {
			// The replication layer cannot answer for this group: surface it as an
			// error instead of silently showing no status at all.
			if group.Status != StatusError {
				_ = store.setStatus(group.ID, StatusError)
			}
			continue
		}
		status := CalculateRPO(group, lastSync, now)
		switch {
		case status.Breached:
			if group.Status != StatusRPOBreached {
				_ = store.setStatus(group.ID, StatusRPOBreached)
			}
			notifier.Notice(fmt.Sprintf("dr:rpo:%s", group.ID),
				fmt.Sprintf("RPO breached for protection group %q: replication lag is %d minutes (target: %d minutes)", group.Name, status.LagMinutes, group.RPOMinutes))
		case group.Status == StatusRPOBreached || group.Status == StatusError:
			_ = store.setStatus(group.ID, StatusProtected)
		}
		out = append(out, status)
	}
	return out
}
