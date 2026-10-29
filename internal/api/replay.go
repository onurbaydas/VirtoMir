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

const (
	replayFor     = 10 * time.Minute
	replayMaxBody = 1 << 20
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
	mu sync.Mutex
	m  map[string]*replayEntry
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
		id := identity(r).Name + "\x00" + r.Method + " " + r.URL.RequestURI() + "\x00" + key
		c := &s.replays
		c.mu.Lock()
		if c.m == nil {
			c.m = map[string]*replayEntry{}
		}
		for k, e := range c.m {
			if time.Since(e.at) > replayFor {
				delete(c.m, k)
			}
		}
		if e, ok := c.m[id]; ok {
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
		e := &replayEntry{done: make(chan struct{}), at: time.Now()}
		c.m[id] = e
		c.mu.Unlock()

		rec := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			e.status, e.ctype, e.body, e.stored = rec.status, rec.Header().Get("Content-Type"), rec.buf.Bytes(), !rec.tooBig
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
