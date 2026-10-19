package sdn

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// FakeRunner records commands and answers them from a table: the longest key that the
// command line starts with wins; a missing command answers exec.ErrNotFound.
type FakeRunner struct {
	Calls   []string
	Answers map[string]string
	Errors  map[string]error
	Missing map[string]bool
}

func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.Calls = append(f.Calls, line)
	if f.Missing[name] {
		return nil, exec.ErrNotFound
	}
	best, out, err := "", "", error(nil)
	for k, v := range f.Answers {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best, out = k, v
		}
	}
	for k, e := range f.Errors {
		if strings.HasPrefix(line, k) && len(k) >= len(best) {
			best, err = k, e
		}
	}
	return []byte(out), err
}

// called reports whether a command starting with prefix ran.
func (f *FakeRunner) called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

var testTime = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
