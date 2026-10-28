package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/carve"
	"github.com/onurbaydas/virtomir/internal/storconn"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Network and SAN storage found and connected by VirtoMir itself (storconn).

func (s *Server) storconnRoutes(mux *routeMux) {
	mux.HandleFunc("POST /api/v1/storage/discover", s.discoverStorage)
	mux.HandleFunc("POST /api/v1/storage/connect", s.connectStorage)
}

// discoverRequest: empty scans this host's networks; Server (with credentials) asks one
// server again, e.g. for the shares it lists only after a sign-in.
type discoverRequest struct {
	Server string `json:"server,omitempty"`
	storconn.Credentials
}

var (
	serverRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
	shareRE  = regexp.MustCompile(`^[^/\\\x00-\x1f-][^/\\\x00-\x1f]*$`)
	exportRE = regexp.MustCompile(`^/[^\x00-\x1f]*$`)
	iqnRE    = regexp.MustCompile(`^(iqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9.-]+(:[^\s]+)?|eui\.[0-9A-Fa-f]{16})$`)
	userRE   = regexp.MustCompile(`^[^\x00-\x1f,=]*$`)
)

func (s *Server) discoverStorage(w http.ResponseWriter, r *http.Request) {
	var req discoverRequest
	if r.ContentLength != 0 && !s.decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute) // may install tools first
	defer cancel()
	var servers map[string][]string
	creds := map[string]storconn.Credentials{}
	if req.Server != "" {
		if !serverRE.MatchString(req.Server) || !userRE.MatchString(req.Username) || strings.ContainsAny(req.Password, "\n\r") {
			s.writeError(w, fmt.Errorf("%w: %q is not a server address", storconn.ErrInvalid, req.Server))
			return
		}
		servers = storconn.Probe(ctx, nil, req.Server)
		creds[req.Server] = req.Credentials
	} else {
		servers = storconn.Probe(ctx, storconn.Networks())
	}
	found := storconn.Discover(ctx, s.cephRunner, servers, creds)
	s.markConnected(ctx, found)
	s.writeJSON(w, http.StatusOK, map[string]any{"found": found})
}

// markConnected names the pool each found storage already is.
func (s *Server) markConnected(ctx context.Context, found []storconn.Found) {
	pools, err := s.backend.ListPools()
	if err != nil {
		return
	}
	for i, f := range found {
		for _, p := range pools {
			switch {
			case f.Kind == storconn.KindNFS && p.Type == "netfs" && p.Source == f.Server+":"+f.Name,
				f.Kind == storconn.KindISCSI && p.Type == "iscsi" && p.Source == f.Name,
				f.Kind == storconn.KindSMB && p.Type == "dir" && f.Name != "" &&
					storconn.SMBShare(ctx, s.cephRunner, p.Name) == "//"+f.Server+"/"+f.Name:
				found[i].Pool = p.Name
			}
		}
	}
}

type connectRequest struct {
	Kind   string `json:"kind"`
	Server string `json:"server"`
	Host   string `json:"host,omitempty"` // its DNS name, for the pool's name
	Name   string `json:"name"`           // target IQN, export path or share
	storconn.Credentials
	// Everywhere: on every member of the cluster (the default in the UI).
	Everywhere bool `json:"everywhere,omitempty"`
	// Pool: the name the asking member chose, so every member uses the same one.
	Pool string `json:"pool,omitempty"`
}

func (req connectRequest) check() error {
	ok := serverRE.MatchString(req.Server) && userRE.MatchString(req.Username) && !strings.ContainsAny(req.Password, "\n\r")
	switch req.Kind {
	case storconn.KindNFS:
		ok = ok && exportRE.MatchString(req.Name)
	case storconn.KindSMB:
		ok = ok && shareRE.MatchString(req.Name)
	case storconn.KindISCSI:
		ok = ok && iqnRE.MatchString(req.Name)
	default:
		ok = false
	}
	if !ok {
		return fmt.Errorf("%w: cannot connect %s %q on %q", storconn.ErrInvalid, req.Kind, req.Name, req.Server)
	}
	return nil
}

// connectStorage makes found storage a pool, here or on every member: tools installed,
// SELinux allowed, mounted or logged in on every boot. Synchronous: members are waited for.
func (s *Server) connectStorage(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := req.check(); err != nil {
		s.writeError(w, err)
		return
	}
	if req.Pool == "" {
		pools, err := s.backend.ListPools()
		if err != nil {
			s.writeError(w, err)
			return
		}
		req.Pool = storconn.PoolName(storconn.Found{Kind: req.Kind, Server: req.Server, Host: req.Host, Name: req.Name},
			func(n string) bool {
				for _, p := range pools {
					if p.Name == n {
						return true
					}
				}
				return false
			})
	}
	local := func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
		defer cancel()
		return s.connectHere(ctx, req)
	}
	if req.Everywhere && s.cluster != nil && s.cluster.Clustered() {
		peer := req
		peer.Everywhere = false
		s.writeJSON(w, http.StatusOK, map[string]any{"pool": req.Pool, "results": s.everyHost(r, 5*time.Minute, local, "/api/v1/storage/connect", peer)})
		return
	}
	if err := local(); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pool": req.Pool})
}

func (s *Server) connectHere(ctx context.Context, req connectRequest) error {
	if err := storconn.Prepare(ctx, s.cephRunner, req.Kind); err != nil {
		return err
	}
	switch req.Kind {
	case storconn.KindNFS:
		return s.backend.CreatePool(virt.PoolSpec{Name: req.Pool, Type: "netfs", Host: req.Server, Export: req.Name,
			Path: filepath.Join(storconn.Base, req.Pool)})
	case storconn.KindISCSI:
		return storconn.AuthFailed(s.backend.CreatePool(virt.PoolSpec{Name: req.Pool, Type: "iscsi", Host: req.Server, Target: req.Name,
			ChapUser: req.Username, ChapPassword: req.Password}))
	}
	dir, err := storconn.MountSMB(ctx, s.cephRunner, req.Pool, req.Server, req.Name, req.Credentials)
	if err != nil {
		return err
	}
	if err := s.backend.CreatePool(virt.PoolSpec{Name: req.Pool, Path: dir}); err != nil {
		_ = storconn.UnmountSMB(ctx, s.cephRunner, req.Pool)
		return err
	}
	return nil
}

// poolOrigin says how VirtoMir made a pool below /srv/virtomir: "carved" from free space or
// a mounted "smb" share ("" for any other pool).
func (s *Server) poolOrigin(ctx context.Context, p virt.Pool) string {
	if p.Type != "dir" || p.Path != filepath.Join(carve.Base, p.Name) {
		return ""
	}
	if carve.Carved(ctx, s.cephRunner, p.Name) {
		return "carved"
	}
	if storconn.SMBShare(ctx, s.cephRunner, p.Name) != "" {
		return "smb"
	}
	return ""
}
