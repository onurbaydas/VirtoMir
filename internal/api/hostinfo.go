package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Host facts libvirt does not have: chassis and sensors, GPUs with their load, and the
// network interfaces with their bond/bridge/VLAN wiring. Plus a packet capture on any of
// those interfaces, which is admin-only (requiredRole) because it shows other people's traffic.
func (s *Server) hostInfoRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/host/chassis", s.hostChassis)
	mux.HandleFunc("GET /api/v1/host/gpus", s.hostGPUs)
	mux.HandleFunc("GET /api/v1/host/interfaces", s.hostInterfaces)
	mux.HandleFunc("GET /api/v1/host/capture", s.hostCapture)
	mux.HandleFunc("GET /api/v1/host/topology", s.hostTopology)
}

// Placer reports where running machines' vCPUs sit (for the topology view).
type Placer interface {
	Placements() ([]virt.Placement, error)
}

// hostTopology is the host's NUMA nodes, sockets, cores and threads with their load, and the
// running machines' vCPUs on top: the picture to pin by (P5).
func (s *Server) hostTopology(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostReader(w)
	if !ok {
		return
	}
	out := struct {
		hostinfo.Topology
		Machines []virt.Placement `json:"machines"`
	}{Topology: h.Topology(r.Context()), Machines: []virt.Placement{}}
	if p, ok := s.backend.(Placer); ok {
		if m, err := p.Placements(); err == nil {
			out.Machines = m
		}
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) hostReader(w http.ResponseWriter) (*hostinfo.Reader, bool) {
	if s.hostinfo == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "host details are not available on this host"})
		return nil, false
	}
	return s.hostinfo, true
}

// ChassisView is the chassis plus how to reach the host's management controller (BMC), if
// one is configured for fencing (HA): that is the only BMC address VirtoMir knows for sure.
type ChassisView struct {
	hostinfo.Chassis
	BMC *BMCLink `json:"bmc,omitempty"`
}

type BMCLink struct {
	Driver string `json:"driver"` // ipmi, redfish
	URL    string `json:"url"`
}

func (s *Server) hostChassis(w http.ResponseWriter, _ *http.Request) {
	h, ok := s.hostReader(w)
	if !ok {
		return
	}
	v := ChassisView{Chassis: h.Chassis()}
	if s.ha != nil && s.cluster != nil {
		if a, ok := s.ha.Agent(s.cluster.SelfID()); ok && a.Endpoint != "" && (a.Driver == "ipmi" || a.Driver == "redfish") {
			u := a.Endpoint
			if !strings.Contains(u, "://") {
				u = "https://" + u
			}
			v.BMC = &BMCLink{Driver: a.Driver, URL: u}
		}
	}
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) hostGPUs(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostReader(w)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"gpus": h.GPUs(r.Context())})
}

func (s *Server) hostInterfaces(w http.ResponseWriter, _ *http.Request) {
	h, ok := s.hostReader(w)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"interfaces": h.Interfaces(nil)})
}

// Capture limits: a capture is a diagnostic look, not a recorder.
const (
	captureMaxSeconds = 120
	captureMaxPackets = 20000
	captureMaxFilter  = 512
)

// captureArgs checks the request and builds tcpdump's arguments. The filter is passed as
// one argument after "--", never through a shell, so it can only be a (possibly invalid)
// BPF expression.
func captureArgs(h *hostinfo.Reader, iface, filter, count string, pcap bool) ([]string, error) {
	if !h.ValidInterface(iface) {
		return nil, fmt.Errorf("no such interface %q", iface)
	}
	if len(filter) > captureMaxFilter {
		return nil, fmt.Errorf("the filter is longer than %d characters", captureMaxFilter)
	}
	if strings.ContainsAny(filter, "\x00\n\r") {
		return nil, fmt.Errorf("the filter must be one line")
	}
	n := 1000
	if count != "" {
		v, err := strconv.Atoi(count)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("count must be a positive number")
		}
		n = min(v, captureMaxPackets)
	}
	args := []string{"-i", iface, "-nn", "-c", strconv.Itoa(n)}
	if pcap {
		args = append(args, "-U", "-w", "-")
	} else {
		args = append(args, "-l", "-tttt")
	}
	if f := strings.TrimSpace(filter); f != "" {
		args = append(args, "--", f)
	}
	return args, nil
}

// hostCapture runs tcpdump on a host interface:
//
//	?iface=vnet3&filter=tcp port 22&count=500&seconds=30            → text lines as SSE ("packet" events)
//	?iface=vnet3&filter=…&count=…&seconds=…&format=pcap              → a .pcap file download
//
// It stops at count packets, after seconds, or when the client goes away.
func (s *Server) hostCapture(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostReader(w)
	if !ok {
		return
	}
	if _, err := exec.LookPath("tcpdump"); err != nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "tcpdump is not installed on this host (apt install tcpdump)"})
		return
	}
	q := r.URL.Query()
	pcap := q.Get("format") == "pcap"
	args, err := captureArgs(h, q.Get("iface"), q.Get("filter"), q.Get("count"), pcap)
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	secs := 30
	if v, err := strconv.Atoi(q.Get("seconds")); err == nil && v > 0 {
		secs = min(v, captureMaxSeconds)
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(secs)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tcpdump", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.writeError(w, err)
		return
	}
	if err := cmd.Start(); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tcpdump: " + err.Error()})
		return
	}
	s.log.Info("packet capture", "user", identity(r).Name, "iface", q.Get("iface"), "filter", q.Get("filter"))
	rc := http.NewResponseController(w)

	if pcap {
		// tcpdump checks the filter before it writes anything: wait for the first bytes so a
		// bad filter is still an error answer, not an empty file.
		br := bufio.NewReader(stdout)
		if _, err := br.Peek(24); err != nil { // pcap global header
			_ = cmd.Wait()
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": tcpdumpError(stderr.String())})
			return
		}
		w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
			fmt.Sprintf("capture-%s-%s.pcap", q.Get("iface"), time.Now().Format("20060102-150405"))))
		_, _ = br.WriteTo(w)
		_ = cmd.Wait()
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		if _, err := fmt.Fprintf(w, "event: packet\ndata: %s\n\n", sc.Text()); err != nil {
			break
		}
		_ = rc.Flush()
	}
	_ = cmd.Wait()
	end := "finished"
	if msg := tcpdumpError(stderr.String()); msg != "" && ctx.Err() == nil {
		end = msg
	}
	_, _ = fmt.Fprintf(w, "event: end\ndata: %s\n\n", end)
	_ = rc.Flush()
}

// tcpdumpError picks the useful line out of tcpdump's stderr (it also prints banners there).
func tcpdumpError(stderr string) string {
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "tcpdump: ") && !strings.Contains(l, "listening on") && !strings.Contains(l, "verbose output suppressed") {
			return l
		}
	}
	return ""
}
