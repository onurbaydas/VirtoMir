package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
)

func TestReplayDoesAWriteOnce(t *testing.T) {
	var runs atomic.Int32
	s := &Server{}
	h := s.replay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) > 1 && r.Method == http.MethodPost {
			http.Error(w, `{"error":"network 'lab' already exists"}`, http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"name":"lab"}`))
	}))
	do := func(method, user, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/networks", nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey, auth.Identity{Name: user}))
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	first := do("POST", "onur", "k1")
	again := do("POST", "onur", "k1") // the browser lost the first answer and sends it again
	if runs.Load() != 1 || again.Code != http.StatusCreated || again.Body.String() != `{"name":"lab"}` || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("sent again: ran %d times, %d %s", runs.Load(), again.Code, again.Body)
	}
	if first.Code != http.StatusCreated {
		t.Errorf("first: %d", first.Code)
	}
	if rec := do("POST", "ayse", "k1"); runs.Load() != 2 || rec.Code != http.StatusConflict {
		t.Errorf("another user's answer was replayed: ran %d, %d", runs.Load(), rec.Code)
	}
	if do("POST", "onur", "k2"); runs.Load() != 3 {
		t.Error("a new key did not run")
	}
	if do("POST", "onur", ""); runs.Load() != 4 {
		t.Error("a write without a key did not run")
	}
}

func TestReplayedWritesAreNotRecordedTwice(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	admin := a.signIn(t, "onur")
	before := len(a.api.activity.List(1000, activity.Filter{}))
	for i := 0; i < 2; i++ {
		if resp, body := admin.req("POST", "/api/v1/networks", `{"name":"lab","mode":"nat","subnet":"10.9.0.0/24","dhcp":true}`, "Idempotency-Key", "same"); resp.StatusCode != http.StatusCreated {
			t.Fatalf("try %d: %d %s", i, resp.StatusCode, body)
		}
	}
	if n := len(a.api.activity.List(1000, activity.Filter{})) - before; n != 1 {
		t.Errorf("one network, %d activity entries", n)
	}
	if n := strings.Count(strings.Join(a.backend.calls, " "), "network lab"); n > 1 {
		t.Errorf("the network was made %d times", n)
	}
}
