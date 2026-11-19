package api

import (
	"encoding/json"
	"testing"
)

func TestEmptyListsSendsNilSlicesAsEmpty(t *testing.T) {
	var jobs []string
	in := map[string]any{"jobs": jobs, "name": "x", "nothing": nil, "full": []int{1}}
	b, err := json.Marshal(emptyLists(in))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"full":[1],"jobs":[],"name":"x","nothing":null}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if in["jobs"] != nil && in["jobs"].([]string) != nil {
		t.Error("the caller's map must not change")
	}
	// Anything that is not a reply map passes through untouched.
	type s struct{ A []int }
	if v := emptyLists(s{}); v.(s).A != nil {
		t.Error("structs are left alone")
	}
}
