package api

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// plainError turns an error no typed mapping in writeError knows into a status and a
// message a person can act on. The full error is logged by the caller.
//
//   - libvirt cannot be reached (socket gone, connection dropped): 503
//   - the host's disk is full: 507
//   - a file or folder does not exist: 404
//   - VirtoMir may not read or write a file: 403 naming the path
//   - anything else: 500 "Something went wrong: <short message>"
func plainError(err error) (int, string) {
	msg := shortError(err.Error())
	switch {
	case libvirtUnreachable(err):
		return http.StatusServiceUnavailable, "VirtoMir cannot reach libvirt right now (" + msg + "). Is libvirtd (or virtqemud) running? VirtoMir reconnects by itself."
	case errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage, "Not enough space on the host's disk: " + msg
	case errors.Is(err, fs.ErrNotExist):
		if p := pathOf(err); p != "" {
			return http.StatusNotFound, p + " does not exist"
		}
		return http.StatusNotFound, msg
	case errors.Is(err, fs.ErrPermission):
		if p := pathOf(err); p != "" {
			return http.StatusForbidden, "Permission denied on " + p + ": VirtoMir may not do that there."
		}
		return http.StatusForbidden, "Permission denied: " + msg
	}
	return http.StatusInternalServerError, "Something went wrong: " + msg
}

// pathOf returns the file or folder of an *os.PathError (or LinkError) in err's chain.
func pathOf(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Path
	}
	return ""
}

// libvirtUnreachable reports connection-level failures talking to libvirt, as opposed to
// libvirt refusing a request (those are typed errors).
func libvirtUnreachable(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connect to libvirt") || strings.Contains(s, "libvirt accepted the connection") ||
		strings.Contains(s, "use of closed network connection") || strings.Contains(s, "libvirt is not connected")
}

// shortError keeps what matters of a long or multi-line message: for tool output (e.g.
// "qemu-img convert: exit status 1: qemu-img: Could not open '/x': Permission denied")
// the tool's own last complaint, and at most 400 characters.
func shortError(s string) string {
	s = strings.TrimSpace(s)
	for _, tool := range []string{"qemu-img: ", "virt-sparsify: ", "virt-v2v: "} {
		if i := strings.LastIndex(s, tool); i >= 0 {
			s = strings.TrimSpace(s[i+len(tool):])
			break
		}
	}
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) > 1 {
		s = strings.TrimSpace(lines[0])
		for _, l := range lines[1:] { // the first line that says more than "exit status"
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(s, l) && len(s) < 40 {
				s += ": " + l
				break
			}
		}
	}
	if r := []rune(s); len(r) > 400 {
		s = string(r[:400]) + "…"
	}
	return s
}
