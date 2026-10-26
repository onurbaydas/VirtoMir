// Package atomicfile writes state files so a crash or power loss leaves either the old or
// the new content, never a torn one, and moves unreadable state files aside instead of
// letting the next save overwrite them.
package atomicfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Write replaces path with data atomically: a temporary file in the same directory is
// written, fsynced and renamed over path, then the directory is fsynced so the rename
// itself survives a crash. The directory must exist.
func Write(path string, data []byte, perm fs.FileMode) error {
	return WriteFunc(path, perm, func(w io.Writer) error {
		_, err := io.Copy(w, bytes.NewReader(data))
		return err
	})
}

// WriteFunc is Write for content produced by a writer (an encoder, for example).
func WriteFunc(path string, perm fs.FileMode, fill func(io.Writer) error) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err = fill(f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir fsyncs a directory so a rename in it is durable. Windows cannot open directories
// for syncing and some filesystems refuse it; both are best-effort.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// CorruptError reports a state file that could not be parsed and was moved aside.
type CorruptError struct {
	Path   string // the state file
	Backup string // where its content is kept now ("" if it could not be moved)
	Err    error  // why it could not be read (or moved)
}

func (e *CorruptError) Error() string {
	if e.Backup == "" {
		return fmt.Sprintf("%s is unreadable and could not be moved aside: %v", e.Path, e.Err)
	}
	return fmt.Sprintf("%s is unreadable (%v); kept it as %s and started empty", e.Path, e.Err, e.Backup)
}

func (e *CorruptError) Unwrap() error { return e.Err }

// Quarantine renames the unreadable file at path to <path>.corrupt-<unix seconds> (with a
// numeric suffix if that exists already) so nothing overwrites it, and returns a
// *CorruptError naming both. cause is the parse error.
func Quarantine(path string, cause error) error {
	return quarantineAt(path, cause, time.Now())
}

func quarantineAt(path string, cause error, now time.Time) error {
	base := path + ".corrupt-" + strconv.FormatInt(now.Unix(), 10)
	backup := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(backup); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if i > 1000 {
			return &CorruptError{Path: path, Err: fmt.Errorf("%w (no free backup name)", cause)}
		}
		backup = base + "-" + strconv.Itoa(i)
	}
	if err := os.Rename(path, backup); err != nil {
		return &CorruptError{Path: path, Err: fmt.Errorf("%w; moving it aside: %v", cause, err)}
	}
	return &CorruptError{Path: path, Backup: backup, Err: cause}
}
