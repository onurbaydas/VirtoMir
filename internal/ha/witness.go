package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A plain TCP witness gives its vote to everyone who reaches it: when two members lose
// sight of each other but both still reach the witness, both count its vote, both believe
// they have quorum and both may start the same machines (#22). A lease witness gives its
// vote to one member at a time: a member counts it only while it holds the witness's lease,
// and renews it with every heartbeat. The other member is refused until the lease has run
// out, so at most one side of a split ever has the third vote.
//
// The witness is `virtomird -witness :8421` on a third machine; members name it as
// "lease://host:8421" (or "lease://host:8421/<cluster>" when one witness serves several
// clusters). It speaks plain HTTP: run it on the management network.

const leasePrefix = "lease://"

// MaxLeaseTTL caps what a member may ask for, so a member that dies holding the lease
// blocks the others for a bounded time.
const MaxLeaseTTL = time.Minute

// witnessTarget splits a witness setting into its kind ("tcp" or "lease"), address and,
// for a lease witness, the cluster's group name.
func witnessTarget(w string) (kind, addr, group string) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(w), leasePrefix)
	if !ok {
		return "tcp", strings.TrimSpace(w), ""
	}
	addr, group, _ = strings.Cut(rest, "/")
	if group == "" {
		group = "default"
	}
	return "lease", addr, group
}

// IsLeaseWitness reports whether a witness setting names a lease witness.
func IsLeaseWitness(w string) bool { k, _, _ := witnessTarget(w); return k == "lease" }

func validWitness(w string) error {
	kind, addr, group := witnessTarget(w)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		if kind == "lease" {
			return errors.New(`a lease witness is "lease://host:port" (the port virtomird -witness listens on, e.g. 8421)`)
		}
		return errors.New(`the witness must be "host:port" (any TCP port it answers on, e.g. 22), or "lease://host:port" for a lease witness`)
	}
	if kind == "lease" && strings.ContainsAny(group, "/?# ") {
		return errors.New(`the cluster name after the witness address may not contain "/", "?", "#" or spaces`)
	}
	return nil
}

// leaseRequest asks the witness for its vote (or to renew it) for ttl.
type leaseRequest struct {
	Group  string `json:"group"`
	Holder string `json:"holder"`
	TTLms  int64  `json:"ttlMs"`
}

// leaseAnswer says who holds the vote and for how much longer.
type leaseAnswer struct {
	Granted   bool   `json:"granted"`
	Holder    string `json:"holder"`
	ExpiresMs int64  `json:"expiresInMs"`
}

type lease struct {
	holder  string
	expires time.Time
}

// LeaseWitness is the witness side: one lease per group, held by one member at a time.
type LeaseWitness struct {
	mu     sync.Mutex
	leases map[string]lease
	now    func() time.Time
}

// NewLeaseWitness returns an empty witness.
func NewLeaseWitness() *LeaseWitness {
	return &LeaseWitness{leases: map[string]lease{}, now: time.Now}
}

// acquire grants or renews the lease of group for holder, unless another holder has it.
func (w *LeaseWitness) acquire(group, holder string, ttl time.Duration) leaseAnswer {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	l, ok := w.leases[group]
	if ok && l.holder != holder && now.Before(l.expires) {
		return leaseAnswer{Holder: l.holder, ExpiresMs: l.expires.Sub(now).Milliseconds()}
	}
	l = lease{holder: holder, expires: now.Add(ttl)}
	w.leases[group] = l
	return leaseAnswer{Granted: true, Holder: holder, ExpiresMs: ttl.Milliseconds()}
}

// ServeHTTP answers POST /v1/lease.
func (w *LeaseWitness) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/lease" || r.Method != http.MethodPost {
		http.Error(rw, "VirtoMir HA witness: POST /v1/lease", http.StatusNotFound)
		return
	}
	var req leaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096)).Decode(&req); err != nil ||
		req.Group == "" || req.Holder == "" || req.TTLms <= 0 {
		http.Error(rw, "need group, holder and ttlMs", http.StatusBadRequest)
		return
	}
	ttl := min(time.Duration(req.TTLms)*time.Millisecond, MaxLeaseTTL)
	ans := w.acquire(req.Group, req.Holder, ttl)
	rw.Header().Set("Content-Type", "application/json")
	if !ans.Granted {
		rw.WriteHeader(http.StatusConflict)
	}
	_ = json.NewEncoder(rw).Encode(ans)
}

// witnessClient goes straight to the witness: a host's HTTP proxy settings must not put a
// machine between the members and their tie-breaker.
var witnessClient = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

// acquireLease asks the witness at addr for group's lease for holder. It answers true only
// when the witness granted it; any error or refusal is no vote.
func acquireLease(ctx context.Context, addr, group, holder string, ttl time.Duration) (bool, error) {
	body, _ := json.Marshal(leaseRequest{Group: group, Holder: holder, TTLms: ttl.Milliseconds()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/lease", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := witnessClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var ans leaseAnswer
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4096)).Decode(&ans); err != nil {
		return false, fmt.Errorf("witness %s: %s", addr, resp.Status)
	}
	return resp.StatusCode == http.StatusOK && ans.Granted && ans.Holder == holder, nil
}

// leaseTTL is how long a member asks to hold the lease: several heartbeats, so one late
// renewal does not lose it, and never less than ten seconds.
func leaseTTL(interval time.Duration) time.Duration {
	return min(max(3*interval, 10*time.Second), MaxLeaseTTL)
}
