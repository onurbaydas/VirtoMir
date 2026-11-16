package api

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// Writes that may be sent twice. A browser drops a request that is under way when the host's
// network changes (Chrome: ERR_NETWORK_CHANGED), and creating or stopping a libvirt network,
// starting a machine or making a bridge changes it: the work is done, the answer is lost.
// The UI sends every write with an Idempotency-Key and sends it again with the same key; the
// first answer is replayed instead of doing the work twice ("network already exists").

// What is remembered has an end: a client decides how many keys it sends, and each one
// holds an answer for ten minutes. Past these bounds a write simply runs as if it had no key.
const (
	replayFor        = 10 * time.Minute
	replayMaxBody    = 1 << 20
	replayMaxEntries = 4096
	replayMaxBytes   = 64 << 20 // all remembered answers together
	replaySweepEvery = 30 * time.Second
)

type replayEntry struct {
	done   chan struct{}
	at     time.Time
	status int
	ctype  string
	body   []byte
	stored bool // false: too large to keep, the request runs again
}

type replayCache struct {
	mu    sync.Mutex
	m     map[string]*replayEntry
	bytes int       // of the answers kept in m
	swept time.Time // when the entries past replayFor were last dropped
}

// sweep drops the entries past replayFor (caller holds c.mu). Not on every request: looking
// at every entry under the lock for each write would make many keys slow down all writes.
func (c *replayCache) sweep(now time.Time) {
	if now.Sub(c.swept) < replaySweepEvery && len(c.m) < replayMaxEntries {
		return
	}
	c.swept = now
	for k, e := range c.m {
		if now.Sub(e.at) > replayFor {
			c.bytes -= len(e.body)
			delete(c.m, k)
		}
	}
}

// replay answers a write sent again with the same Idempotency-Key (by the same user) with
// the answer of the first one, waiting for it when it still runs.
func (s *Server) replay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 || r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		// Only for someone who is signed in: a sign-in attempt, or anything else sent
		// without a user, is not worth remembering, and anyone can send those.
		user := identity(r).Name
		if user == "" {
			next.ServeHTTP(w, r)
			return
		}
		id := user + "\x00" + r.Method + " " + r.URL.RequestURI() + "\x00" + key
		c := &s.replays
		now := time.Now()
		c.mu.Lock()
		if c.m == nil {
			c.m = map[string]*replayEntry{}
		}
		c.sweep(now)
		if e, ok := c.m[id]; ok && now.Sub(e.at) <= replayFor {
			c.mu.Unlock()
			select {
			case <-e.done:
			case <-r.Context().Done():
				return
			}
			if e.stored {
				if e.ctype != "" {
					w.Header().Set("Content-Type", e.ctype)
				}
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(e.status)
				_, _ = w.Write(e.body)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if old, ok := c.m[id]; ok { // past replayFor and not swept yet
			c.bytes -= len(old.body)
			delete(c.m, id)
		}
		if len(c.m) >= replayMaxEntries {
			c.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}
		e := &replayEntry{done: make(chan struct{}), at: now}
		c.m[id] = e
		c.mu.Unlock()

		rec := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			body, stored := rec.buf.Bytes(), !rec.tooBig
			c.mu.Lock()
			if stored && c.bytes+len(body) > replayMaxBytes {
				body, stored = nil, false // no room: sent again, this write runs again
			}
			if c.m[id] == e {
				c.bytes += len(body)
			}
			c.mu.Unlock()
			e.status, e.ctype, e.body, e.stored = rec.status, rec.Header().Get("Content-Type"), body, stored
			close(e.done)
		}()
		next.ServeHTTP(rec, r)
	})
}

// captureWriter passes the answer on and keeps a copy of it.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	tooBig bool
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.tooBig {
		if c.buf.Len()+len(b) > replayMaxBody {
			c.tooBig = true
			c.buf.Reset()
		} else {
			c.buf.Write(b)
		}
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection (flushes, deadlines).
func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
