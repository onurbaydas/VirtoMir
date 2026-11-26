package virt

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"github.com/ulikunitz/xz"
)

// Compressed disk images (Fedora's .raw.xz, .qcow2.gz, .img.gz) are unpacked on upload.
// Stored as they come they would be a raw volume full of xz or gzip bytes that no machine
// can boot from.

var (
	gzipMagic = []byte{0x1f, 0x8b}
	xzMagic   = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
)

// compressionOf names the compression of a stream from its first bytes ("" for none).
func compressionOf(head []byte) string {
	switch {
	case bytes.HasPrefix(head, xzMagic):
		return "xz"
	case bytes.HasPrefix(head, gzipMagic):
		return "gzip"
	}
	return ""
}

// unpackedName drops a compression suffix: fedora.raw.xz → fedora.raw, disk.img.gz → disk.img.
func unpackedName(name string) string {
	for _, ext := range []string{".xz", ".gz", ".gzip"} {
		if base, ok := strings.CutSuffix(name, ext); ok && base != "" {
			return base
		}
	}
	return name
}

// decompressor wraps r in the reader for kind.
func decompressor(kind string, r io.Reader) (io.Reader, error) {
	switch kind {
	case "gzip":
		return gzip.NewReader(r)
	case "xz":
		return xz.NewReader(r)
	}
	return nil, fmt.Errorf("unknown compression %q", kind)
}

// errNoRoom stops unpacking before the pool fills up.
var errNoRoom = errors.New("no room")

// roomWriter refuses to write past limit bytes.
type roomWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (rw *roomWriter) Write(p []byte) (int, error) {
	if rw.n+int64(len(p)) > rw.limit {
		return 0, errNoRoom
	}
	n, err := rw.w.Write(p)
	rw.n += int64(n)
	return n, err
}

// unpackTo decompresses r (kind) into a new file in dir, writing at most limit bytes, and
// returns its path. On any failure nothing is left behind.
func unpackTo(ctx context.Context, dir, kind string, r io.Reader, limit int64) (string, int64, error) {
	dr, err := decompressor(kind, &ctxReader{ctx: ctx, r: r})
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, invalidf("The upload looks like a %s archive but cannot be read: %v", kind, err)
	}
	f, err := os.CreateTemp(dir, ".virtomir-unpack-*")
	if err != nil {
		return "", 0, err
	}
	tmp := f.Name()
	rw := &roomWriter{w: f, limit: limit}
	_, err = io.Copy(rw, dr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && rw.n == 0 {
		err = invalidf("The %s archive is empty.", kind)
	}
	if err != nil {
		_ = os.Remove(tmp)
		switch {
		case ctx.Err() != nil:
			return "", 0, ctx.Err()
		case errors.Is(err, errNoRoom):
			return "", 0, refusedf("The unpacked image does not fit: the pool has %s free.", humanBytes(uint64(limit)))
		case errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission):
			return "", 0, err
		}
		var ie *InvalidError
		if errors.As(err, &ie) {
			return "", 0, err
		}
		return "", 0, invalidf("The %s archive is damaged and could not be unpacked: %v", kind, err)
	}
	return tmp, rw.n, nil
}

// uploadCompressed unpacks a gzip or xz upload into a file pool, checks it like any other
// image, and makes it the volume name (without the compression suffix).
func (c *Client) uploadCompressed(ctx context.Context, l *libvirt.Libvirt, pool, name, kind string, r io.Reader) (Volume, error) {
	dir, err := c.filePoolPath(l, pool)
	if err != nil {
		return Volume{}, invalidf("%s is %s-compressed. Upload it to a directory, file system or NFS pool, where VirtoMir unpacks it, or unpack it first (pool %s keeps no plain files).", name, kind, pool)
	}
	name = unpackedName(name)
	if err := checkName("Volume", name); err != nil {
		return Volume{}, err
	}
	p, err := l.StoragePoolLookupByName(pool)
	if err != nil {
		return Volume{}, classify(err)
	}
	if _, err := l.StorageVolLookupByName(p, name); err == nil {
		return Volume{}, refusedf("Pool %s already has a volume named %s.", pool, name)
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		return Volume{}, refusedf("Pool %s already has a file named %s.", pool, name)
	}
	_ = l.StoragePoolRefresh(p, 0)
	_, _, _, avail, err := l.StoragePoolGetInfo(p)
	if err != nil {
		return Volume{}, classify(err)
	}
	// Leave a little room so the pool is never filled to the last byte.
	limit := int64(avail) - int64(avail)/50
	if limit <= 0 {
		return Volume{}, refusedf("Pool %s has no free space.", pool)
	}
	defer inFlight.add(dst)()
	tmp, n, err := unpackTo(ctx, dir, kind, r, limit)
	if err != nil {
		return Volume{}, err
	}
	// Once linked to its name the image stays; the temporary name always goes.
	defer os.Remove(tmp)
	format, err := sniffFormat(tmp)
	if err != nil {
		return Volume{}, err
	}
	if format != "qcow2" && format != "raw" {
		return Volume{}, invalidf("The unpacked image is %s; upload qcow2 or raw images, or import it through New machine → Import.", format)
	}
	if format == "qcow2" {
		if _, err := checkedImage(ctx, tmp, format, false); err != nil {
			return Volume{}, err
		}
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return Volume{}, err
	}
	// Link, not rename: link fails if a file of that name appeared in the meantime.
	if err := os.Link(tmp, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Volume{}, refusedf("Pool %s already has a file named %s.", pool, name)
		}
		return Volume{}, err
	}
	_ = l.StoragePoolRefresh(p, 0)
	vol, err := l.StorageVolLookupByName(p, name)
	if err != nil {
		_ = os.Remove(dst)
		return Volume{}, classify(err)
	}
	c.log.Info("volume uploaded and unpacked", "pool", pool, "volume", name, "compression", kind, "bytes", n, "format", format)
	return volumeInfo(l, vol)
}

// peekCompression reports the compression of br's next bytes without consuming them.
func peekCompression(br *bufio.Reader) string {
	head, _ := br.Peek(len(xzMagic))
	return compressionOf(head)
}
