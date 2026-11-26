// Package syncver decides whether a cluster member takes another member's copy of a
// cluster-wide store (SDN, GPU pools, placement rules, DRS, HA, storage policies and tiers,
// physical networks).
//
// Each store has one version number that a change bumps. When two members change the same
// store at about the same time, both go from N to N+1 with different contents; comparing
// versions alone, each would refuse the other's copy and they would disagree until the
// next change (#47). For equal versions with different contents every member therefore
// keeps the copy whose content hash is larger: a choice all members make the same way, so
// they converge on one copy instead of drifting apart. The other change is lost, and that is
// logged.
package syncver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
)

// Take reports whether a member holding ours (at ourVersion) should take theirs (at
// theirVersion). ours and theirs are the store's shared form: what Shared() returns.
func Take(store string, theirVersion, ourVersion int, theirs, ours any) bool {
	switch {
	case theirVersion > ourVersion:
		return true
	case theirVersion < ourVersion:
		return false
	}
	a, errA := canonical(theirs)
	b, errB := canonical(ours)
	if errA != nil || errB != nil || bytes.Equal(a, b) {
		return false
	}
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	take := bytes.Compare(ha[:], hb[:]) > 0
	slog.Default().Warn("cluster-wide settings were changed on two members at once; all members keep the same one of the two changes",
		"store", store, "version", ourVersion, "taking_theirs", take)
	return take
}

// canonical is v as JSON in a form that does not depend on how a member happened to hold
// it: object keys sorted, and null, [] and {} values dropped (an empty list and a missing
// one mean the same here). Otherwise two equal copies could look different every minute.
func canonical(v any) ([]byte, error) {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(prune(generic))
}

func prune(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if e = prune(e); e != nil {
				out[k] = e
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(x) == 0 {
			return nil
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = prune(e)
		}
		return out
	}
	return v
}
