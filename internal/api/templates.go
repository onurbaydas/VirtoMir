package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/respool"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Templater is the template part of the backend (implemented by *virt.Client).
type Templater interface {
	SetTemplate(uuid string, on bool) error
	Deploy(ctx context.Context, template string, spec virt.DeploySpec, report func(string)) (string, error)
}

func (s *Server) templateRoutes(mux *routeMux) {
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/template", s.setTemplate)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/template/prepare", s.prepareTemplate)
	mux.HandleFunc("POST /api/v1/domains/{uuid}/deploy", s.deploy)
}

func (s *Server) templater(w http.ResponseWriter) (Templater, bool) {
	t, ok := s.backend.(Templater)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "templates are not available"})
	}
	return t, ok
}

func (s *Server) setTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.templater(w)
	if !ok {
		return
	}
	var b struct {
		Template bool `json:"template"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	if err := t.SetTemplate(r.PathValue("uuid"), b.Template); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// deploy creates a machine from a template in the background (full copies take minutes).
func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.templater(w)
	if !ok {
		return
	}
	var b virt.DeploySpec
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	vcpus, mem := 1, uint64(1024)
	if doms, err := s.backend.ListDomains(); err == nil {
		for _, d := range doms {
			if strings.EqualFold(d.UUID, uuid) {
				vcpus, mem = d.VCPUs, d.MaxMemoryKiB>>10
			}
		}
	}
	// The new machine's disks are the template's, full or linked (a linked overlay can grow
	// to the same size).
	if identity(r).Role < auth.Admin && !s.checkQuota(w, r, 1, vcpus, mem, s.diskGiB([]string{uuid})) {
		return
	}
	rp := r.URL.Query().Get("respool") // the new machine's resource pool, if any
	if rp != "" && !s.checkRespool(w, rp, "", respool.Demand{VCPUs: vcpus, MemoryMiB: mem}) {
		return
	}
	job := s.jobs.Start("deploy", "New machine "+b.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := t.Deploy(ctx, uuid, b, report)
		if err == nil {
			s.recordOwner(r, res)
			s.joinRespool(rp, res)
		}
		s.live.Refresh()
		return res, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// linuxPrep makes a Linux guest anonymous for copies: a new machine ID and new SSH host keys
// on first boot of every copy (cloud-init clean does both where cloud-init is installed).
const linuxPrep = `set -e
if command -v cloud-init >/dev/null 2>&1; then cloud-init clean --logs --machine-id; else truncate -s0 /etc/machine-id; fi
rm -f /var/lib/dbus/machine-id /etc/ssh/ssh_host_*
sync`

// windowsSysprep generalizes Windows and shuts it down when done (minutes).
var windowsSysprep = []string{`C:\Windows\System32\Sysprep\sysprep.exe`, "/generalize", "/oobe", "/shutdown", "/quiet"}

// templateWait is how long a guest may take to shut down after preparing (sysprep is slow).
var templateWait = 20 * time.Minute

// prepareTemplate does what "make a template" used to ask the admin to do inside the machine:
// through the guest agent it makes the system anonymous (Linux: machine ID and SSH host keys;
// Windows: sysprep), shuts it down, waits until it is off and makes it a template.
func (s *Server) prepareTemplate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.templater(w)
	if !ok {
		return
	}
	g, okExec := s.backend.(GuestExecer)
	agent, okAgent := s.backend.(GuestAgent)
	if !okExec || !okAgent {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "running programs in machines is not available"})
		return
	}
	uuid := r.PathValue("uuid")
	info, err := agent.GuestInfo(uuid)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !info.Connected {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the machine's guest agent does not answer: start the machine and install qemu-guest-agent in it, or prepare it by hand"})
		return
	}
	windows := strings.Contains(strings.ToLower(info.OS), "windows")
	name := uuid
	if d, err := s.backend.DomainDetail(uuid); err == nil {
		name = d.Name
	}
	job := s.jobs.Start("template", "Prepare "+name+" as a template", func(ctx context.Context, report func(string)) (string, error) {
		report("Making the system anonymous…")
		if windows {
			// sysprep runs for minutes and shuts the machine down itself; do not wait for it here.
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := g.GuestExec(c, uuid, windowsSysprep)
			cancel()
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return "", fmt.Errorf("sysprep did not start: %w", err)
			}
		} else {
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			res, err := g.GuestExec(c, uuid, []string{"/bin/sh", "-c", linuxPrep})
			cancel()
			if err != nil {
				return "", err
			}
			if res.ExitCode != 0 {
				return "", fmt.Errorf("making the system anonymous failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
			}
			report("Shutting down…")
			if err := s.backend.DomainAction(uuid, virt.ActionShutdown); err != nil {
				return "", err
			}
		}
		report("Waiting for the machine to shut down…")
		if err := s.waitShutoff(ctx, uuid, templateWait); err != nil {
			return "", err
		}
		report("Making it a template…")
		if err := t.SetTemplate(uuid, true); err != nil {
			return "", err
		}
		s.live.Refresh()
		return uuid, nil
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// waitShutoff waits until the machine is off, or says it did not shut down in time.
func (s *Server) waitShutoff(ctx context.Context, uuid string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		if doms, err := s.backend.ListDomains(); err == nil {
			for _, d := range doms {
				if strings.EqualFold(d.UUID, uuid) && d.State == virt.StateShutoff {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the machine did not shut down within %s; it is prepared, shut it down and make it a template", limit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
