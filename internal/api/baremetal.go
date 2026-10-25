package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/baremetal"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/ha"
	"github.com/onurbaydas/virtomir/internal/signature"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Bare-metal onboarding (D17, ADR-0077): the hosts this member installs over the network,
// and the plain HTTP server their network boot fetches its files from.

func (s *Server) baremetalRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/baremetal", s.getBaremetal)
	mux.HandleFunc("PUT /api/v1/baremetal/settings", s.setBaremetalSettings)
	mux.HandleFunc("POST /api/v1/baremetal/installer", s.fetchInstaller)
	mux.HandleFunc("POST /api/v1/baremetal/hosts", s.addBaremetalHost)
	mux.HandleFunc("PUT /api/v1/baremetal/hosts/{id}", s.updateBaremetalHost)
	mux.HandleFunc("DELETE /api/v1/baremetal/hosts/{id}", s.deleteBaremetalHost)
	mux.HandleFunc("POST /api/v1/baremetal/hosts/{id}/install", s.installBaremetalHost)
	mux.HandleFunc("POST /api/v1/baremetal/hosts/{id}/cancel", s.cancelBaremetalHost)
	mux.HandleFunc("POST /api/v1/baremetal/hosts/{id}/power", s.powerBaremetalHost)
}

// netboot is the boot file server while it runs.
type netboot struct {
	mu   sync.Mutex
	srv  *http.Server
	addr string
	err  string
}

func (s *Server) noBaremetal(w http.ResponseWriter) bool {
	if s.baremetal == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "bare-metal onboarding is not available on this host"})
		return true
	}
	return false
}

func (s *Server) netbootDir() string { return filepath.Join(s.stateDir, "netboot") }

// RunBaremetal is called once before the API serves: it starts the boot file server.
func (s *Server) RunBaremetal(ctx context.Context) {
	if s.runCtx == nil {
		s.runCtx = ctx
	}
	if s.baremetal != nil {
		s.applyNetboot(ctx)
	}
}

// applyNetboot (re)starts the boot file server and proxy DHCP as the settings say.
func (s *Server) applyNetboot(ctx context.Context) {
	set := s.baremetal.Settings()
	addr := ""
	if set.Enabled {
		addr = net.JoinHostPort(set.Address, fmt.Sprint(set.Port))
	}
	s.netboot.mu.Lock()
	if s.netboot.addr != addr || (addr != "" && s.netboot.srv == nil) {
		if s.netboot.srv != nil {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = s.netboot.srv.Shutdown(c)
			cancel()
			s.netboot.srv = nil
		}
		s.netboot.addr, s.netboot.err = addr, ""
		if addr != "" {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				s.netboot.err = err.Error()
				s.log.Warn("network boot", "err", err)
			} else {
				srv := &http.Server{Handler: s.bootHandler(), ReadHeaderTimeout: 10 * time.Second}
				s.netboot.srv = srv
				go func() {
					if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
						s.log.Warn("network boot", "err", err)
					}
				}()
				s.log.Info("network boot files served", "addr", addr)
			}
		}
	}
	s.netboot.mu.Unlock()
	if err := s.applyProxy(ctx); err != nil {
		s.log.Warn("proxy DHCP", "err", err)
	}
}

func (s *Server) bootHandler() http.Handler {
	return &baremetal.Server{Store: s.baremetal, Dir: s.netbootDir(), RepoKey: signature.VirtoMirArmored(), Log: s.log,
		Invite: func(_ context.Context, name string) (string, error) {
			if s.cluster == nil {
				return "", errors.New("this host cannot form a cluster (TLS is off)")
			}
			set := s.baremetal.Settings()
			code := cluster.Code{Address: "https://" + net.JoinHostPort(set.Address, s.listenPort), Fingerprint: s.fingerprint, Secret: s.cluster.Invite()}
			s.log.Info("join code for a new host", "host", name)
			return code.String(), nil
		}}
}

// applyProxy runs dnsmasq for the hosts being installed (ModeProxy).
func (s *Server) applyProxy(ctx context.Context) error {
	set := s.baremetal.Settings()
	var macs []string
	for _, h := range s.baremetal.Hosts() {
		if h.State == baremetal.StateInstalling {
			macs = append(macs, h.MAC)
		}
	}
	network := ""
	if set.Enabled && set.Mode == baremetal.ModeProxy && len(macs) > 0 {
		var err error
		if network, err = ifaceNetwork(set.Interface, set.Address); err != nil {
			return err
		}
	}
	return baremetal.ApplyProxy(ctx, s.bmRunner, set, network, macs, s.netbootDir())
}

// ifaceNetwork is the network address of the prefix iface holds address in.
func ifaceNetwork(iface, address string) (string, error) {
	nic, err := net.InterfaceByName(iface)
	if err != nil {
		return "", fmt.Errorf("%w: no interface %s", baremetal.ErrInvalid, iface)
	}
	addrs, _ := nic.Addrs()
	want, _ := netip.ParseAddr(address)
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == want {
			return p.Masked().Addr().String(), nil
		}
	}
	return "", fmt.Errorf("%w: %s is not an address of %s", baremetal.ErrInvalid, address, iface)
}

func (s *Server) getBaremetal(w http.ResponseWriter, _ *http.Request) {
	if s.baremetal == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "hosts": []baremetal.Host{}})
		return
	}
	hosts := []baremetal.Host{}
	for _, h := range s.baremetal.Hosts() {
		hosts = append(hosts, h.Public())
	}
	s.netboot.mu.Lock()
	listening, nbErr := s.netboot.srv != nil, s.netboot.err
	s.netboot.mu.Unlock()
	_, ipmiErr := exec.LookPath("ipmitool")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "settings": s.baremetal.Settings(), "hosts": hosts,
		"status": map[string]any{
			"installerReady": baremetal.InstallerReady(s.netbootDir()), "listening": listening, "error": nbErr,
			"ipmitool": ipmiErr == nil,
		},
	})
}

func (s *Server) setBaremetalSettings(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	var set baremetal.Settings
	if !s.decode(w, r, &set) {
		return
	}
	if err := s.baremetal.SetSettings(set); err != nil {
		s.writeError(w, err)
		return
	}
	s.applyNetboot(r.Context())
	s.netboot.mu.Lock()
	nbErr := s.netboot.err
	s.netboot.mu.Unlock()
	if nbErr != "" {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "saved, but the boot files are not served: " + nbErr})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

func (s *Server) fetchInstaller(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Minute}
	if err := baremetal.FetchInstaller(ctx, client, s.baremetal.Settings().Mirror, s.netbootDir()); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the installer was not downloaded: " + err.Error()})
		return
	}
	s.done(w, http.StatusNoContent, nil)
}

// hostBody is a host as the API takes it: a password instead of its hash.
type hostBody struct {
	Name     string            `json:"name"`
	MAC      string            `json:"mac"`
	BMC      baremetal.BMC     `json:"bmc"`
	Disk     string            `json:"disk,omitempty"`
	Static   *baremetal.Static `json:"static,omitempty"`
	SSHKeys  []string          `json:"sshKeys,omitempty"`
	Password string            `json:"password,omitempty"`
}

// apply puts the body into h; empty passwords keep the stored ones.
func (b hostBody) apply(h *baremetal.Host) error {
	h.Name, h.MAC, h.Disk, h.Static, h.SSHKeys = b.Name, b.MAC, b.Disk, b.Static, b.SSHKeys
	keep := h.BMC.Password
	h.BMC = b.BMC
	if h.BMC.Password == "" {
		h.BMC.Password = keep
	}
	if b.Password != "" {
		if len(b.Password) < 8 {
			return fmt.Errorf("%w: the password has at least 8 characters", baremetal.ErrInvalid)
		}
		hash, err := virt.HashPassword(b.Password)
		if err != nil {
			return err
		}
		h.PasswordHash = hash
	}
	return nil
}

func (s *Server) addBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	var b hostBody
	if !s.decode(w, r, &b) {
		return
	}
	var h baremetal.Host
	if err := b.apply(&h); err != nil {
		s.writeError(w, err)
		return
	}
	h, err := s.baremetal.Add(h)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, h.Public())
}

func (s *Server) updateBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	var b hostBody
	if !s.decode(w, r, &b) {
		return
	}
	h, err := s.baremetal.Update(r.PathValue("id"), func(h *baremetal.Host) error {
		if h.State == baremetal.StateInstalling || h.State == baremetal.StateJoining {
			return fmt.Errorf("%w: %s is %s; cancel first", baremetal.ErrBusy, h.Name, h.State)
		}
		return b.apply(h)
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, h.Public())
}

func (s *Server) deleteBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	if err := s.baremetal.Delete(r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.applyProxy(r.Context()); err != nil {
		s.log.Warn("proxy DHCP", "err", err)
	}
	s.done(w, http.StatusNoContent, nil)
}

func fenceAgent(b baremetal.BMC) ha.FenceAgent {
	return ha.FenceAgent{Driver: b.Driver, Endpoint: b.Endpoint, Username: b.Username, Password: b.Password, InsecureTLS: b.InsecureTLS}
}

// installBaremetalHost erases and installs the host on its next network boot; with a BMC,
// VirtoMir boots it from the network now.
func (s *Server) installBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	set := s.baremetal.Settings()
	switch {
	case !set.Enabled:
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "turn network boot on first (settings)"})
		return
	case !baremetal.InstallerReady(s.netbootDir()):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "download the Debian installer first"})
		return
	case s.cluster == nil:
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "new hosts join a cluster, and this host cannot form one (TLS is off)"})
		return
	}
	h, err := s.baremetal.Update(r.PathValue("id"), func(h *baremetal.Host) error {
		if h.State == baremetal.StateJoined {
			return fmt.Errorf("%w: %s is a member already; remove it from the cluster before installing it again", baremetal.ErrBusy, h.Name)
		}
		h.Token, h.State, h.Message = newToken(), baremetal.StateInstalling, ""
		h.Events = append(h.Events, baremetal.Event{At: time.Now().UTC(), Message: "installation started by " + identity(r).Name + "; its disk will be erased"})
		return nil
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.applyProxy(r.Context()); err != nil {
		s.baremetal.Log(h.ID, true, "proxy DHCP: "+err.Error())
	}
	if h.BMC.Driver == "" {
		s.baremetal.Log(h.ID, false, "no BMC: boot it from the network yourself")
	} else {
		res, err := s.fencer.Fence(r.Context(), fenceAgent(h.BMC), ha.FencePXE)
		if err != nil {
			s.baremetal.Log(h.ID, true, "the BMC did not boot it from the network: "+err.Error())
		} else {
			s.baremetal.Log(h.ID, false, res.Message)
		}
	}
	h, _ = s.baremetal.Host(h.ID)
	s.done(w, http.StatusAccepted, h.Public())
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Server) cancelBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	h, err := s.baremetal.Update(r.PathValue("id"), func(h *baremetal.Host) error {
		h.Token, h.State, h.Message = "", baremetal.StateRegistered, ""
		h.Events = append(h.Events, baremetal.Event{At: time.Now().UTC(), Message: "installation cancelled by " + identity(r).Name})
		return nil
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.applyProxy(r.Context()); err != nil {
		s.log.Warn("proxy DHCP", "err", err)
	}
	s.done(w, http.StatusOK, h.Public())
}

func (s *Server) powerBaremetalHost(w http.ResponseWriter, r *http.Request) {
	if s.noBaremetal(w) {
		return
	}
	var b struct {
		Action string `json:"action"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	h, ok := s.baremetal.Host(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such host"})
		return
	}
	action := ha.FenceAction(b.Action)
	switch action {
	case ha.FenceStatus, ha.FenceOff, ha.FenceReboot, ha.FencePXE:
	default:
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the action is status, off, reboot or pxe"})
		return
	}
	if h.BMC.Driver == "" {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": h.Name + " has no BMC"})
		return
	}
	res, err := s.fencer.Fence(r.Context(), fenceAgent(h.BMC), action)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if action != ha.FenceStatus {
		s.baremetal.Log(h.ID, false, res.Message+" (by "+identity(r).Name+")")
	}
	s.writeJSON(w, http.StatusOK, res)
}
