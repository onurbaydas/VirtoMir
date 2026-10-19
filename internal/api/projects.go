package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Projects (ADR-0060): multi-tenancy. A machine belongs to at most one project, stored
// next to its owner in the domain's <metadata>; a machine without a project is global and
// behaves exactly as before. Project checks run in addition to (not instead of) user
// quotas and resource pools; global admins pass them all.

// projectUsage adds up the machines of one project on this host.
func (s *Server) projectUsage(projectID string) project.Usage {
	var u project.Usage
	doms, err := s.backend.ListDomains()
	if err != nil {
		return u
	}
	var mine []string
	for _, d := range doms {
		if d.Project == projectID {
			u.Machines++
			u.VCPUs += d.VCPUs
			u.MemoryMiB += int64(d.MaxMemoryKiB >> 10)
			mine = append(mine, d.UUID)
			if d.GPUPool != "" && d.GPUCount > 0 {
				if u.GPUs == nil {
					u.GPUs = map[string]int{}
				}
				u.GPUs[d.GPUPool] += d.GPUCount
			}
		}
	}
	u.DiskGiB = int64(s.diskGiB(mine))
	return u
}

// projectOf is the project a machine belongs to ("" when global).
func (s *Server) projectOf(uuid string) string {
	doms, err := s.backend.ListDomains()
	if err != nil {
		return ""
	}
	for _, d := range doms {
		if strings.EqualFold(d.UUID, uuid) {
			return d.Project
		}
	}
	return ""
}

// projectOperator reports whether the user may change things in the project: they must be
// a global admin or an operator (or better) in the project itself.
func (s *Server) projectOperator(projectID, user string, role auth.Role) bool {
	if role >= auth.Admin {
		return true
	}
	r, ok := s.projects.RoleFor(projectID, user, s.groupsOf(user))
	return ok && (r == "operator" || r == "admin")
}

// checkProject answers 403/409 and returns false when the caller may not put demand into
// project projectID: they must be a global admin or an operator in the project, the storage
// pool and every network must be on the project's allowed lists, and the project's limits
// must cover the demand on top of what its machines use now. An empty projectID (a global
// machine) or no project store at all checks nothing.
func (s *Server) checkProject(w http.ResponseWriter, user string, role auth.Role, projectID string, demand project.Usage, pool string, networks []string) bool {
	if s.projects == nil || projectID == "" {
		return true
	}
	p, ok := s.projects.Get(projectID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no project " + projectID})
		return false
	}
	if !s.projectOperator(projectID, user, role) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "you are not an operator in project " + projectID})
		return false
	}
	if pool != "" && !slices.Contains(p.StoragePools, pool) {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "storage pool " + pool + " is not allowed in project " + projectID})
		return false
	}
	for _, n := range networks {
		if n != "" && !slices.Contains(p.Networks, n) {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": "network " + n + " is not allowed in project " + projectID})
			return false
		}
	}
	if err := p.Limits.Exceeded(s.projectUsage(projectID), demand); err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

// projectRoleRank orders project roles like auth.Role orders global ones.
var projectRoleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

// effectiveRole is the caller's global role, raised to their role in the project of the
// machine the path is about (ADR-0060): a project viewer may view that project's machines,
// a project operator may run them, a project admin may also change and delete them. Paths
// that are not about one machine, and machines without a project, keep the global role.
func (s *Server) effectiveRole(id auth.Identity, path string) auth.Role {
	rest, ok := strings.CutPrefix(path, "/api/v1/domains/")
	if !ok || s.projects == nil {
		return id.Role
	}
	uuid, _, _ := strings.Cut(rest, "/")
	pid := s.projectOf(uuid)
	if pid == "" {
		return id.Role
	}
	r, ok := s.projects.RoleFor(pid, id.Name, s.groupsOf(id.Name))
	if !ok {
		return id.Role
	}
	switch projectRoleRank[r] {
	case 3:
		return max(id.Role, auth.Admin)
	case 2:
		return max(id.Role, auth.Operator)
	}
	return max(id.Role, auth.Viewer)
}

// memberAtLeast reports whether the caller has at least the given role in project pid,
// directly or through one of their groups; an empty project puts no burden on anyone.
func (s *Server) memberAtLeast(pid string, id auth.Identity, min string) bool {
	if pid == "" {
		return true
	}
	r, ok := s.projects.RoleFor(pid, id.Name, s.groupsOf(id.Name))
	return ok && projectRoleRank[r] >= projectRoleRank[min]
}

func (s *Server) projectRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/projects", s.listProjects)
	mux.HandleFunc("POST /api/v1/projects", s.createProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", s.getProject)
	mux.HandleFunc("PUT /api/v1/projects/{id}", s.updateProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", s.deleteProject)
	// Moving a machine into or out of a project needs Operator only here: the handler
	// checks membership in both the source and the target project and the target's limits.
	mux.HandleFunc("PUT /api/v1/domains/{uuid}/project", s.setMachineProject)
}

// listProjects: global admins see every project, everyone else only the ones they are a
// member of (ProjectsFor, directly or through one of their groups).
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"projects": []project.Project{}})
		return
	}
	id := identity(r)
	if id.Role >= auth.Admin {
		s.writeJSON(w, http.StatusOK, map[string]any{"projects": s.projects.List()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"projects": s.projects.ProjectsFor(id.Name, s.groupsOf(id.Name))})
}

// getProject: members and global admins; anyone else gets the same 404 as an unknown ID,
// so they cannot even probe which projects exist.
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no projects on this host"})
		return
	}
	id := identity(r)
	p, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no project " + r.PathValue("id")})
		return
	}
	if id.Role < auth.Admin {
		if _, member := s.projects.RoleFor(p.ID, id.Name, s.groupsOf(id.Name)); !member {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no project " + p.ID})
			return
		}
	}
	s.writeJSON(w, http.StatusOK, p)
}

// projectErrStatus maps the store's errors to HTTP like writeError does for the backend.
func projectErrStatus(err error) int {
	if strings.Contains(err.Error(), "exists already") {
		return http.StatusConflict
	}
	if strings.Contains(err.Error(), "no project ") {
		return http.StatusNotFound
	}
	return http.StatusUnprocessableEntity
}

// projectToMembers repeats a project change on the other members, so every host of the
// cluster has the same projects (ADR-0060: one set per cluster, like the alert settings
// saved on every host). ?local=1 marks the copies, which are not passed on again, the
// same way the DRS rules mark theirs. The answer says per member whether the change
// arrived, so callers can name the members that need a retry.
func (s *Server) projectToMembers(r *http.Request, method, path string, body any) []HostResult {
	if r.URL.Query().Get("local") != "" || s.cluster == nil || !s.cluster.Clustered() {
		return nil
	}
	id := identity(r)
	members, _ := s.cluster.Members()
	results := make([]HostResult, 0, len(members))
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		results = append(results, HostResult{Host: n.Name})
		res := &results[len(results)-1]
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			res.OK = true
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, method, path+"?local=1", body, nil); err != nil {
				res.OK, res.Error = false, err.Error()
				s.log.Warn("project change not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
	return results
}

// createProject is admin-only (the guard sees to that). In a cluster the change is copied
// to every member, and the answer names the members that did not take it.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "projects are not available on this host"})
		return
	}
	var p project.Project
	if !s.decode(w, r, &p) {
		return
	}
	created, err := s.projects.Create(p)
	if err != nil {
		s.writeJSON(w, projectErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	results := s.projectToMembers(r, http.MethodPost, "/api/v1/projects", created)
	answer := map[string]any{"project": created}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusCreated, answer)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "projects are not available on this host"})
		return
	}
	var p project.Project
	if !s.decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	if err := s.projects.Update(p); err != nil {
		s.writeJSON(w, projectErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	next, _ := s.projects.Get(p.ID)
	results := s.projectToMembers(r, http.MethodPut, "/api/v1/projects/"+p.ID, next)
	answer := map[string]any{"project": next}
	if results != nil {
		answer["results"] = results
	}
	s.done(w, http.StatusOK, answer)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "projects are not available on this host"})
		return
	}
	if err := s.projects.Delete(r.PathValue("id")); err != nil {
		s.writeJSON(w, projectErrStatus(err), map[string]string{"error": err.Error()})
		return
	}
	results := s.projectToMembers(r, http.MethodDelete, "/api/v1/projects/"+r.PathValue("id"), nil)
	// The project is gone here whatever the members did; a full sweep stays the silent
	// 204 it always was, and a member that could not be reached is named in the answer
	// instead (it keeps the project until the change is made there again).
	if slices.ContainsFunc(results, func(h HostResult) bool { return !h.OK }) {
		s.done(w, http.StatusOK, map[string]any{"results": results})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// setMachineProject moves a machine into or out of a project (ADR-0060): a global admin may
// move anywhere; anyone else needs to be an operator (or better) in both the source and the
// target project, and the target's limits must cover the machine on top of what its machines
// use now. An empty project makes the machine global again.
func (s *Server) setMachineProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "projects are not available on this host"})
		return
	}
	var b struct {
		Project string `json:"project"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	uuid := r.PathValue("uuid")
	id := identity(r)
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	var dom *virt.Domain
	for i := range doms {
		if strings.EqualFold(doms[i].UUID, uuid) {
			dom = &doms[i]
			break
		}
	}
	if dom == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no machine " + uuid})
		return
	}
	if id.Role < auth.Admin && !(s.memberAtLeast(dom.Project, id, "operator") && s.memberAtLeast(b.Project, id, "operator")) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "you need to be an operator in both the source and the target project"})
		return
	}
	if b.Project != "" && !strings.EqualFold(b.Project, dom.Project) {
		p, ok := s.projects.Get(b.Project)
		if !ok {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no project " + b.Project})
			return
		}
		demand := project.Usage{Machines: 1, VCPUs: dom.VCPUs, MemoryMiB: int64(dom.MaxMemoryKiB >> 10), DiskGiB: int64(s.diskGiB([]string{uuid}))}
		if err := p.Limits.Exceeded(s.projectUsage(b.Project), demand); err != nil {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
	}
	if o, ok := s.backend.(interface {
		SetProject(uuid, projectID string) error
	}); ok {
		if err := o.SetProject(uuid, b.Project); err != nil {
			s.log.Warn("record project", "domain", uuid, "err", err)
		}
	}
	s.done(w, http.StatusNoContent, nil)
}

// recordProject marks which project a machine belongs to.
func (s *Server) recordProject(uuid, projectID string) {
	if projectID == "" || uuid == "" {
		return
	}
	o, ok := s.backend.(interface {
		SetProject(uuid, projectID string) error
	})
	if !ok {
		return
	}
	if err := o.SetProject(uuid, projectID); err != nil {
		s.log.Warn("record project", "domain", uuid, "err", err)
	}
}
