package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// First-run setup (ADR-0045): right after installing, an admin is walked through what a
// host needs before the first machine — hardware virtualization, a storage pool, a
// network, a backup location and an image to start from. The page reads this host's state
// and fixes it with the usual API calls; finishing it (or skipping) is remembered in
// <state-dir>/setup.json. A host that already has machines counts as set up.
func (s *Server) setupRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/setup", s.getSetup)
	mux.HandleFunc("POST /api/v1/setup/done", s.setupDone)
}

// SetupCheck is one thing the host needs, or should have.
type SetupCheck struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Needed bool   `json:"needed"` // without it, machines cannot work (otherwise: recommended)
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (s *Server) setupFile() string { return filepath.Join(s.stateDir, "setup.json") }

func (s *Server) setupIsDone() bool {
	if s.stateDir == "" {
		return true
	}
	if _, err := os.Stat(s.setupFile()); err == nil {
		return true
	}
	if doms, err := s.backend.ListDomains(); err == nil && len(doms) > 0 {
		s.markSetupDone() // an existing host, upgraded: nothing to walk through
		return true
	}
	return false
}

func (s *Server) markSetupDone() {
	b, _ := json.Marshal(map[string]any{"done": true, "at": time.Now().UTC()})
	_ = os.MkdirAll(s.stateDir, 0o755)
	_ = os.WriteFile(s.setupFile(), b, 0o644)
}

func (s *Server) setupDone(w http.ResponseWriter, _ *http.Request) {
	if s.stateDir == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.markSetupDone()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSetup(w http.ResponseWriter, _ *http.Request) {
	out := struct {
		Done      bool         `json:"done"`
		Arch      string       `json:"arch"`
		BackupDir string       `json:"backupDir,omitempty"`
		Checks    []SetupCheck `json:"checks"`
	}{Done: s.setupIsDone(), Checks: []SetupCheck{}}

	add := func(c SetupCheck) { out.Checks = append(out.Checks, c) }
	h, err := s.backend.HostInfo()
	if err != nil {
		add(SetupCheck{ID: "libvirt", Needed: true, Title: "libvirt", Detail: "VirtoMir cannot reach libvirt: " + err.Error() + ". Is libvirtd (or virtqemud) running?"})
	} else {
		add(SetupCheck{ID: "libvirt", OK: true, Needed: true, Title: "libvirt", Detail: "libvirt " + h.LibvirtVersion + ", " + h.Hypervisor + " " + h.HypervisorVersion + " on " + h.Hostname})
		out.Arch = h.CPUModel
	}

	cpu, _ := os.ReadFile("/proc/cpuinfo")
	flags := string(cpu)
	_, kvmErr := os.Stat("/dev/kvm")
	switch {
	case kvmErr == nil:
		add(SetupCheck{ID: "kvm", OK: true, Needed: true, Title: "Hardware virtualization", Detail: "/dev/kvm is there: machines run at full speed."})
	case strings.Contains(flags, " vmx") || strings.Contains(flags, " svm"):
		add(SetupCheck{ID: "kvm", Needed: true, Title: "Hardware virtualization", Detail: "The CPU supports it, but /dev/kvm is missing: load the module (modprobe kvm_intel or kvm_amd)."})
	default:
		add(SetupCheck{ID: "kvm", Needed: true, Title: "Hardware virtualization", Detail: "No /dev/kvm: turn on VT-x / AMD-V in the BIOS (on a virtual host: nested virtualization). Without it machines are emulated and very slow."})
	}

	if pools, err := s.backend.ListPools(); err == nil {
		c := SetupCheck{ID: "pool", Needed: true, Title: "Storage for disks and images", Detail: "No active directory or NFS pool yet: create one for disks and ISOs."}
		for _, p := range pools {
			if p.Active && (p.Type == "dir" || p.Type == "netfs" || p.Type == "fs") {
				c.OK, c.Detail = true, "Pool "+p.Name+" ("+p.Path+")."
				break
			}
		}
		add(c)
	}
	if nets, err := s.backend.ListNetworks(); err == nil {
		c := SetupCheck{ID: "network", Needed: true, Title: "A network for machines", Detail: "No active network: create a NAT network (machines reach the internet through the host) or a bridge to your LAN."}
		for _, n := range nets {
			if n.Active && n.Mode != "isolated" {
				c.OK, c.Detail = true, "Network "+n.Name+" ("+n.Mode+")."
				break
			}
		}
		add(c)
	}
	if s.schedules != nil {
		dir := s.schedules.Settings().BackupDir
		out.BackupDir = dir
		c := SetupCheck{ID: "backups", Title: "Backup location", Detail: "Backups go to " + dir + ". Another disk or an NFS mount is better than the system disk."}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			c.OK = true
		} else if err := os.MkdirAll(dir, 0o755); err == nil {
			c.OK = true
		} else {
			c.Detail = "Backups would go to " + dir + ", which cannot be created: " + err.Error()
		}
		add(c)
	}
	if os.Geteuid() != 0 {
		add(SetupCheck{ID: "root", Title: "Running as root", Detail: "virtomird runs as an ordinary user: backups of disks it cannot read and host network changes are not available."})
	}
	if _, err := exec.LookPath("restic"); err == nil {
		add(SetupCheck{ID: "offsite", OK: true, Title: "Off-site copies", Detail: "restic is installed; set up a repository on the Backups page."})
	} else {
		add(SetupCheck{ID: "offsite", Title: "Off-site copies", Detail: "Optional: install restic to copy backups to S3, B2, SFTP or another disk."})
	}
	s.writeJSON(w, http.StatusOK, out)
}
