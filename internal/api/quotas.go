package api

import (
	"net/http"
	"strings"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/quota"
)

// Quotas (ADR-0022): operators covered by a rule may create machines within it and manage
// the machines they own; admins are not limited.
func (s *Server) quotaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/quotas", s.getQuotas)
	mux.HandleFunc("PUT /api/v1/quotas", s.putQuotas)
	mux.HandleFunc("GET /api/v1/quotas/mine", s.myQuota)
}

func (s *Server) getQuotas(w http.ResponseWriter, _ *http.Request) {
	if s.quotas == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"rules": []quota.Rule{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"rules": s.quotas.Rules()})
}

func (s *Server) putQuotas(w http.ResponseWriter, r *http.Request) {
	if s.quotas == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "quotas are not available on this host"})
		return
	}
	var b struct {
		Rules []quota.Rule `json:"rules"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.quotas.Set(b.Rules); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"rules": s.quotas.Rules()})
}

// myQuota: the caller's rule and usage (none: they cannot create machines themselves).
func (s *Server) myQuota(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	out := map[string]any{"unlimited": id.Role >= auth.Admin}
	if rule, ok := s.quotaFor(id.Name); ok && id.Role < auth.Admin {
		out["rule"] = rule
		out["used"] = s.usage(id.Name)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) quotaFor(user string) (quota.Rule, bool) {
	if s.quotas == nil || user == "" {
		return quota.Rule{}, false
	}
	return s.quotas.For(user, s.groupsOf(user))
}

// usage adds up the machines user owns on this host.
func (s *Server) usage(user string) quota.Usage {
	var u quota.Usage
	doms, err := s.backend.ListDomains()
	if err != nil {
		return u
	}
	for _, d := range doms {
		if d.Owner == user {
			u.Machines++
			u.VCPUs += d.VCPUs
			u.MemoryMiB += d.MaxMemoryKiB >> 10
		}
	}
	return u
}

// ownerOf is who created a machine through VirtoMir ("" when unknown).
func (s *Server) ownerOf(uuid string) string {
	doms, err := s.backend.ListDomains()
	if err != nil {
		return ""
	}
	for _, d := range doms {
		if strings.EqualFold(d.UUID, uuid) {
			return d.Owner
		}
	}
	return ""
}

// selfService reports whether an operator may do an admin-level request anyway: create a
// machine within their quota, or manage (settings, rename, delete…) a machine they own.
// XML edits, host devices and moves stay admin-only: they could bypass the quota or the
// host's safety checks.
func (s *Server) selfService(id auth.Identity, method, path string) bool {
	if s.quotas == nil || id.Role != auth.Operator {
		return false
	}
	if _, ok := s.quotaFor(id.Name); !ok {
		return false
	}
	if method == http.MethodPost && path == "/api/v1/domains" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/api/v1/domains/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if method == http.MethodPost && len(parts) == 2 && parts[1] == "deploy" {
		return true // a machine from a template: checked against the quota by the handler
	}
	if s.ownerOf(parts[0]) != id.Name {
		return false
	}
	op := method + " " + strings.Join(parts[1:], "/")
	switch {
	case op == "DELETE ",
		op == "PUT settings", op == "POST settings/preview",
		op == "PUT autostart", op == "POST rename",
		method == http.MethodDelete && len(parts) == 3 && parts[1] == "snapshots":
		return true
	}
	return false
}

// checkQuota answers 403 and returns false when a non-admin would exceed their quota by
// adding (machines, vcpus, memory).
func (s *Server) checkQuota(w http.ResponseWriter, r *http.Request, machines, vcpus int, memoryMiB uint64) bool {
	id := identity(r)
	if id.Role >= auth.Admin || s.quotas == nil {
		return true
	}
	rule, ok := s.quotaFor(id.Name)
	if !ok {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "you have no quota for creating machines; ask an admin"})
		return false
	}
	if err := quota.Check(rule, s.usage(id.Name), machines, vcpus, memoryMiB); err != nil {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

// recordOwner marks who created a machine.
func (s *Server) recordOwner(r *http.Request, uuid string) {
	o, ok := s.backend.(interface {
		SetOwner(uuid, owner string) error
	})
	if !ok || uuid == "" {
		return
	}
	if err := o.SetOwner(uuid, identity(r).Name); err != nil {
		s.log.Warn("record owner", "domain", uuid, "err", err)
	}
}
