package virt

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func xzed(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func TestCompressionOf(t *testing.T) {
	raw := bytes.Repeat([]byte{0}, 64)
	for _, c := range []struct {
		head []byte
		want string
	}{
		{gzipped(t, raw), "gzip"},
		{xzed(t, raw), "xz"},
		{append([]byte("QFI\xfb"), raw...), ""},
		{raw, ""},
		{[]byte{0x1f}, ""},
		{nil, ""},
	} {
		if got := compressionOf(c.head); got != c.want {
			t.Errorf("compressionOf(% x…) = %q, want %q", c.head[:min(len(c.head), 6)], got, c.want)
		}
	}
}

func TestUnpackedName(t *testing.T) {
	for in, want := range map[string]string{
		"Fedora-Cloud-Base.raw.xz": "Fedora-Cloud-Base.raw",
		"disk.qcow2.gz":            "disk.qcow2",
		"disk.img.gzip":            "disk.img",
		"plain.qcow2":              "plain.qcow2",
		".xz":                      ".xz",
	} {
		if got := unpackedName(in); got != want {
			t.Errorf("unpackedName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnpackTo(t *testing.T) {
	img := append([]byte("QFI\xfb"), bytes.Repeat([]byte("disk"), 4096)...)
	for kind, packed := range map[string][]byte{"gzip": gzipped(t, img), "xz": xzed(t, img)} {
		dir := t.TempDir()
		path, n, err := unpackTo(context.Background(), dir, kind, bytes.NewReader(packed), 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		got, _ := os.ReadFile(path)
		if n != int64(len(img)) || !bytes.Equal(got, img) {
			t.Fatalf("%s: unpacked %d bytes, content equal %v", kind, n, bytes.Equal(got, img))
		}
		if f, _ := sniffFormat(path); f != "qcow2" {
			t.Errorf("%s: unpacked image sniffed as %q", kind, f)
		}
	}
}

func TestUnpackToLeavesNothingBehind(t *testing.T) {
	img := bytes.Repeat([]byte("disk"), 4096)
	packed := gzipped(t, img)
	for name, c := range map[string]struct {
		kind  string
		data  []byte
		limit int64
	}{
		"damaged":   {"gzip", append(packed[:len(packed)/2:len(packed)/2], 0xde, 0xad), 1 << 20},
		"truncated": {"xz", xzed(t, img)[:40], 1 << 20},
		"too big":   {"gzip", packed, 100},
		"empty":     {"gzip", gzipped(t, nil), 1 << 20},
	} {
		dir := t.TempDir()
		_, _, err := unpackTo(context.Background(), dir, c.kind, bytes.NewReader(c.data), c.limit)
		var ie *InvalidError
		var oe *OperationError
		if err == nil || !(errors.As(err, &ie) || errors.As(err, &oe)) {
			t.Errorf("%s: err = %v, want a message for the user", name, err)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
			t.Errorf("%s: left %v", name, left)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
			t.Errorf("%s: left %v", name, left)
		}
	}
}

func TestUnpackToStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if _, _, err := unpackTo(ctx, dir, "gzip", bytes.NewReader(gzipped(t, []byte("x"))), 1<<20); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}
