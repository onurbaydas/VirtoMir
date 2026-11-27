// Conversion of VMware machines with the real virt-v2v binary (ADR-0061), and adoption
// of the result as a VirtoMir machine. Experimental, not tested on hardware: the command
// line follows the virt-v2v 2.x documentation, but no real vCenter or ESXi host has been
// migrated through this code yet.
//
// The exec pattern follows internal/offsite (restic: an Exec func field, the real runner
// streaming stdout line by line with stderr captured); the pool and disk handling follows
// internal/virt/ova.go (OVA import: images land in a directory pool, and a failure leaves
// nothing behind).
package vmware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

// Exec runs virt-v2v with the given environment; lines of stdout go to onLine as they
// come. env is nil today — the password travels in a password file, never the
// environment — and exists so the shape matches internal/offsite.
type Exec func(ctx context.Context, env []string, args []string, onLine func(string)) (stdout []byte, err error)

// LookPath reports whether a binary is installed (exec.LookPath shape).
type LookPath func(bin string) (string, error)

// Converter migrates VMware machines: it runs virt-v2v and defines the result through
// the libvirt client. exec and look are replaceable in tests.
type Converter struct {
	client *virt.Client
	exec   Exec
	look   LookPath
}

// NewConverter migrates with the real virt-v2v and defines through client.
func NewConverter(client *virt.Client) *Converter {
	return &Converter{client: client, exec: execV2V, look: exec.LookPath}
}

// Issues lists every failed pre-flight check in one error, so the API can show them all
// at once (errors.As yields the []string itself). They count as invalid input.
type Issues []string

func (e Issues) Error() string        { return strings.Join(e, "; ") }
func (e Issues) Is(target error) bool { return target == ErrInvalid }

// Preflight checks everything that would stop a migration before it starts, and returns
// every failed check at once. A Windows guest without a provided virtio driver ISO is
// not one of them: virt-v2v injects the drivers it finds itself (Convert reports a note
// about it instead).
func (s *Converter) Preflight(ctx context.Context, c Connection, vm VM, poolFreeBytes uint64) error {
	var iss Issues
	if !c.Configured() {
		iss = append(iss, "Set up the VMware connection first.")
	}
	if _, err := s.look("virt-v2v"); err != nil {
		iss = append(iss, "virt-v2v is not installed on this host (e.g. apt install virt-v2v).")
	} else if _, err := s.exec(ctx, nil, []string{"--version"}, nil); err != nil {
		iss = append(iss, "virt-v2v is installed but does not run: "+firstLine(err.Error(), err.Error()))
	}
	if vm.PowerState != "off" {
		iss = append(iss, fmt.Sprintf("%s is %s in VMware. Shut it down there first — warm or snapshot migration is not offered.", vm.Name, vm.PowerState))
	}
	var need uint64
	for _, d := range vm.Disks {
		need += d.CapacityBytes
	}
	if len(vm.Disks) == 0 {
		iss = append(iss, fmt.Sprintf("%s has no disks to migrate.", vm.Name))
	} else if poolFreeBytes < need || poolFreeBytes-need < need/10 { // 10% headroom
		iss = append(iss, fmt.Sprintf("The disks of %s need %s (with 10%% headroom), but the pool has only %s free.",
			vm.Name, humanBytes(need+need/10), humanBytes(poolFreeBytes)))
	}
	if len(iss) == 0 {
		return nil
	}
	return iss
}

// ConvertOpts is one migration.
type ConvertOpts struct {
	// PoolPath is the directory of the storage pool the disks land in (virt-v2v -os).
	PoolPath string `json:"poolPath"`
	// NetworkMap maps each VMware port group the VM's NICs use to a libvirt network
	// name; virt-v2v rewrites the interfaces accordingly (--network <group>:<network>).
	NetworkMap map[string]string `json:"networkMap,omitempty"`
	// Name of the machine in VirtoMir; the VMware VM's name when empty.
	Name string `json:"name,omitempty"`
	// VirtioISO is a directory or ISO with Windows virtio drivers (--inject-drivers);
	// virt-v2v injects the drivers it finds itself when it is empty.
	VirtioISO string `json:"virtioIso,omitempty"`
}

// Convert migrates one VMware machine with virt-v2v and adopts the result: the disks
// land as images in the pool directory, the produced definition is renamed to opts.Name
// and defined through the libvirt client (virt.Client.AdoptDomain, the same way a
// machine moved from another host is defined). It returns the new machine's UUID, so
// the caller records the owner and lets the configuration history capture the first
// version — exactly what a create through the API does (internal/api/domains.go).
//
// The VMware password reaches virt-v2v only through a 0600 password file that is gone
// when Convert returns; it is never on the command line and never in an error. On a
// failure after partial output, the disk files virt-v2v already wrote are removed and
// the pool refreshed, so a retried migration starts clean.
func (s *Converter) Convert(ctx context.Context, c Connection, vm VM, opts ConvertOpts, report func(string)) (string, error) {
	if !c.Configured() {
		return "", refusedf("Set up the VMware connection first.")
	}
	if vm.Name == "" || opts.PoolPath == "" {
		return "", invalidf("Choose the VMware machine and the storage pool to migrate into.")
	}
	if st, err := os.Stat(opts.PoolPath); err != nil || !st.IsDir() {
		return "", invalidf("%s is not a storage pool directory.", opts.PoolPath)
	}
	if opts.Name == "" {
		opts.Name = vm.Name
	}
	if report == nil {
		report = func(string) {}
	}
	ic, err := inputURL(c, vm)
	if err != nil {
		return "", err
	}
	if s.client != nil { // a taken name should fail now, not after hours of copying
		if doms, err := s.client.ListDomains(); err == nil {
			for _, d := range doms {
				if d.Name == opts.Name {
					return "", refusedf("A machine named %s already exists.", opts.Name)
				}
			}
		}
	}

	// The password reaches virt-v2v through a file CreateTemp makes with 0600, which is
	// gone when Convert returns — never the command line, never a log, never an error.
	pw, err := os.CreateTemp("", "virtomir-v2v-*.pw")
	if err != nil {
		return "", err
	}
	pwFile := pw.Name()
	defer os.Remove(pwFile)
	if _, err := pw.WriteString(c.Password + "\n"); err != nil {
		pw.Close()
		return "", err
	}
	if err := pw.Close(); err != nil {
		return "", err
	}

	before := dirFiles(opts.PoolPath)
	var disks []string
	xmlName := vm.Name + ".xml"
	// Nothing of a failed migration stays behind: what virt-v2v wrote into the pool is
	// removed and the pool refreshed — the cleanup() of the OVA import.
	cleanup := func() {
		s.removeCreated(opts.PoolPath, before, vm.Name, xmlName, disks)
		s.refreshPool(opts.PoolPath)
	}

	args := []string{
		"-ic", ic,
		"--password-file", pwFile,
		"-o", "local", // local output: VirtoMir keeps full control of the definition
		"-os", opts.PoolPath,
		"--machine-readable",
	}
	for _, src := range slices.Sorted(maps.Keys(opts.NetworkMap)) {
		args = append(args, "--network", src+":"+opts.NetworkMap[src])
	}
	if opts.VirtioISO != "" {
		args = append(args, "--inject-drivers", opts.VirtioISO)
	}
	args = append(args, vm.Name) // the guest by name (virt-v2v 2.x positional syntax)

	report("Converting " + vm.Name + " from VMware…")
	if opts.VirtioISO == "" && looksWindows(vm.GuestOS) {
		report("No virtio driver ISO was provided: virt-v2v injects the drivers it finds itself, and a Windows guest keeps slower emulated devices when it finds none.")
	}

	p := &v2vProgress{report: report}
	if _, err := s.exec(ctx, nil, args, p.line); err != nil {
		cleanup()
		return "", redact(v2vClassify(err), c.Password)
	}

	doc, written, err := findDefinition(opts.PoolPath, before, vm.Name)
	if err != nil {
		cleanup()
		return "", redact(err, c.Password)
	}
	xmlName = written

	out, disks, err := prepareDefinition(doc, opts.PoolPath, opts.Name)
	if err != nil {
		cleanup()
		return "", redact(err, c.Password)
	}

	if s.client == nil {
		cleanup()
		return "", refusedf("The converted machine cannot be defined: no libvirt connection.")
	}
	report("Defining " + opts.Name + "…")
	uuid, err := s.client.AdoptDomain(out)
	if err != nil {
		cleanup()
		return "", err
	}
	// The definition was consumed; the disks belong to the machine now.
	_ = os.Remove(filepath.Join(opts.PoolPath, xmlName))
	s.refreshPool(opts.PoolPath)
	report("Migrated " + vm.Name + " as " + opts.Name + ".")
	return uuid, nil
}

// execV2V runs the real virt-v2v binary.
func execV2V(ctx context.Context, env []string, args []string, onLine func(string)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "virt-v2v", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(stdout, &buf))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	if err := cmd.Wait(); err != nil {
		rawErr := stderr.String()
		if strings.TrimSpace(rawErr) == "" && buf.Len() > 0 {
			rawErr = buf.String()
		}
		return buf.Bytes(), errors.New(cleanV2VError(rawErr, err.Error()))
	}
	return buf.Bytes(), nil
}

// firstLine is the first line of a tool's message, trimmed and capped at 600
// characters, with a fallback — the shape internal/offsite gives restic's stderr.
func firstLine(s, fallback string) string {
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s = strings.TrimSpace(s); s == "" {
		return fallback
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

var (
	// reLibvirtTS matches leading libvirt timestamps with optional pid, e.g.:
	// "2026-10-19 01:23:45.123+0000: 12345: " or "2026-10-19 01:23:45.123+0000: "
	reLibvirtTS = regexp.MustCompile(`^\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?(?::\s*\d+)?:\s*`)
	// reBracketTS matches bracketed elapsed time or log prefixes, e.g.:
	// "[   0.0] " or "[12.345] "
	reBracketTS = regexp.MustCompile(`^\[\s*\d+(?:\.\d+)?\s*\]\s*`)
	// reSimpleTS matches standard ISO or date-time prefixes without a colon, e.g.:
	// "2026-10-19 01:23:45 "
	reSimpleTS = regexp.MustCompile(`^\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\s*`)
)

// stripTimestamp removes leading timestamps and log prefixes from a line.
func stripTimestamp(line string) string {
	line = strings.TrimSpace(line)
	for {
		if m := reLibvirtTS.FindString(line); m != "" {
			line = strings.TrimSpace(line[len(m):])
			continue
		}
		if m := reBracketTS.FindString(line); m != "" {
			line = strings.TrimSpace(line[len(m):])
			continue
		}
		if m := reSimpleTS.FindString(line); m != "" {
			line = strings.TrimSpace(line[len(m):])
			continue
		}
		break
	}
	return line
}

// cleanV2VError extracts the meaningful failure from virt-v2v output:
// preferring lines containing 'error' (case-insensitive), otherwise the last
// 5 non-empty lines, stripping libvirt 'info :' noise lines and timestamps,
// and capping at 1000 characters.
func cleanV2VError(output, fallback string) string {
	lines := strings.Split(output, "\n")
	var cleaned []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.Contains(strings.ToLower(line), "info :") {
			continue
		}
		line = stripTimestamp(line)
		if line == "" {
			continue
		}
		if strings.Contains(strings.ToLower(line), "info :") {
			continue
		}
		cleaned = append(cleaned, line)
	}

	var selected []string
	for _, l := range cleaned {
		if strings.Contains(strings.ToLower(l), "error") {
			selected = append(selected, l)
		}
	}
	if len(selected) == 0 {
		if len(cleaned) > 5 {
			selected = cleaned[len(cleaned)-5:]
		} else if len(cleaned) > 0 {
			selected = cleaned
		}
	}

	msg := strings.Join(selected, "\n")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = strings.TrimSpace(fallback)
	}
	if msg == "" {
		msg = "unknown error"
	}

	if !strings.HasPrefix(msg, "virt-v2v:") && !strings.HasPrefix(msg, "virt-v2v ") {
		msg = "virt-v2v: " + msg
	}

	runes := []rune(msg)
	if len(runes) > 1000 {
		msg = string(runes[:1000])
	}
	return msg
}

// v2vClassify turns a virt-v2v failure into one of VirtoMir's error classes, keeping
// the tool's meaningful failure message and a hint at what to fix.
func v2vClassify(err error) error {
	msg := strings.TrimSpace(err.Error())
	low := strings.ToLower(msg)
	switch {
	case containsAny(low, "not found", "no such", "cannot find", "does not exist"):
		return invalidf("%s — check the machine's name and its place in vCenter.", msg)
	case containsAny(low, "connection refused", "timed out", "timeout", "no route", "unreachable", "name or service not known"):
		return refusedf("%s — the vCenter or ESXi host could not be reached.", msg)
	case containsAny(low, "authentication", "password", "credentials", "permission denied", "login"):
		return refusedf("%s — check the VMware user and password.", msg)
	}
	return refusedf("%s", msg)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// inputURL builds virt-v2v's -ic URL: vpx://user@vcenter/<datacenter>/<host> for a
// vCenter connection, esx://user@host for a standalone ESXi one. The password never
// rides in it (it goes through --password-file). no_verify=1 because virt-v2v has no
// way to pin a thumbprint the way the inventory reader does.
func inputURL(c Connection, vm VM) (string, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return "", invalidf("The VMware connection has no valid address.")
	}
	iu := &url.URL{Host: u.Host, RawQuery: "no_verify=1"}
	iu.User = url.User(c.Username)
	if c.ESXi {
		iu.Scheme = "esx"
	} else {
		if vm.Datacenter == "" || vm.Host == "" {
			return "", invalidf("%s has no datacenter or host recorded; refresh the inventory first.", vm.Name)
		}
		iu.Scheme, iu.Path = "vpx", "/"+vm.Datacenter+"/"+vm.Host
	}
	return iu.String(), nil
}

// looksWindows tells a Windows guest from its inventory OS string.
func looksWindows(guestOS string) bool {
	return strings.Contains(strings.ToLower(guestOS), "windows")
}

// v2vProgress turns --machine-readable lines into report() lines. virt-v2v writes one
// JSON object per line; the disk counters stick, so a bare percentage still says which
// disk it belongs to. Lines that are not JSON are ignored.
type v2vProgress struct {
	report func(string)
	disk   int
	disks  int
	last   int
}

func (p *v2vProgress) line(line string) {
	var e struct {
		Type    string  `json:"type"`
		Value   string  `json:"value"`
		PerCent float64 `json:"per_cent"`
		Disk    int     `json:"disk"`
		Disks   int     `json:"disks"`
	}
	if json.Unmarshal([]byte(line), &e) != nil {
		return
	}
	if e.Disk > 0 {
		p.disk = e.Disk
	}
	if e.Disks > 0 {
		p.disks = e.Disks
	}
	if p.report == nil {
		return
	}
	switch e.Type {
	case "message":
		if e.Value != "" {
			p.report(e.Value)
		}
	case "warning":
		if e.Value != "" {
			p.report("virt-v2v warning: " + e.Value)
		}
	case "progress":
		if pct := int(e.PerCent); pct != p.last {
			p.last = pct
			if p.disk > 0 && p.disks > 0 {
				p.report(fmt.Sprintf("Converting disk %d of %d… %d %%", p.disk, p.disks, pct))
			} else {
				p.report(fmt.Sprintf("Converting… %d %%", pct))
			}
		}
	}
}

// findDefinition reads the definition virt-v2v wrote: <vm>.xml, or the one new .xml
// file in the pool when virt-v2v had to sanitize the guest's name.
func findDefinition(pool string, before map[string]bool, vmName string) ([]byte, string, error) {
	// The guest's name comes from VMware: it is a file name in the pool only if it has no
	// directory part ('/', "..").
	if own := vmName + ".xml"; filepath.IsLocal(own) && filepath.Base(own) == own {
		if doc, err := os.ReadFile(filepath.Join(pool, own)); err == nil {
			return doc, own, nil
		}
	}
	var news []string
	ents, err := os.ReadDir(pool)
	if err != nil {
		return nil, "", refusedf("virt-v2v finished, but %s cannot be read: %v.", pool, err)
	}
	for _, e := range ents {
		if !before[e.Name()] && strings.HasSuffix(e.Name(), ".xml") {
			news = append(news, e.Name())
		}
	}
	switch len(news) {
	case 1:
		doc, err := os.ReadFile(filepath.Join(pool, news[0]))
		if err != nil {
			return nil, "", refusedf("virt-v2v finished, but its definition cannot be read: %v.", err)
		}
		return doc, news[0], nil
	case 0:
		return nil, "", refusedf("virt-v2v finished, but wrote no machine definition into %s — see its messages above.", pool)
	}
	return nil, "", refusedf("virt-v2v finished, but %s ended up with several new definitions (%s).", pool, strings.Join(news, ", "))
}

// prepareDefinition makes virt-v2v's definition a VirtoMir machine: it carries the
// chosen name, absolute disk paths (-o local may write them relative to -os), and a
// console that stays on the host — VNC is never exposed directly (internal/virt/create.go).
// It also returns the disk files, so a failure can remove them.
func prepareDefinition(doc []byte, pool, name string) (string, []string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(string(doc)); err != nil {
		return "", nil, invalidf("virt-v2v's definition cannot be read: %v.", err)
	}
	x.Name = name
	var disks []string
	if x.Devices != nil {
		for i := range x.Devices.Disks {
			d := &x.Devices.Disks[i]
			if d.Source == nil || d.Source.File == nil || d.Source.File.File == "" {
				continue
			}
			if !filepath.IsAbs(d.Source.File.File) {
				d.Source.File.File = filepath.Join(pool, d.Source.File.File)
			}
			disks = append(disks, d.Source.File.File)
		}
		for i := range x.Devices.Graphics {
			if vnc := x.Devices.Graphics[i].VNC; vnc != nil {
				vnc.Port, vnc.AutoPort, vnc.Listen = -1, "yes", "127.0.0.1"
				vnc.Listeners = []libvirtxml.DomainGraphicListener{{Address: &libvirtxml.DomainGraphicListenerAddress{Address: "127.0.0.1"}}}
			}
		}
	}
	out, err := x.Marshal()
	if err != nil {
		return "", nil, err
	}
	return out, disks, nil
}

// dirFiles lists a directory's entries (an empty map when it cannot be read).
func dirFiles(dir string) map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		out[e.Name()] = true
	}
	return out
}

// removeCreated deletes what virt-v2v wrote into the pool during this run: the disks
// named in the definition (when there is one), the definition itself, and every new
// file that carries the source VM's name. Files that were already there, or appeared
// with another machine's name, are never touched.
func (s *Converter) removeCreated(pool string, before map[string]bool, vmName, xmlName string, disks []string) {
	ours := map[string]bool{filepath.Join(pool, xmlName): true}
	for _, d := range disks {
		ours[d] = true
	}
	ents, err := os.ReadDir(pool)
	if err != nil {
		return
	}
	for _, e := range ents {
		if before[e.Name()] {
			continue
		}
		p := filepath.Join(pool, e.Name())
		if ours[p] || carriesName(e.Name(), vmName) {
			_ = os.RemoveAll(p)
		}
	}
}

// carriesName tells whether a file name was derived from the source VM's name
// (virt-v2v names its output <guest>.xml and <guest>-diskN).
func carriesName(file, vmName string) bool {
	return file == vmName+".xml" || strings.HasPrefix(file, vmName+"-") || strings.HasPrefix(file, vmName+"_")
}

// refreshPool rescans the pool that owns path, so the new (or removed) images show up —
// best effort, the way the OVA import refreshes.
func (s *Converter) refreshPool(path string) {
	if s.client == nil {
		return
	}
	pools, err := s.client.ListPools()
	if err != nil {
		return
	}
	for _, p := range pools {
		if p.Path == path {
			_ = s.client.PoolAction(p.Name, "refresh")
			return
		}
	}
}

// humanBytes is a size for people (the shape internal/virt uses).
func humanBytes(b uint64) string {
	const gib = 1 << 30
	if b >= gib {
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%d MiB", b>>20)
}

// refusedf is a sentence for the user that errors.Is matches to ErrRefused.
func refusedf(format string, a ...any) error {
	return classed{ErrRefused, fmt.Sprintf(format, a...)}
}
