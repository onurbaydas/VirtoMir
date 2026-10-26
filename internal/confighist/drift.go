package confighist

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"libvirt.org/go/libvirtxml"
)

// DefaultXMLDir is where libvirt's QEMU driver keeps the definitions of persistent machines.
const DefaultXMLDir = "/etc/libvirt/qemu"

// Drift is a machine whose definition file was edited directly: libvirt keeps using what it
// loaded and only reads the file again when it restarts (or the file is defined again).
type Drift struct {
	FileChanged bool      `json:"fileChanged"` // always true; present only while a file is ahead of libvirt
	File        string    `json:"file"`
	Modified    time.Time `json:"modified"`
}

// DriftWatcher compares libvirt's definition files with what libvirt has loaded. It looks at
// the files' modification times every interval and compares only files that changed (and
// every file now and then), so it costs next to nothing on a quiet host.
type DriftWatcher struct {
	dir      string
	src      Source
	log      *slog.Logger
	interval time.Duration

	mu     sync.Mutex
	seen   map[string]time.Time // file → modification time when last compared
	drift  map[string]Drift     // machine UUID → edited file
	fileOf map[string]string    // machine UUID → its definition file
}

// NewDriftWatcher watches dir (DefaultXMLDir when empty).
func NewDriftWatcher(dir string, src Source, log *slog.Logger) *DriftWatcher {
	if dir == "" {
		dir = DefaultXMLDir
	}
	return &DriftWatcher{dir: dir, src: src, log: log, interval: 30 * time.Second, seen: map[string]time.Time{}, drift: map[string]Drift{}, fileOf: map[string]string{}}
}

// Run checks until ctx ends. It does nothing off Linux or when the folder cannot be read
// (VirtoMir not running as root, a session libvirt, another driver).
func (w *DriftWatcher) Run(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	if _, err := os.ReadDir(w.dir); err != nil {
		w.log.Info("definition files are not watched", "dir", w.dir, "err", err)
		return
	}
	full := time.Now()
	for {
		if time.Since(full) > 10*time.Minute {
			w.mu.Lock()
			clear(w.seen) // compare everything again now and then
			w.mu.Unlock()
			full = time.Now()
		}
		w.Check()
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.interval):
		}
	}
}

// Check compares every definition file that changed since it was last compared.
func (w *DriftWatcher) Check() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		path := filepath.Join(w.dir, e.Name())
		present[path] = true
		info, err := e.Info()
		if err != nil {
			continue
		}
		w.mu.Lock()
		same := w.seen[path].Equal(info.ModTime())
		w.mu.Unlock()
		if !same {
			w.compare(path, info.ModTime())
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for path := range w.seen {
		if !present[path] {
			delete(w.seen, path)
		}
	}
	for uuid, f := range w.fileOf {
		if !present[f] {
			delete(w.fileOf, uuid)
			delete(w.drift, uuid)
		}
	}
}

func (w *DriftWatcher) compare(path string, mod time.Time) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	uuid := fileUUID(string(b))
	if uuid == "" {
		return
	}
	loaded, err := w.src.DomainXML(uuid, true)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil { // a file libvirt has not loaded at all, or libvirt is away: nothing to say yet
		return
	}
	w.seen[path] = mod
	w.fileOf[uuid] = path
	same, err := SameDefinition(string(b), loaded)
	switch {
	case err != nil:
		w.log.Debug("compare definition file", "file", path, "err", err)
	case same:
		delete(w.drift, uuid)
	default:
		w.drift[uuid] = Drift{FileChanged: true, File: path, Modified: mod.UTC()}
	}
}

// Recheck compares a machine's file again on the next Check (e.g. right after it was loaded).
func (w *DriftWatcher) Recheck(uuid string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.fileOf[strings.ToLower(uuid)]; ok {
		delete(w.seen, f)
	}
}

// Drift reports whether a machine's definition file was edited but not loaded.
func (w *DriftWatcher) Drift(uuid string) (Drift, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.drift[strings.ToLower(uuid)]
	return d, ok
}

// ErrNoDrift is returned by EditedFile when the machine's file matches what libvirt loaded.
var ErrNoDrift = errors.New("the definition file matches what libvirt has loaded")

// EditedFile returns the content of a machine's edited definition file, to load it.
func (w *DriftWatcher) EditedFile(uuid string) (path, doc string, err error) {
	d, ok := w.Drift(uuid)
	if !ok {
		return "", "", ErrNoDrift
	}
	b, err := os.ReadFile(d.File)
	if err != nil {
		return "", "", err
	}
	if !strings.EqualFold(fileUUID(string(b)), uuid) {
		return "", "", errors.New("the file now belongs to another machine")
	}
	return d.File, string(b), nil
}

func fileUUID(doc string) string {
	var d libvirtxml.Domain
	if d.Unmarshal(doc) != nil {
		return ""
	}
	return strings.ToLower(d.UUID)
}

// The file keeps secrets (VNC/SPICE passwords) that libvirt never shows over its API.
var secretAttr = regexp.MustCompile(`\s+passwd='[^']*'`)

// SameDefinition reports whether a definition file and libvirt's loaded definition describe
// the same machine (formatting, comments and passwords aside).
func SameDefinition(file, loaded string) (bool, error) {
	norm := func(doc string) (string, error) {
		var d libvirtxml.Domain
		if err := d.Unmarshal(secretAttr.ReplaceAllString(doc, "")); err != nil {
			return "", err
		}
		return d.Marshal()
	}
	a, err := norm(file)
	if err != nil {
		return false, err
	}
	b, err := norm(loaded)
	if err != nil {
		return false, err
	}
	return a == b, nil
}
