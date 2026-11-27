package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/virt"
)

type metricsBackend struct {
	fakeBackend
	pools []virt.Pool
}

func (m *metricsBackend) ListPools() ([]virt.Pool, error) { return m.pools, nil }

func TestMetrics(t *testing.T) {
	snap := `{"time":"2026-10-14T10:00:00Z","connected":true,
	  "host":{"cpuPercent":12.5,"memTotalKiB":1024,"memUsedKiB":512},
	  "domains":[
	    {"uuid":"u2","name":"web\"2","state":"running","vcpus":2,"memoryKiB":2048,"cpuPercent":50,
	     "disks":[{"name":"vda","readBps":100,"writeBps":200,"readIops":1,"writeIops":2}]},
	    {"uuid":"u1","name":"db","state":"shutoff","vcpus":1,"memoryKiB":1024,"cpuPercent":0}]}`
	b := &metricsBackend{pools: []virt.Pool{{Name: "images", Type: "dir", Active: true, CapacityBytes: 1000}}}
	l := &fakeLive{backlog: [][]byte{[]byte(snap)}}
	rec := httptest.NewRecorder()
	newTestServer(b, l).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		`virtomir_host_cpu_percent 12.5`,
		`virtomir_host_memory_used_bytes 524288`,
		`virtomir_domain_running{uuid="u1",name="db"} 0`,
		`virtomir_domain_running{uuid="u2",name="web\"2"} 1`,
		`virtomir_domain_disk_write_bytes_per_second{uuid="u2",name="web\"2",device="vda"} 200`,
		`virtomir_pool_capacity_bytes{pool="images",type="dir"} 1000`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	// Every family appears once, with all its samples right after its TYPE line.
	seen := map[string]bool{}
	current := ""
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if name, ok := strings.CutPrefix(line, "# TYPE "); ok {
			current = strings.Fields(name)[0]
			if seen[current] {
				t.Errorf("family %s appears twice", current)
			}
			seen[current] = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.FieldsFunc(line, func(r rune) bool { return r == '{' || r == ' ' })[0]
		if name != current {
			t.Errorf("sample %q outside its family %q", line, current)
		}
	}
}

func TestMetricsSensors(t *testing.T) {
	root := t.TempDir()
	for name, v := range map[string]string{
		"sys/class/hwmon/hwmon0/name":           "coretemp",
		"sys/class/hwmon/hwmon0/temp1_input":    "45000",
		"sys/class/hwmon/hwmon0/temp1_label":    "Package id 0",
		"sys/class/hwmon/hwmon1/name":           "nct6775",
		"sys/class/hwmon/hwmon1/fan1_input":     "1200",
		"sys/class/hwmon/hwmon1/power1_average": "152000000",
		"sys/class/hwmon/hwmon1/in0_input":      "1200",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := hostinfo.New()
	h.Root = root
	s := newTestServer(&metricsBackend{}, &fakeLive{})
	s.hostinfo = h
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE virtomir_host_temperature_celsius gauge",
		`virtomir_host_temperature_celsius{chip="coretemp",label="Package id 0"} 45`,
		`virtomir_host_fan_rpm{chip="nct6775",label="fan1"} 1200`,
		`virtomir_host_power_watts{chip="nct6775",label="power1"} 152`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}

	// Without sensors (a VM) no sensor family appears.
	s.hostinfo = hostinfo.New()
	s.hostinfo.Root = t.TempDir()
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if strings.Contains(rec.Body.String(), "virtomir_host_temperature") {
		t.Error("sensor lines without sensors")
	}
}
