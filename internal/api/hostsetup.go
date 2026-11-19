package api

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/hostsetup"
)

// Host chores VirtoMir does itself instead of telling the admin which command to run:
// installing a feature's packages, the IOMMU on the kernel command line, the HA watchdog and
// the SSH trust ZFS replication needs. Writes are admin-only (requiredRole's default).
func (s *Server) hostSetupRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/host/setup", s.getHostSetup)
	mux.HandleFunc("POST /api/v1/host/setup/packages/{feature}", s.installFeature)
	mux.HandleFunc("POST /api/v1/host/setup/iommu", s.enableIOMMU)
	mux.HandleFunc("POST /api/v1/host/setup/watchdog", s.enableWatchdog)
	mux.HandleFunc("POST /api/v1/host/setup/ssh-authorize", s.authorizeSSHKey)
	mux.HandleFunc("POST /api/v1/host/setup/ssh-trust", s.trustSSHTarget)
}

type featureState struct {
	Title     string `json:"title"`
	Installed bool   `json:"installed"`
}

func (s *Server) getHostSetup(w http.ResponseWriter, r *http.Request) {
	features := map[string]featureState{}
	for name, f := range hostsetup.Features {
		features[name] = featureState{Title: f.Title, Installed: s.hostSetup.Installed(name)}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"features": features, "watchdog": s.hostSetup.HasWatchdog()})
}

// installFeature installs in the background: the UI follows the job (and shows it in Tasks).
func (s *Server) installFeature(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("feature")
	f, ok := hostsetup.Features[name]
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "VirtoMir does not know how to set up " + name})
		return
	}
	job := s.jobs.Start("setup", "Install "+f.Title, func(ctx context.Context, report func(string)) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		if err := s.hostSetup.Install(ctx, name, report); err != nil {
			return "", err
		}
		return name, nil
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) enableIOMMU(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := s.hostSetup.EnableIOMMU(ctx)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.log.Info("IOMMU turned on in the kernel command line", "method", res.Method, "params", res.Params, "by", identity(r).Name)
	s.done(w, http.StatusOK, res)
}

func (s *Server) enableWatchdog(w http.ResponseWriter, r *http.Request) {
	if err := s.hostSetup.EnableWatchdog(r.Context()); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, map[string]bool{"watchdog": s.hostSetup.HasWatchdog()})
}

// authorizeSSHKey lets another member's root sign in here (for ZFS replication to this host).
func (s *Server) authorizeSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := s.hostSetup.Authorize(body.Key); err != nil {
		s.writeError(w, err)
		return
	}
	s.log.Info("SSH key authorized for root", "key", keyComment(body.Key), "by", identity(r).Name)
	s.done(w, http.StatusNoContent, nil)
}

type sshTrust struct {
	// Authorized: the target now accepts this host's key.
	Authorized bool `json:"authorized"`
	// Verified: an ssh to the target worked (it also remembered the target's host key).
	Verified  bool   `json:"verified"`
	PublicKey string `json:"publicKey"`
	// NeedsPassword: the target is not a cluster member; with root's password there (sent once,
	// kept nowhere) VirtoMir adds the key itself.
	NeedsPassword bool `json:"needsPassword,omitempty"`
	// Reason says why it is not done yet.
	Reason string `json:"reason,omitempty"`
}

// trustSSHTarget makes the replication target accept this host's root key: a cluster member
// is asked over the peer protocol; any other host is signed in to once with root's password.
func (s *Server) trustSSHTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target   string `json:"target"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	user, host := splitTarget(body.Target)
	if host == "" || (user != "" && user != "root") {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the target is a host name or address (or root@host)"})
		return
	}
	name, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	pub, err := s.hostSetup.PublicKey(ctx, "virtomir@"+name)
	if err != nil {
		s.writeError(w, err)
		return
	}
	res := sshTrust{PublicKey: pub}
	// One ssh: it checks the key works and remembers the target's host key.
	verify := func() error {
		_, err := s.hostSetup.Run.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "StrictHostKeyChecking=accept-new", "root@"+host, "true")
		return err
	}
	switch memberID := s.memberAt(host); {
	case memberID != "":
		if err := s.callHost(ctx, identity(r), memberID, http.MethodPost, "/api/v1/host/setup/ssh-authorize", map[string]string{"key": pub}, nil); err != nil {
			s.writeError(w, err)
			return
		}
	case body.Password != "":
		if err := s.hostSetup.CopyKey(ctx, host, body.Password, pub); err != nil {
			s.writeError(w, err)
			return
		}
		s.log.Info("SSH key added on a host outside the cluster", "target", host, "by", identity(r).Name)
	default:
		if verify() == nil { // it already accepts the key
			res.Authorized, res.Verified = true, true
			s.writeJSON(w, http.StatusOK, res)
			return
		}
		res.NeedsPassword = true
		res.Reason = host + " is not a member of this cluster. Enter root's password on " + host + " once: VirtoMir adds this host's key there and does not keep the password."
		s.writeJSON(w, http.StatusOK, res)
		return
	}
	res.Authorized = true
	if err := verify(); err != nil {
		res.Reason = "The key was added on " + host + ", but signing in did not work yet: " + err.Error()
	} else {
		res.Verified = true
	}
	s.writeJSON(w, http.StatusOK, res)
}

// memberAt is the ID of the cluster member reached at host (its address or name), or "".
func (s *Server) memberAt(host string) string {
	if s.cluster == nil {
		return ""
	}
	members, _ := s.cluster.Members()
	for _, m := range members {
		if u, err := url.Parse(m.Address); (err == nil && strings.EqualFold(u.Hostname(), host)) || strings.EqualFold(m.Name, host) {
			return m.ID
		}
	}
	return ""
}

var targetHostRE = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*|[0-9a-fA-F:]{2,39})$`)

// splitTarget splits [user@]host; host is "" when it is not a host name or address.
func splitTarget(t string) (user, host string) {
	t = strings.TrimSpace(t)
	if u, h, ok := strings.Cut(t, "@"); ok {
		user, t = u, h
	}
	t = strings.Trim(t, "[]")
	if !targetHostRE.MatchString(t) {
		return user, ""
	}
	return user, t
}

func keyComment(key string) string {
	if f := strings.Fields(key); len(f) >= 3 {
		return f[2]
	}
	return "(no comment)"
}
