package api

import (
	"context"
	"fmt"
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

// What is remembered for replays has an end, whoever sends how many keys.
func TestReplayCacheIsBounded(t *testing.T) {
	var runs atomic.Int32
	big := strings.Repeat("x", replayMaxBody-10)
	s := &Server{}
	h := s.replay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		if r.URL.Query().Get("big") != "" {
			w.Write([]byte(big))
			return
		}
		w.Write([]byte("ok"))
	}))
	send := func(h http.Handler, user, target, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", target, nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey, auth.Identity{Name: user}))
		r.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	do := func(user, target, key string) *httptest.ResponseRecorder { return send(h, user, target, key) }

	// Nobody signed in (a sign-in attempt): nothing is remembered, every request runs.
	do("", "/api/v1/auth/login", "k")
	do("", "/api/v1/auth/login", "k")
	if runs.Load() != 2 || len(s.replays.m) != 0 {
		t.Fatalf("without a user: ran %d times, %d remembered", runs.Load(), len(s.replays.m))
	}

	// Large answers: remembered while they fit together, then the writes just run.
	runs.Store(0)
	n := replayMaxBytes/replayMaxBody + 8
	for i := 0; i < n; i++ {
		do("onur", "/api/v1/x?big=1", "big-"+strings.Repeat("i", i+1))
	}
	if s.replays.bytes > replayMaxBytes {
		t.Errorf("%d bytes of answers remembered, more than %d", s.replays.bytes, replayMaxBytes)
	}
	first := do("onur", "/api/v1/x?big=1", "big-i")
	last := do("onur", "/api/v1/x?big=1", "big-"+strings.Repeat("i", n))
	if first.Header().Get("Idempotent-Replayed") != "true" || last.Header().Get("Idempotent-Replayed") == "true" {
		t.Errorf("first replayed=%q, the one past the limit replayed=%q", first.Header().Get("Idempotent-Replayed"), last.Header().Get("Idempotent-Replayed"))
	}

	// Many keys: no more than replayMaxEntries are kept, and the rest still run.
	s2 := &Server{}
	h2 := s2.replay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { runs.Add(1); w.Write([]byte("ok")) }))
	runs.Store(0)
	for i := 0; i < replayMaxEntries+50; i++ {
		send(h2, "onur", "/api/v1/y", fmt.Sprintf("k%d", i))
	}
	if len(s2.replays.m) != replayMaxEntries || int(runs.Load()) != replayMaxEntries+50 {
		t.Errorf("%d remembered, %d ran", len(s2.replays.m), runs.Load())
	}

	// Entries past their time go, and their bytes with them.
	s.replays.mu.Lock()
	for _, e := range s.replays.m {
		e.at = e.at.Add(-2 * replayFor)
	}
	s.replays.swept = s.replays.swept.Add(-2 * replaySweepEvery)
	s.replays.mu.Unlock()
	do("onur", "/api/v1/x", "fresh")
	if len(s.replays.m) != 1 || s.replays.bytes != 2 {
		t.Errorf("after the sweep: %d entries, %d bytes", len(s.replays.m), s.replays.bytes)
	}
}
