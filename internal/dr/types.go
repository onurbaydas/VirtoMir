// Package dr coordinates disaster recovery (ADR-0069): protection groups replicate
// a set of machines from one host to another (through internal/zfsrep) with an RPO
// target, and recovery plans bring whole stacks back up — as a test in isolated
// networks or for real, with failback reversing the replication direction.
package dr

import "time"

// Status values of a protection group.
const (
	StatusProtected   = "protected"
	StatusRPOBreached = "rpo_breached"
	StatusSyncing     = "syncing"
	StatusFailedOver  = "failed_over"
	StatusError       = "error"
)

// StepStatus values of one step of an execution run.
const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// ExecutionMode is what an execution run does, ExecutionState what it ended up as.
const (
	ModeTestFailover ExecutionMode  = "test_failover"
	ModeTestCleanup  ExecutionMode  = "test_cleanup"
	ModeRealFailover ExecutionMode  = "real_failover"
	ModeFailback     ExecutionMode  = "failback"
	StateRunning     ExecutionState = "running"
	StateSucceeded   ExecutionState = "succeeded"
	StateFailed      ExecutionState = "failed"
	StateTestActive  ExecutionState = "test_active"
	StateTestCleaned ExecutionState = "test_cleaned"
)

// ProtectionGroup watches a set of machines that is replicated from one host to
// another, with an RPO target (how much data loss in minutes is tolerable) and a
// replication interval.
type ProtectionGroup struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Description         string    `json:"description,omitempty"`
	SourceHost          string    `json:"sourceHost"`
	TargetHost          string    `json:"targetHost"`
	ActiveHost          string    `json:"activeHost,omitempty"`
	TargetPool          string    `json:"targetPool"`
	VMUUIDs             []string  `json:"vmUuids"`
	RPOMinutes          int       `json:"rpoMinutes"`
	ReplicationInterval int       `json:"replicationInterval"`
	Enabled             bool      `json:"enabled"`
	Status              string    `json:"status"` // protected, rpo_breached, syncing, failed_over, error
	Created             time.Time `json:"created"`
	Updated             time.Time `json:"updated"`
}

// ReplicatedDisk is one disk of a machine and where its replica lives.
type ReplicatedDisk struct {
	Dev              string    `json:"dev"`
	SourcePool       string    `json:"sourcePool"`
	SourceVolume     string    `json:"sourceVolume"`
	TargetPool       string    `json:"targetPool"`
	TargetVolume     string    `json:"targetVolume"`
	LatestSnapshot   string    `json:"latestSnapshot,omitempty"`
	LastSync         time.Time `json:"lastSync,omitzero"`
	BytesTransferred uint64    `json:"bytesTransferred,omitempty"`
}

// VMDiskMapping records which replica disk belongs to which source machine, so
// start-from-replica knows what to clone and failback knows what to send back.
type VMDiskMapping struct {
	VMUUID     string           `json:"vmUuid"`
	DomainName string           `json:"domainName"`
	Disks      []ReplicatedDisk `json:"disks"`
}

// RPOStatus is a protection group's replication lag against its RPO target.
type RPOStatus struct {
	GroupID           string    `json:"groupId"`
	RPOMinutes        int       `json:"rpoMinutes"`
	LastSync          time.Time `json:"lastSync,omitzero"`
	LagMinutes        int       `json:"lagMinutes"`
	Breached          bool      `json:"breached"`
	CompliancePercent float64   `json:"compliancePercent"`
}

// NetworkMap sends the traffic of the source network to another one on the
// target; IsolatedTestNetwork keeps test-failover copies off the real networks.
type NetworkMap struct {
	SourceNetwork       string `json:"sourceNetwork"`
	TargetNetwork       string `json:"targetNetwork"`
	IsolatedTestNetwork string `json:"isolatedTestNetwork,omitempty"`
}

// IPConfig re-addresses one interface of a failover copy: the replica keeps the
// source machine's MAC, so it needs a new address before it may talk.
type IPConfig struct {
	InterfaceMAC string   `json:"interfaceMac,omitempty"`
	IPv4Address  string   `json:"ipv4Address"`
	IPv4Gateway  string   `json:"ipv4Gateway,omitempty"`
	Nameservers  []string `json:"nameservers,omitempty"`
}

// PlanVM is one machine inside a boot group of a recovery plan.
type PlanVM struct {
	VMUUID          string       `json:"vmUuid"`
	SourceName      string       `json:"sourceName"`
	DRVMName        string       `json:"drVmName,omitempty"`
	NetworkMappings []NetworkMap `json:"networkMappings,omitempty"`
	IPReaddressing  *IPConfig    `json:"ipReaddressing,omitempty"`
	StopOnError     bool         `json:"stopOnError"`
}

// BootGroup starts its machines together, at a given Order, DelaySeconds after
// the group before it (databases first, then middle tiers, then front ends).
type BootGroup struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Order        int      `json:"order"`
	DelaySeconds int      `json:"delaySeconds"`
	VMs          []PlanVM `json:"vms"`
}

// RecoveryPlan brings a protection group back up: which machines, in which
// order, on which networks, with hook scripts around the whole run.
type RecoveryPlan struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Description       string      `json:"description,omitempty"`
	ProtectionGroupID string      `json:"protectionGroupId"`
	BootGroups        []BootGroup `json:"bootGroups"`
	PreHookScript     string      `json:"preHookScript,omitempty"`
	PostHookScript    string      `json:"postHookScript,omitempty"`
	HookTimeoutSec    int         `json:"hookTimeoutSec,omitempty"`
	Created           time.Time   `json:"created"`
	Updated           time.Time   `json:"updated"`
}

// StepStatus is where one step of an execution run stands.
type StepStatus string

// ExecutionMode is what an execution run is doing; ExecutionState is where the
// run (or an active test) stands.
type ExecutionMode string
type ExecutionState string

// StepLog records how one step of an execution run went.
type StepLog struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	BootGroupID string     `json:"bootGroupId,omitempty"`
	VMUUID      string     `json:"vmUuid,omitempty"`
	DomainUUID  string     `json:"domainUuid,omitempty"` // the test domain the step defined, so cleanup can stop it
	Status      StepStatus `json:"status"`
	Output      string     `json:"output,omitempty"`
	Error       string     `json:"error,omitempty"`
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished,omitempty"`
}

// ExecutionRun is one run of a recovery plan (a test or the real thing), with
// every step it performed and how each went.
type ExecutionRun struct {
	ID           string         `json:"id"`
	PlanID       string         `json:"planId"`
	Mode         ExecutionMode  `json:"mode"`
	State        ExecutionState `json:"state"`
	Started      time.Time      `json:"started"`
	Finished     *time.Time     `json:"finished,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Steps        []StepLog      `json:"steps"`
}
