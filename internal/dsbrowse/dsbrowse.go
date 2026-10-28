// Package dsbrowse is the datastore browser: the files and folders of a file-based storage
// pool (a directory, NFS, SMB or carved pool), with downloading, uploading, folders,
// renaming, copying and moving between pools, and deleting. Every operation goes through
// os.Root, so neither ".." nor a symbolic link leads out of the pool's directory, and files a
// machine uses are never renamed, moved or deleted.
package dsbrowse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Errors for the API.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrInUse    = errors.New("in use")
)

// Entry is a file or folder of a pool.
type Entry struct {
	Name      string    `json:"name"`
	Dir       bool      `json:"dir,omitempty"`
	Size      int64     `json:"sizeBytes"`
	Allocated int64     `json:"allocatedBytes"` // on disk: less than Size for a thin image
	Modified  time.Time `json:"modified"`
	Format    string    `json:"format,omitempty"` // qcow2, iso, raw, vmdk, …
	UsedBy    []string  `json:"usedBy,omitempty"` // machines with this disk (or one inside a folder)
}

// Clean checks a path inside a pool ("" is its top) and returns it relative and clean.
func Clean(p string) (string, error) {
	if strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q is not a path inside the pool", ErrInvalid, p)
	}
	c := path.Clean("/" + p)[1:]
	if c == "" {
		c = "."
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %q leaves the pool", ErrInvalid, p)
		}
	}
	return c, nil
}

func open(root string) (*os.Root, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open the pool's directory %s: %w", root, err)
	}
	return r, nil
}

func notFound(err error, rel string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, rel)
	}
	return err
}

// usersOf lists the machines using the file at abs, or anything below it for a folder.
func usersOf(abs string, users map[string][]string) []string {
	var out []string
	for p, us := range users {
		if p == abs || strings.HasPrefix(p, abs+"/") {
			for _, u := range us {
				if !slices.Contains(out, u) {
					out = append(out, u)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// List lists a folder of the pool at root: folders first, then files by name.
func List(root, rel string, users map[string][]string) ([]Entry, error) {
	rel, err := Clean(rel)
	if err != nil {
		return nil, err
	}
	r, err := open(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := r.Open(rel)
	if err != nil {
		return nil, notFound(err, rel)
	}
	defer d.Close()
	des, err := d.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a folder", ErrInvalid, rel)
	}
	out := []Entry{}
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue // gone meanwhile
		}
		name := path.Join(rel, de.Name())
		e := Entry{Name: de.Name(), Dir: de.IsDir(), Modified: info.ModTime().UTC(),
			UsedBy: usersOf(filepath.Join(root, name), users)}
		if !e.Dir {
			e.Size = info.Size()
			e.Allocated = e.Size
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				e.Allocated = st.Blocks * 512
			}
			if info.Mode().IsRegular() {
				e.Format = format(r, name)
			}
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// format names an image by its extension, or a qcow2 one by its magic.
func format(r *os.Root, name string) string {
	switch ext := strings.ToLower(path.Ext(name)); ext {
	case ".qcow2", ".iso", ".vmdk", ".vhdx", ".vhd", ".ova", ".ovf", ".raw", ".img", ".vdi":
		return strings.TrimPrefix(ext, ".")
	}
	f, err := r.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	magic := make([]byte, 4)
	if n, _ := io.ReadFull(f, magic); n == 4 && string(magic) == "QFI\xfb" {
		return "qcow2"
	}
	return ""
}

// Mkdir makes a folder.
func Mkdir(root, rel string) error {
	rel, err := Clean(rel)
	if err != nil || rel == "." {
		return fmt.Errorf("%w: a folder needs a name", ErrInvalid)
	}
	r, err := open(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Mkdir(rel, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s already exists", ErrInvalid, rel)
		}
		return notFound(err, path.Dir(rel))
	}
	return nil
}

func checkFree(root, rel string, users map[string][]string) error {
	if us := usersOf(filepath.Join(root, rel), users); len(us) > 0 {
		return fmt.Errorf("%w: %s is a disk of %s; detach it or delete the machine first", ErrInUse, rel, strings.Join(us, ", "))
	}
	return nil
}

// Rename renames or moves a file or folder inside the pool; it never replaces another one.
func Rename(root, from, to string, users map[string][]string) error {
	from, err := Clean(from)
	if err != nil {
		return err
	}
	if to, err = Clean(to); err != nil {
		return err
	}
	if from == "." || to == "." {
		return fmt.Errorf("%w: the pool itself cannot be renamed here", ErrInvalid)
	}
	if err := checkFree(root, from, users); err != nil {
		return err
	}
	r, err := open(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(to); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrInvalid, to)
	}
	if err := r.Rename(from, to); err != nil {
		return notFound(err, from)
	}
	return nil
}

// Remove deletes a file, or a folder that is empty.
func Remove(root, rel string, users map[string][]string) error {
	rel, err := Clean(rel)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("%w: the pool itself is removed on the storage page", ErrInvalid)
	}
	if err := checkFree(root, rel, users); err != nil {
		return err
	}
	r, err := open(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Remove(rel); err != nil {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("%w: the folder %s is not empty", ErrInvalid, rel)
		}
		return notFound(err, rel)
	}
	return nil
}

// Open opens a file for downloading.
func Open(root, rel string) (*os.File, error) {
	rel, err := Clean(rel)
	if err != nil {
		return nil, err
	}
	r, err := open(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, notFound(err, rel)
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s is not a file", ErrInvalid, rel)
	}
	return f, nil
}

// Upload writes a new file from src: into a hidden part file first, renamed when complete,
// so a broken upload never looks like an image. An existing file is never replaced.
func Upload(root, rel string, src io.Reader) (int64, error) {
	rel, err := Clean(rel)
	if err != nil || rel == "." {
		return 0, fmt.Errorf("%w: the file needs a name", ErrInvalid)
	}
	r, err := open(root)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	if _, err := r.Lstat(rel); err == nil {
		return 0, fmt.Errorf("%w: %s already exists", ErrInvalid, rel)
	}
	part := path.Join(path.Dir(rel), "."+path.Base(rel)+".part")
	f, err := r.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, notFound(err, path.Dir(rel))
	}
	n, err := sparseCopy(context.Background(), f, src, -1, nil)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = r.Rename(part, rel)
	}
	if err != nil {
		_ = r.Remove(part)
		return n, err
	}
	return n, nil
}

// Copy copies (or, with move, moves) a file from one pool to another (or the same), keeping
// a thin image thin. report gets the progress in percent.
func Copy(ctx context.Context, srcRoot, src, dstRoot, dst string, move bool, users map[string][]string, report func(string)) error {
	src, err := Clean(src)
	if err != nil {
		return err
	}
	if dst, err = Clean(dst); err != nil {
		return err
	}
	if err := checkFree(srcRoot, src, users); err != nil {
		return err // a running machine's disk copied while in use would be inconsistent
	}
	if move && srcRoot == dstRoot {
		return Rename(srcRoot, src, dst, users)
	}
	sr, err := open(srcRoot)
	if err != nil {
		return err
	}
	defer sr.Close()
	in, err := sr.Open(src)
	if err != nil {
		return notFound(err, src)
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%w: only files are copied (%s is not one)", ErrInvalid, src)
	}
	dr, err := open(dstRoot)
	if err != nil {
		return err
	}
	defer dr.Close()
	if _, err := dr.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s already exists there", ErrInvalid, dst)
	}
	part := path.Join(path.Dir(dst), "."+path.Base(dst)+".part")
	out, err := dr.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return notFound(err, path.Dir(dst))
	}
	_, err = sparseCopy(ctx, out, in, st.Size(), report)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = dr.Rename(part, dst)
	}
	if err != nil {
		_ = dr.Remove(part)
		return err
	}
	if move {
		return sr.Remove(src)
	}
	return nil
}

const chunk = 1 << 20

// sparseCopy copies src to dst, seeking over all-zero chunks so they stay holes. size (-1:
// unknown) gives report a percentage.
func sparseCopy(ctx context.Context, dst *os.File, src io.Reader, size int64, report func(string)) (int64, error) {
	buf := make([]byte, chunk)
	var n int64
	last := -1
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		m, rerr := io.ReadFull(src, buf)
		if m > 0 {
			if zero(buf[:m]) {
				if _, err := dst.Seek(int64(m), io.SeekCurrent); err != nil {
					return n, err
				}
			} else if _, err := dst.Write(buf[:m]); err != nil {
				return n, err
			}
			n += int64(m)
			if report != nil && size > 0 {
				if pct := int(n * 100 / size); pct != last {
					last = pct
					report(fmt.Sprintf("%d %%", pct))
				}
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return n, rerr
		}
	}
	return n, dst.Truncate(n) // a hole at the end still counts in the size
}

func zero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
