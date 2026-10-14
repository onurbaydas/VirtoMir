package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/onurbaydas/virtomir/internal/live"
)

// Prometheus / OpenMetrics text exposition of the newest live snapshot, the storage pools and
// the open alerts. Scrape it with an API token (viewer role is enough):
//
//	scrape_configs:
//	  - job_name: virtomir
//	    scheme: https
//	    metrics_path: /api/v1/metrics
//	    authorization: { credentials: vmt_… }
//
// Rates are the per-second values VirtoMir already computes once a second (gauges), so no
// rate() is needed in queries.

func (s *Server) metricsRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/metrics", s.metrics)
}

// metricWriter collects samples per metric family: the text format wants all samples of one
// family together, while they are produced machine by machine.
type metricWriter struct {
	order    []string
	help     map[string]string
	families map[string]*strings.Builder
}

func newMetricWriter() *metricWriter {
	return &metricWriter{help: map[string]string{}, families: map[string]*strings.Builder{}}
}

func (m *metricWriter) gauge(name, help string, value float64, labels ...string) {
	b, ok := m.families[name]
	if !ok {
		b = &strings.Builder{}
		m.families[name], m.help[name] = b, help
		m.order = append(m.order, name)
	}
	fmt.Fprintf(b, "%s%s %g\n", name, labelText(labels), value)
}

func (m *metricWriter) writeTo(w io.Writer) {
	for _, name := range m.order {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s", name, m.help[name], name, m.families[name].String())
	}
}

// labelText renders key/value pairs as {k="v",…} with Prometheus escaping.
func labelText(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		fmt.Fprintf(&b, `%s="%s"`, kv[i], v)
	}
	b.WriteByte('}')
	return b.String()
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	m := newMetricWriter()
	defer func() {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.writeTo(w)
	}()
	m.gauge("virtomir_up", "VirtoMir answers.", 1, "version", s.version, "host", s.hostName)

	var snap live.Snapshot
	backlog, _, unsubscribe := s.live.Subscribe()
	unsubscribe()
	if len(backlog) > 0 && json.Unmarshal(backlog[len(backlog)-1], &snap) == nil {
		writeSnapshotMetrics(m, snap)
	}

	if pools, err := s.backend.ListPools(); err == nil {
		sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
		for _, p := range pools {
			l := []string{"pool", p.Name, "type", p.Type}
			m.gauge("virtomir_pool_active", "Storage pool is active.", boolValue(p.Active), l...)
			m.gauge("virtomir_pool_capacity_bytes", "Storage pool capacity.", float64(p.CapacityBytes), l...)
			m.gauge("virtomir_pool_allocation_bytes", "Storage pool space in use.", float64(p.AllocationBytes), l...)
			m.gauge("virtomir_pool_available_bytes", "Storage pool free space.", float64(p.AvailableBytes), l...)
		}
	}

	if s.alerts != nil {
		open := map[string]int{"warning": 0, "critical": 0}
		for _, a := range s.alerts.List() {
			if a.Resolved == nil {
				open[string(a.Severity)]++
			}
		}
		for _, sev := range []string{"critical", "warning"} {
			m.gauge("virtomir_alerts_open", "Alerts that have not resolved yet.", float64(open[sev]), "severity", sev)
		}
	}
}

func writeSnapshotMetrics(m *metricWriter, snap live.Snapshot) {
	m.gauge("virtomir_libvirt_connected", "VirtoMir reaches libvirt.", boolValue(snap.Connected))
	m.gauge("virtomir_sample_timestamp_seconds", "Time of the newest live sample.", float64(snap.Time.UnixMilli())/1000)
	if !snap.Connected {
		return
	}
	h := snap.Host
	m.gauge("virtomir_host_cpu_percent", "Host CPU in use (all CPUs = 100).", h.CPUPercent)
	m.gauge("virtomir_host_memory_total_bytes", "Host memory.", float64(h.MemTotalKiB)*1024)
	m.gauge("virtomir_host_memory_used_bytes", "Host memory in use.", float64(h.MemUsedKiB)*1024)
	if h.IO != nil {
		m.gauge("virtomir_host_disk_read_bytes_per_second", "Physical disk reads.", h.IO.DiskReadBps)
		m.gauge("virtomir_host_disk_write_bytes_per_second", "Physical disk writes.", h.IO.DiskWriteBps)
		m.gauge("virtomir_host_network_receive_bytes_per_second", "Physical network receive.", h.IO.NetRxBps)
		m.gauge("virtomir_host_network_transmit_bytes_per_second", "Physical network transmit.", h.IO.NetTxBps)
	}

	doms := append([]live.DomainLive(nil), snap.Domains...)
	sort.Slice(doms, func(i, j int) bool { return doms[i].Name < doms[j].Name })
	for _, d := range doms {
		l := []string{"uuid", d.UUID, "name", d.Name}
		m.gauge("virtomir_domain_info", "One per machine; the state is a label.", 1, "uuid", d.UUID, "name", d.Name, "state", string(d.State))
		m.gauge("virtomir_domain_running", "Machine is running.", boolValue(d.State == "running"), l...)
		m.gauge("virtomir_domain_autostart", "Machine starts with the host.", boolValue(d.Autostart), l...)
		m.gauge("virtomir_domain_vcpus", "vCPUs.", float64(d.VCPUs), l...)
		m.gauge("virtomir_domain_memory_bytes", "Current memory.", float64(d.MemoryKiB)*1024, l...)
		m.gauge("virtomir_domain_cpu_percent", "Share of the machine's own vCPUs in use (all = 100).", d.CPUPercent, l...)
		if d.HostRSSKiB > 0 {
			m.gauge("virtomir_domain_host_rss_bytes", "Host memory the QEMU process uses.", float64(d.HostRSSKiB)*1024, l...)
		}
		if d.GuestMemTotalKiB > 0 {
			m.gauge("virtomir_domain_guest_memory_used_bytes", "Memory in use as the guest reports it.", float64(d.GuestMemUsedKiB)*1024, l...)
			m.gauge("virtomir_domain_guest_memory_total_bytes", "Memory the guest sees.", float64(d.GuestMemTotalKiB)*1024, l...)
		}
		for i, p := range d.VCPUPercent {
			m.gauge("virtomir_domain_vcpu_percent", "Load of one vCPU (100 = busy).", p, append(l, "vcpu", fmt.Sprint(i))...)
		}
		for _, dk := range d.Disks {
			dl := append(append([]string(nil), l...), "device", dk.Name)
			m.gauge("virtomir_domain_disk_read_bytes_per_second", "Disk reads.", dk.ReadBps, dl...)
			m.gauge("virtomir_domain_disk_write_bytes_per_second", "Disk writes.", dk.WriteBps, dl...)
			m.gauge("virtomir_domain_disk_read_iops", "Disk read operations per second.", dk.ReadIops, dl...)
			m.gauge("virtomir_domain_disk_write_iops", "Disk write operations per second.", dk.WriteIops, dl...)
			if dk.ReadLatencyMs > 0 {
				m.gauge("virtomir_domain_disk_read_latency_seconds", "Average read latency.", dk.ReadLatencyMs/1000, dl...)
			}
			if dk.WriteLatencyMs > 0 {
				m.gauge("virtomir_domain_disk_write_latency_seconds", "Average write latency.", dk.WriteLatencyMs/1000, dl...)
			}
		}
		for _, n := range d.Nets {
			nl := append(append([]string(nil), l...), "interface", n.Name)
			m.gauge("virtomir_domain_network_receive_bytes_per_second", "Network receive.", n.RxBps, nl...)
			m.gauge("virtomir_domain_network_transmit_bytes_per_second", "Network transmit.", n.TxBps, nl...)
			m.gauge("virtomir_domain_network_problems_per_second", "Dropped and errored packets.", n.ProblemsPs, nl...)
		}
	}
}
