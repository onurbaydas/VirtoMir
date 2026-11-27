// Package content manages the cluster-wide content library for VirtoMir (ADR-0063).
// This file implements resumable, checksum-verified file transfers between cluster peers.
package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
)

// ErrChecksumMismatch indicates that a downloaded item version does not match its expected SHA-256 digest.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// DownloadPath returns the canonical API endpoint path for downloading a content version.
func DownloadPath(itemID, versionID string) string {
	return fmt.Sprintf("/api/v1/content/items/%s/versions/%s/download", url.PathEscape(itemID), url.PathEscape(versionID))
}

// ServeContent streams a file to the response writer, honoring HTTP Range requests for resumable downloads.
func ServeContent(w http.ResponseWriter, r *http.Request, filePath string, totalSize int64) {
	rangeHdr := strings.TrimSpace(r.Header.Get("Range"))
	if rangeHdr == "" {
		f, err := os.Open(filePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
		return
	}

	if !strings.HasPrefix(strings.ToLower(rangeHdr), "bytes=") {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "invalid range header", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	spec := strings.TrimSpace(rangeHdr[len("bytes="):])
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "invalid range format", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	if startStr == "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "invalid range start", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "invalid range start offset", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	var end int64
	if endStr == "" {
		end = totalSize - 1
	} else {
		end, err = strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
			http.Error(w, "invalid range end offset", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}

	if start < 0 || end < start || totalSize <= 0 || end >= totalSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.CopyN(w, f, length)
}

// progressReader counts what was read so a long copy can report progress.
type progressReader struct {
	r     io.Reader
	n     atomic.Int64
	total int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

func (p *progressReader) percent() int64 {
	if p.total <= 0 {
		return 100
	}
	pct := p.n.Load() * 100 / p.total
	if pct > 100 {
		return 100
	}
	return pct
}

func validatePathComponent(name, val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return fmt.Errorf("%s cannot be empty", name)
	}
	if strings.ContainsAny(val, "/\\") || strings.Contains(val, "..") {
		return fmt.Errorf("invalid %s %q: path traversal not allowed", name, val)
	}
	return nil
}

func validateContentRange(crHeader string, expectedStart, totalSize int64) error {
	cr := strings.TrimSpace(crHeader)
	if cr == "" {
		return errors.New("missing Content-Range header in 206 response")
	}
	if !strings.HasPrefix(strings.ToLower(cr), "bytes ") {
		return fmt.Errorf("invalid Content-Range unit: %q", cr)
	}
	spec := strings.TrimSpace(cr[len("bytes "):])
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid Content-Range format: %q", cr)
	}

	rangeParts := strings.SplitN(strings.TrimSpace(parts[0]), "-", 2)
	if len(rangeParts) != 2 {
		return fmt.Errorf("invalid Content-Range byte range: %q", cr)
	}

	start, err := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Range start offset: %q", cr)
	}
	end, err := strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Range end offset: %q", cr)
	}

	if start != expectedStart {
		return fmt.Errorf("Content-Range start offset mismatch: got %d, want %d", start, expectedStart)
	}
	if end < start {
		return fmt.Errorf("Content-Range end before start: %d < %d", end, start)
	}
	if totalSize > 0 && end >= totalSize {
		return fmt.Errorf("Content-Range end exceeds total size: %d >= %d", end, totalSize)
	}

	totStr := strings.TrimSpace(parts[1])
	if totStr != "*" {
		tot, err := strconv.ParseInt(totStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid Content-Range total size: %q", cr)
		}
		if totalSize > 0 && tot != totalSize {
			return fmt.Errorf("Content-Range total size mismatch: got %d, want %d", tot, totalSize)
		}
	}
	return nil
}

// Pull downloads a content item version from a cluster peer into targetPoolPath with resume support and SHA-256 verification.
func Pull(ctx context.Context, peer *cluster.Peer, item Item, version ItemVersion, targetPoolPath string, report func(string)) (string, error) {
	if peer == nil {
		return "", errors.New("peer is required")
	}

	if err := validatePathComponent("item ID", item.ID); err != nil {
		return "", err
	}
	if err := validatePathComponent("version ID", version.ID); err != nil {
		return "", err
	}
	if err := validatePathComponent("item name", item.Name); err != nil {
		return "", err
	}
	if err := validatePathComponent("version", version.Version); err != nil {
		return "", err
	}
	if err := validatePathComponent("format", version.Format); err != nil {
		return "", err
	}
	if strings.TrimSpace(version.SHA256) == "" {
		return "", errors.New("expected SHA-256 checksum cannot be empty")
	}

	tmpFile, err := poolFile(fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))
	if err != nil {
		return "", err
	}
	finalFile, err := FileName(item.Name, version.Version, version.Format)
	if err != nil {
		return "", err
	}
	tmpPath := filepath.Join(targetPoolPath, tmpFile)
	finalPath := filepath.Join(targetPoolPath, finalFile)

	targetClean := filepath.Clean(targetPoolPath)
	if filepath.Dir(tmpPath) != targetClean || filepath.Dir(finalPath) != targetClean {
		return "", errors.New("path traversal detected: target path escapes directory")
	}

	if targetPoolPath != "" {
		if err := os.MkdirAll(targetPoolPath, 0755); err != nil {
			return "", fmt.Errorf("create target directory: %w", err)
		}
	}

	// Check if temporary file already exists on disk
	var offset int64
	hasPartial := false
	if st, err := os.Stat(tmpPath); err == nil {
		switch {
		case st.Size() == version.Size:
			// Previous transfer reached exact size, verify it directly
			if hash, err := fileSHA256(tmpPath); err == nil && strings.EqualFold(hash, version.SHA256) {
				_ = os.Remove(finalPath)
				if err := os.Rename(tmpPath, finalPath); err != nil {
					return "", err
				}
				_ = os.Chmod(finalPath, 0644)
				if report != nil {
					report(fmt.Sprintf("Downloaded %s (version %s)", item.Name, version.Version))
				}
				return finalPath, nil
			}
			// Mismatched size/hash, remove corrupted temp file
			_ = os.Remove(tmpPath)
		case st.Size() < version.Size && st.Size() > 0:
			offset = st.Size()
			hasPartial = true
		default:
			// Empty or oversized file
			_ = os.Remove(tmpPath)
		}
	}

	headers := make(map[string]string)
	if hasPartial {
		headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
	}

	path := DownloadPath(item.ID, version.ID)
	resp, err := peer.Download(ctx, path, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var f *os.File
	var initialOffset int64

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if !hasPartial {
			return "", errors.New("peer returned 206 Partial Content when full download was requested")
		}
		if err := validateContentRange(resp.Header.Get("Content-Range"), offset, version.Size); err != nil {
			return "", err
		}
		f, err = os.OpenFile(tmpPath, os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return "", fmt.Errorf("open temp file for resume: %w", err)
		}
		initialOffset = offset
	case http.StatusOK:
		f, err = os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return "", fmt.Errorf("open temp file for writing: %w", err)
		}
		initialOffset = 0
	default:
		return "", fmt.Errorf("peer returned HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	remaining := version.Size - initialOffset
	limitR := io.LimitReader(resp.Body, remaining+1)
	pr := &progressReader{r: limitR, total: version.Size}
	pr.n.Store(initialOffset)

	stop := make(chan struct{})
	if report != nil {
		report(fmt.Sprintf("Downloading %s: %d of %d bytes (%d%%)", item.Name, pr.n.Load(), version.Size, pr.percent()))
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					report(fmt.Sprintf("Downloading %s: %d of %d bytes (%d%%)", item.Name, pr.n.Load(), version.Size, pr.percent()))
				}
			}
		}()
	}

	_, copyErr := io.Copy(f, pr)
	close(stop)
	_ = f.Sync()
	closeErr := f.Close()

	if copyErr != nil {
		// Partial file remains intact on disk for resume
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}

	st, err := os.Stat(tmpPath)
	if err != nil {
		return "", err
	}
	if st.Size() < version.Size {
		return "", fmt.Errorf("download incomplete: %d of %d bytes", st.Size(), version.Size)
	}
	if st.Size() > version.Size {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("download exceeded expected size: %d > %d", st.Size(), version.Size)
	}

	hash, err := fileSHA256(tmpPath)
	if err != nil {
		return "", fmt.Errorf("compute checksum: %w", err)
	}

	if !strings.EqualFold(hash, version.SHA256) {
		// Only the partial download is rubbish; an existing final file (already checksum-verified
		// when it was written) is the last good local copy and must not be destroyed here.
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("%w: expected %s, got %s; discarded download", ErrChecksumMismatch, version.SHA256, hash)
	}

	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("rename to final volume: %w", err)
	}
	_ = os.Chmod(finalPath, 0644)

	if report != nil {
		report(fmt.Sprintf("Downloaded %s (version %s)", item.Name, version.Version))
	}
	return finalPath, nil
}

// PruneLocalFile deletes the local volume file when an older version is pruned.
func PruneLocalFile(targetPoolPath, itemName, versionStr, format string) error {
	if err := validatePathComponent("item name", itemName); err != nil {
		return err
	}
	if err := validatePathComponent("version", versionStr); err != nil {
		return err
	}
	if err := validatePathComponent("format", format); err != nil {
		return err
	}
	file, err := FileName(itemName, versionStr, format)
	if err != nil {
		return err
	}
	path := filepath.Join(targetPoolPath, file)
	if filepath.Dir(path) != filepath.Clean(targetPoolPath) {
		return errors.New("path traversal detected: target path escapes directory")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
