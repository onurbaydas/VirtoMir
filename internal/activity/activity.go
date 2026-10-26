// Package activity records who changed what: every changing API call and every sign-in,
// with its result. The newest entries are kept in memory for the UI; all of them are
// appended to a JSON-lines file (rotated at MaxFileSize) for auditing and SIEM import.
// Entries are cryptographically chained with SHA-256 hashes to guarantee tamper evidence.
package activity

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GenesisHash is the SHA-256 prevHash anchor for the first entry in an audit chain.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

type Entry struct {
	Seq      int64     `json:"seq,omitempty"`
	PrevHash string    `json:"prevHash,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	Token    string    `json:"token,omitempty"` // API token name, if one was used
	Remote   string    `json:"remote,omitempty"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Error    string    `json:"error,omitempty"`
}

// OK reports whether the request succeeded.
func (e Entry) OK() bool { return e.Status > 0 && e.Status < 400 }

// ComputeHash computes the canonical SHA-256 hash for an activity entry.
func ComputeHash(e Entry) string {
	canonical := fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s|%s|%d|%s",
		e.PrevHash, e.Seq, e.Time.UTC().Format(time.RFC3339Nano),
		e.User, e.Token, e.Remote, e.Method, e.Path, e.Status, e.Error)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

const (
	// Keep is how many entries stay in memory.
	Keep = 1000
	// MaxFileSize rotates the log to <file>.1 (older ones shift up, see OldFiles).
	MaxFileSize = 10 << 20
)

type Log struct {
	path     string
	mu       sync.Mutex
	ring     []Entry // oldest first
	siem     *Forwarder
	lastHash string
	lastSeq  int64
}

// Open loads the newest entries of path. A missing file is fine.
// It scans the existing file (and rotated path+".1" if present) to discover the
// last valid Hash and Seq on disk, resuming the chain seamlessly across restarts.
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	if path == "" {
		return l, nil
	}

	files := logFiles(path)
	if len(files) == 0 {
		return l, nil
	}

	var (
		totalEntries int64
		lastEntry    Entry
		hasAnyEntry  bool
	)

	for _, fp := range files {
		f, err := os.Open(fp)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return l, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.push(e)
				lastEntry = e
				hasAnyEntry = true
				totalEntries++
			}
		}
		_ = f.Close()
		if err := sc.Err(); err != nil {
			return l, err
		}
	}

	if hasAnyEntry {
		if lastEntry.Hash != "" {
			l.lastHash = lastEntry.Hash
			l.lastSeq = lastEntry.Seq
		} else {
			// Legacy unhashed entries
			l.lastHash = GenesisHash
			l.lastSeq = totalEntries
		}
	}

	return l, nil
}

func (l *Log) push(e Entry) {
	if len(l.ring) == Keep {
		l.ring = append(l.ring[:0], l.ring[1:]...)
	}
	l.ring = append(l.ring, e)
}

// Add records an entry (and appends it to the file; write errors are returned but the
// entry is kept in memory either way). Each entry receives a strictly increasing Seq,
// PrevHash pointing to the previous entry (or GenesisHash), and a cryptographic Hash.
func (l *Log) Add(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	} else {
		e.Time = e.Time.UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lastSeq++
	e.Seq = l.lastSeq
	if l.lastHash == "" {
		e.PrevHash = GenesisHash
	} else {
		e.PrevHash = l.lastHash
	}
	e.Hash = ComputeHash(e)
	l.lastHash = e.Hash

	l.push(e)
	if l.siem != nil {
		l.siem.Send(e)
	}
	if l.path == "" {
		return nil
	}

	// A failed rotation must not lose the entry: it is still appended, and the error is
	// returned so the caller logs it (the file just keeps growing until rotation works).
	var rotErr error
	if st, err := os.Stat(l.path); err == nil && st.Size() > MaxFileSize {
		rotErr = rotate(l.path)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		return errors.Join(rotErr, err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return errors.Join(rotErr, err)
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return errors.Join(rotErr, err)
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(rotErr, err)
}

// OldFiles is how many rotated files (<file>.1 newest … <file>.N oldest) are kept.
const OldFiles = 5

// logFiles lists the existing log files of path, oldest first (path.N … path.1, path).
func logFiles(path string) []string {
	var files []string
	for i := OldFiles; i >= 1; i-- {
		if p := path + "." + strconv.Itoa(i); fileExists(p) {
			files = append(files, p)
		}
	}
	if fileExists(path) {
		files = append(files, path)
	}
	return files
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// rotate shifts path.1 → path.2 … and path → path.1, dropping the oldest file. It goes
// from the oldest down, so a failure part-way never overwrites a newer file.
func rotate(path string) error {
	for i := OldFiles; i >= 1; i-- {
		from := path
		if i > 1 {
			from = path + "." + strconv.Itoa(i-1)
		}
		if !fileExists(from) {
			continue
		}
		if err := os.Rename(from, path+"."+strconv.Itoa(i)); err != nil {
			return fmt.Errorf("rotate activity log: %w", err)
		}
	}
	return nil
}

// VerificationResult describes the outcome of verifying an audit log's cryptographic chain.
type VerificationResult struct {
	Valid         bool   `json:"valid"`
	VerifiedCount int    `json:"verifiedCount"`
	FirstSeq      int64  `json:"firstSeq,omitempty"`
	LastSeq       int64  `json:"lastSeq,omitempty"`
	HeadHash      string `json:"headHash,omitempty"`
	TamperedSeq   int64  `json:"tamperedSeq,omitempty"`
	TamperedIndex int    `json:"tamperedIndex,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func (l *Log) loadDiskEntries() ([]Entry, *VerificationResult, error) {
	if l.path == "" {
		out := make([]Entry, len(l.ring))
		copy(out, l.ring)
		return out, nil, nil
	}

	files := logFiles(l.path)
	if len(files) == 0 {
		return nil, nil, nil
	}

	var entries []Entry
	for _, fp := range files {
		f, err := os.Open(fp)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil {
				_ = f.Close()
				tamper := &VerificationResult{
					Valid:         false,
					VerifiedCount: len(entries),
					TamperedIndex: len(entries),
					Reason:        fmt.Sprintf("malformed json entry: %v", err),
				}
				return entries, tamper, nil
			}
			entries = append(entries, e)
		}
		_ = f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
	}
	return entries, nil, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func verifySlice(entries []Entry) VerificationResult {
	if len(entries) == 0 {
		return VerificationResult{Valid: true, VerifiedCount: 0}
	}

	// Skip legacy unhashed entries (written before cryptographic chaining).
	chainStart := 0
	for chainStart < len(entries) && entries[chainStart].Hash == "" {
		e := entries[chainStart]
		if e.Seq != 0 || e.PrevHash != "" {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: 0,
				TamperedIndex: chainStart,
				TamperedSeq:   e.Seq,
				Reason:        fmt.Sprintf("missing hash on entry %d (seq %d)", chainStart, e.Seq),
			}
		}
		chainStart++
	}

	// If all entries are legacy unhashed entries, log is valid with 0 verified chained entries.
	if chainStart == len(entries) {
		return VerificationResult{
			Valid:         true,
			VerifiedCount: 0,
		}
	}

	first := entries[chainStart]
	if chainStart > 0 {
		// When starting after legacy entries, the first chained entry must anchor at GenesisHash.
		if first.PrevHash != GenesisHash {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: 0,
				TamperedIndex: chainStart,
				TamperedSeq:   first.Seq,
				Reason:        fmt.Sprintf("chain anchor prevHash mismatch at entry %d (seq %d): expected %s, got %s", chainStart, first.Seq, GenesisHash, first.PrevHash),
			}
		}
	} else {
		// chainStart == 0
		if first.Seq == 1 {
			// Unrotated genesis entry must point to GenesisHash.
			if first.PrevHash != GenesisHash {
				return VerificationResult{
					Valid:         false,
					VerifiedCount: 0,
					TamperedIndex: 0,
					TamperedSeq:   first.Seq,
					Reason:        fmt.Sprintf("genesis prevHash mismatch at entry 0 (seq %d): expected %s, got %s", first.Seq, GenesisHash, first.PrevHash),
				}
			}
		} else {
			// Rotated log where genesis entry was rotated away.
			if first.Seq <= 0 {
				return VerificationResult{
					Valid:         false,
					VerifiedCount: 0,
					TamperedIndex: 0,
					TamperedSeq:   first.Seq,
					Reason:        fmt.Sprintf("invalid sequence number at entry 0: %d", first.Seq),
				}
			}
			if !isHex64(first.PrevHash) {
				return VerificationResult{
					Valid:         false,
					VerifiedCount: 0,
					TamperedIndex: 0,
					TamperedSeq:   first.Seq,
					Reason:        fmt.Sprintf("invalid prevHash format at entry 0 (seq %d): %s", first.Seq, first.PrevHash),
				}
			}
		}
	}

	// Verify first chained entry's hash
	expectedStart := ComputeHash(first)
	if first.Hash != expectedStart {
		return VerificationResult{
			Valid:         false,
			VerifiedCount: 0,
			TamperedIndex: chainStart,
			TamperedSeq:   first.Seq,
			Reason:        fmt.Sprintf("hash mismatch at entry %d (seq %d): expected %s, got %s", chainStart, first.Seq, expectedStart, first.Hash),
		}
	}

	for i := chainStart + 1; i < len(entries); i++ {
		if entries[i].Hash == "" {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: i - chainStart,
				TamperedIndex: i,
				TamperedSeq:   entries[i].Seq,
				Reason:        fmt.Sprintf("unhashed entry at entry %d after cryptographic chain started", i),
			}
		}

		// d) e[i].Seq == e[i-1].Seq + 1
		if entries[i].Seq != entries[i-1].Seq+1 {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: i - chainStart,
				TamperedIndex: i,
				TamperedSeq:   entries[i].Seq,
				Reason:        fmt.Sprintf("sequence breakage at entry %d: expected seq %d, got %d", i, entries[i-1].Seq+1, entries[i].Seq),
			}
		}

		// b) e[i].PrevHash == e[i-1].Hash
		if entries[i].PrevHash != entries[i-1].Hash {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: i - chainStart,
				TamperedIndex: i,
				TamperedSeq:   entries[i].Seq,
				Reason:        fmt.Sprintf("prevHash mismatch at entry %d (seq %d): expected %s, got %s", i, entries[i].Seq, entries[i-1].Hash, entries[i].PrevHash),
			}
		}

		// c) e[i].Hash == ComputeHash(e[i])
		expected := ComputeHash(entries[i])
		if entries[i].Hash != expected {
			return VerificationResult{
				Valid:         false,
				VerifiedCount: i - chainStart,
				TamperedIndex: i,
				TamperedSeq:   entries[i].Seq,
				Reason:        fmt.Sprintf("hash mismatch at entry %d (seq %d): expected %s, got %s", i, entries[i].Seq, expected, entries[i].Hash),
			}
		}
	}

	return VerificationResult{
		Valid:         true,
		VerifiedCount: len(entries) - chainStart,
		FirstSeq:      entries[chainStart].Seq,
		LastSeq:       entries[len(entries)-1].Seq,
		HeadHash:      entries[len(entries)-1].Hash,
	}
}

// Verify opens the log file(s) from disk and verifies the full cryptographic chain.
func (l *Log) Verify() (VerificationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries, tamper, err := l.loadDiskEntries()
	if err != nil {
		return VerificationResult{}, err
	}
	if tamper != nil {
		return *tamper, nil
	}
	res := verifySlice(entries)
	if res.Valid && l.lastHash != "" && l.lastHash != GenesisHash && res.HeadHash != l.lastHash {
		return VerificationResult{
			Valid:         false,
			VerifiedCount: res.VerifiedCount,
			TamperedIndex: len(entries),
			TamperedSeq:   l.lastSeq,
			Reason:        fmt.Sprintf("head hash mismatch with in-memory log: expected %s (seq %d), got %s (seq %d)", l.lastHash, l.lastSeq, res.HeadHash, res.LastSeq),
		}, nil
	}
	return res, nil
}

// sanitizeCSVCell neutralizes spreadsheet formula injection in exported audit
// cells: values starting with =, +, -, @, tab or CR would execute as formulas
// when the CSV is opened in Excel or LibreOffice, so they get a leading
// apostrophe (TAP / CVE-2023-23917 style mitigation).
func sanitizeCSVCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// Export streams all disk entries in either "jsonl" or "csv" format with full Seq,
// PrevHash, Hash fields, and header comments/metadata indicating chain status.
func (l *Log) Export(w io.Writer, format string) error {
	f := strings.ToLower(strings.TrimSpace(format))
	if f != "jsonl" && f != "csv" {
		return fmt.Errorf("unsupported export format: %q (expected jsonl or csv)", format)
	}

	l.mu.Lock()
	entries, tamper, err := l.loadDiskEntries()
	l.mu.Unlock()
	if err != nil {
		return err
	}

	var res VerificationResult
	if tamper != nil {
		res = *tamper
	} else {
		res = verifySlice(entries)
	}

	switch f {
	case "csv":
		var comment string
		if res.Valid {
			comment = fmt.Sprintf("# virtoMir audit log export; chain_valid=true; verified_count=%d; first_seq=%d; last_seq=%d; head_hash=%s\n",
				res.VerifiedCount, res.FirstSeq, res.LastSeq, res.HeadHash)
		} else {
			comment = fmt.Sprintf("# virtoMir audit log export; chain_valid=false; verified_count=%d; tampered_seq=%d; tampered_index=%d; reason=%s\n",
				res.VerifiedCount, res.TamperedSeq, res.TamperedIndex, res.Reason)
		}
		if _, err := io.WriteString(w, comment); err != nil {
			return err
		}

		cw := csv.NewWriter(w)
		if err := cw.Write([]string{"seq", "prevHash", "hash", "time", "user", "token", "remote", "method", "path", "status", "error"}); err != nil {
			return err
		}
		for _, e := range entries {
			row := []string{
				strconv.FormatInt(e.Seq, 10),
				e.PrevHash,
				e.Hash,
				e.Time.UTC().Format(time.RFC3339Nano),
				sanitizeCSVCell(e.User),
				sanitizeCSVCell(e.Token),
				sanitizeCSVCell(e.Remote),
				sanitizeCSVCell(e.Method),
				sanitizeCSVCell(e.Path),
				strconv.Itoa(e.Status),
				sanitizeCSVCell(e.Error),
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()

	case "jsonl":
		header := map[string]any{
			"_metadata": res,
		}
		metaBytes, err := json.Marshal(header)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(metaBytes, '\n')); err != nil {
			return err
		}
		for _, e := range entries {
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if _, err := w.Write(append(b, '\n')); err != nil {
				return err
			}
		}
		return nil
	}

	return nil
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

// AttachSIEM connects an asynchronous RFC 5424 forwarder to the activity log.
func (l *Log) AttachSIEM(f *Forwarder) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.siem = f
}

// SIEM returns the current forwarder, if any.
func (l *Log) SIEM() *Forwarder {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.siem
}
