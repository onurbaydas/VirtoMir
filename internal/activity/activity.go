// Package activity records who changed what: every changing API call and every sign-in,
// with its result. The newest entries are kept in memory for the UI; all of them are
// appended to a JSON-lines file (rotated at MaxFileSize) for auditing and SIEM import.
package activity

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Token  string    `json:"token,omitempty"` // API token name, if one was used
	Remote string    `json:"remote,omitempty"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// OK reports whether the request succeeded.
func (e Entry) OK() bool { return e.Status > 0 && e.Status < 400 }

const (
	// Keep is how many entries stay in memory.
	Keep = 1000
	// MaxFileSize rotates the log to <file>.1.
	MaxFileSize = 10 << 20
)

type Log struct {
	path string
	mu   sync.Mutex
	ring []Entry // oldest first
}

// Open loads the newest entries of path. A missing file is fine.
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			l.push(e)
		}
	}
	return l, sc.Err()
}

func (l *Log) push(e Entry) {
	if len(l.ring) == Keep {
		l.ring = append(l.ring[:0], l.ring[1:]...)
	}
	l.ring = append(l.ring, e)
}

// Add records an entry (and appends it to the file; write errors are returned but the
// entry is kept in memory either way).
func (l *Log) Add(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.push(e)
	if l.path == "" {
		return nil
	}
	if st, err := os.Stat(l.path); err == nil && st.Size() > MaxFileSize {
		_ = os.Rename(l.path, l.path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Filter narrows List. Zero values match everything.
type Filter struct {
	User   string
	Target string // substring of the path, e.g. a machine UUID
	Failed bool   // only failed requests
}

// List returns up to limit entries, newest first.
func (l *Log) List(limit int, f Filter) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Entry{}
	for i := len(l.ring) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.ring[i]
		if f.User != "" && e.User != f.User {
			continue
		}
		if f.Target != "" && !strings.Contains(e.Path, f.Target) {
			continue
		}
		if f.Failed && e.OK() {
			continue
		}
		out = append(out, e)
	}
	return out
}
