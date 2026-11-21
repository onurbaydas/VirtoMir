package virt

import "testing"

// qemu-img's JSON describes files a user uploaded: reading it never panics.
func FuzzParseImageInfo(f *testing.F) {
	f.Add([]byte(`{"filename":"a.qcow2","format":"qcow2","virtual-size":1073741824,"actual-size":200704}`))
	f.Add([]byte(`[{"filename":"b.qcow2","format":"qcow2","backing-filename":"a.qcow2"},{"filename":"a.qcow2","format":"raw"}]`))
	f.Add([]byte(`{"format-specific":{"type":"vmdk","data":{"extents":[{"filename":"x-flat.vmdk"}]}}}`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseImageInfo(b)
	})
}
