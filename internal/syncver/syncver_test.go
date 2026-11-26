package syncver

import (
	"encoding/json"
	"testing"
)

type shared struct {
	Version int      `json:"version"`
	Items   []string `json:"items"`
}

func TestTake(t *testing.T) {
	x := shared{Version: 4, Items: []string{"gpu-a"}}
	y := shared{Version: 4, Items: []string{"gpu-b"}}
	if !Take("t", 5, 4, y, x) || Take("t", 3, 4, y, x) {
		t.Error("versions alone decide when they differ")
	}
	if Take("t", 4, 4, x, x) {
		t.Error("an identical copy was taken")
	}
	// Equal versions, different contents: exactly one side takes the other's, the same
	// way on both members, so they end up equal.
	if Take("t", 4, 4, y, x) == Take("t", 4, 4, x, y) {
		t.Error("both or neither member took the other's copy")
	}
}

func TestTakeRawMessage(t *testing.T) {
	ours := json.RawMessage(`{"version":2,"vpcs":{"a":{"name":"A"}}}`)
	same := json.RawMessage("{\n  \"version\": 2,\n  \"vpcs\": {\"a\": {\"name\": \"A\"}}\n}")
	other := json.RawMessage(`{"version":2,"vpcs":{"b":{"name":"B"}}}`)
	if Take("sdn", 2, 2, same, ours) {
		t.Error("the same state, formatted differently, was taken")
	}
	if Take("sdn", 2, 2, other, ours) == Take("sdn", 2, 2, ours, other) {
		t.Error("no single winner")
	}
	if Take("sdn", 2, 2, json.RawMessage(`{broken`), ours) {
		t.Error("broken JSON was taken")
	}
}

func TestTakeIgnoresEmptyVersusMissing(t *testing.T) {
	a := map[string]any{"version": 3, "pools": nil, "labels": map[string]any{}}
	b := map[string]any{"version": 3, "pools": []string{}}
	if Take("t", 3, 3, a, b) || Take("t", 3, 3, b, a) {
		t.Error("empty and missing lists were told apart")
	}
}
