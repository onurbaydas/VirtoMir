// Command check_virtomir is a monitoring plugin for Nagios, Icinga 2, Naemon, Checkmk and the
// other programs that run a check and read its exit code (0 OK, 1 WARNING, 2 CRITICAL,
// 3 UNKNOWN) and its first line ("STATUS - text | perfdata").
//
//	check_virtomir --url https://host:8420 --token vmt_… --host -w 80 -c 95
//	check_virtomir --machine web-01
//	check_virtomir --pools -w 80 -c 90
//	check_virtomir --cluster
//	check_virtomir --alerts
//	check_virtomir --checkmk          # Checkmk special agent: one piggyback host per machine
//
// The API token (Account → API tokens; a viewer's is enough) can also come from
// VIRTOMIR_TOKEN. VirtoMir's own certificate is self-signed: pin it with --fingerprint
// (Host settings shows it) or, on a trusted network, skip the check with --insecure.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Exit codes of the plugin API.
const (
	OK = iota
	Warning
	Critical
	Unknown
)

var names = [...]string{"OK", "WARNING", "CRITICAL", "UNKNOWN"}

// result is one check's outcome.
type result struct {
	code  int
	text  string
	perf  []string
	extra []string // more lines (long output)
}

func (r result) String() string {
	s := names[r.code] + " - " + r.text
	if len(r.perf) > 0 {
		s += " | " + strings.Join(r.perf, " ")
	}
	for _, l := range r.extra {
		s += "\n" + l
	}
	return s
}

func worse(a, b int) int {
	// CRITICAL beats WARNING beats UNKNOWN beats OK, as Nagios ranks them.
	rank := map[int]int{OK: 0, Unknown: 1, Warning: 2, Critical: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func level(v, warn, crit float64) int {
	switch {
	case crit > 0 && v >= crit:
		return Critical
	case warn > 0 && v >= warn:
		return Warning
	}
	return OK
}

// client talks to VirtoMir's API.
type client struct {
	base, token string
	http        *http.Client
}

func (c client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.base, "/")+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	return json.Unmarshal(body, v)
}

type snapshot struct {
	Connected bool `json:"connected"`
	Host      struct {
		CPUPercent  float64 `json:"cpuPercent"`
		MemTotalKiB float64 `json:"memTotalKiB"`
		MemUsedKiB  float64 `json:"memUsedKiB"`
	} `json:"host"`
	Domains []struct {
		UUID       string  `json:"uuid"`
		Name       string  `json:"name"`
		State      string  `json:"state"`
		CPUPercent float64 `json:"cpuPercent"`
		VCPUs      int     `json:"vcpus"`
		MemoryKiB  float64 `json:"memoryKiB"`
	} `json:"domains"`
}

func checkHost(ctx context.Context, c client, warn, crit float64) result {
	var s snapshot
	if err := c.get(ctx, "/api/v1/summary", &s); err != nil {
		return result{code: Unknown, text: err.Error()}
	}
	if !s.Connected {
		return result{code: Critical, text: "VirtoMir does not reach libvirt"}
	}
	mem := 0.0
	if s.Host.MemTotalKiB > 0 {
		mem = 100 * s.Host.MemUsedKiB / s.Host.MemTotalKiB
	}
	code := worse(level(s.Host.CPUPercent, warn, crit), level(mem, warn, crit))
	running := 0
	for _, d := range s.Domains {
		if d.State == "running" {
			running++
		}
	}
	return result{code: code,
		text: fmt.Sprintf("CPU %.0f %%, memory %.0f %%, %d of %d machines running", s.Host.CPUPercent, mem, running, len(s.Domains)),
		perf: []string{perf("cpu", s.Host.CPUPercent, "%", warn, crit, 0, 100), perf("memory", mem, "%", warn, crit, 0, 100),
			fmt.Sprintf("machines_running=%d;;;0;%d", running, len(s.Domains))}}
}

func checkMachine(ctx context.Context, c client, name string, warn, crit float64) result {
	var s snapshot
	if err := c.get(ctx, "/api/v1/summary", &s); err != nil {
		return result{code: Unknown, text: err.Error()}
	}
	for _, d := range s.Domains {
		if d.Name != name && d.UUID != name {
			continue
		}
		if d.State != "running" {
			return result{code: Critical, text: fmt.Sprintf("%s is %s", d.Name, d.State)}
		}
		return result{code: level(d.CPUPercent, warn, crit), text: fmt.Sprintf("%s is running, CPU %.0f %% of %d vCPUs", d.Name, d.CPUPercent, d.VCPUs),
			perf: []string{perf("cpu", d.CPUPercent, "%", warn, crit, 0, 100), fmt.Sprintf("memory=%.0fB", d.MemoryKiB*1024)}}
	}
	return result{code: Unknown, text: fmt.Sprintf("no machine %q on this host", name)}
}

func checkPools(ctx context.Context, c client, warn, crit float64) result {
	var r struct {
		Pools []struct {
			Name            string  `json:"name"`
			Active          bool    `json:"active"`
			Type            string  `json:"type"`
			CapacityBytes   float64 `json:"capacityBytes"`
			AllocationBytes float64 `json:"allocationBytes"`
		} `json:"pools"`
	}
	if err := c.get(ctx, "/api/v1/storage/pools", &r); err != nil {
		return result{code: Unknown, text: err.Error()}
	}
	res := result{code: OK}
	var bad []string
	for _, p := range r.Pools {
		if !p.Active {
			res.code = worse(res.code, Warning)
			bad = append(bad, p.Name+" inactive")
			continue
		}
		if p.CapacityBytes <= 0 || p.Type == "iscsi" || p.Type == "scsi" || p.Type == "mpath" {
			continue // LUN pools are always "full"
		}
		used := 100 * p.AllocationBytes / p.CapacityBytes
		if l := level(used, warn, crit); l != OK {
			res.code = worse(res.code, l)
			bad = append(bad, fmt.Sprintf("%s %.0f %% full", p.Name, used))
		}
		res.perf = append(res.perf, perf(p.Name, used, "%", warn, crit, 0, 100))
		res.extra = append(res.extra, fmt.Sprintf("%s: %.0f %% of %.1f GiB used", p.Name, used, p.CapacityBytes/(1<<30)))
	}
	if len(bad) > 0 {
		res.text = strings.Join(bad, ", ")
	} else {
		res.text = fmt.Sprintf("%d pools fine", len(r.Pools))
	}
	return res
}

func checkCluster(ctx context.Context, c client) result {
	var cl struct {
		Enabled *bool `json:"enabled"`
		Members []struct {
			Name      string `json:"name"`
			Reachable bool   `json:"reachable"`
			Error     string `json:"error"`
		} `json:"members"`
	}
	if err := c.get(ctx, "/api/v1/cluster", &cl); err != nil {
		return result{code: Unknown, text: err.Error()}
	}
	if len(cl.Members) == 0 {
		return result{code: OK, text: "not in a cluster"}
	}
	res := result{code: OK}
	var down []string
	for _, m := range cl.Members {
		if !m.Reachable {
			down = append(down, m.Name)
		}
	}
	if len(down) > 0 {
		res.code = Warning
		if 2*len(down) >= len(cl.Members) {
			res.code = Critical
		}
	}
	var ha struct {
		Clustered bool `json:"clustered"`
		Quorum    struct {
			Quorate    bool `json:"quorate"`
			Votes      int  `json:"votes"`
			TotalVotes int  `json:"totalVotes"`
			Required   int  `json:"required"`
		} `json:"quorum"`
	}
	quorum := ""
	if err := c.get(ctx, "/api/v1/ha/status", &ha); err == nil && ha.Clustered && ha.Quorum.TotalVotes > 0 {
		quorum = fmt.Sprintf(", HA quorum %d of %d votes (%d needed)", ha.Quorum.Votes, ha.Quorum.TotalVotes, ha.Quorum.Required)
		if !ha.Quorum.Quorate {
			res.code = Critical
			quorum = ", HA has no quorum" + strings.TrimPrefix(quorum, ", HA quorum")
		}
		res.perf = append(res.perf, fmt.Sprintf("votes=%d;;%d;0;%d", ha.Quorum.Votes, ha.Quorum.Required, ha.Quorum.TotalVotes))
	}
	up := len(cl.Members) - len(down)
	res.text = fmt.Sprintf("%d of %d members up%s", up, len(cl.Members), quorum)
	if len(down) > 0 {
		res.text += "; unreachable: " + strings.Join(down, ", ")
	}
	res.perf = append([]string{fmt.Sprintf("members_up=%d;;;0;%d", up, len(cl.Members))}, res.perf...)
	return res
}

func checkAlerts(ctx context.Context, c client) result {
	var r struct {
		Alerts []struct {
			Severity string     `json:"severity"`
			Subject  string     `json:"subject"`
			Message  string     `json:"message"`
			Resolved *time.Time `json:"resolved"`
			AckedBy  string     `json:"ackedBy"`
		} `json:"alerts"`
	}
	if err := c.get(ctx, "/api/v1/alerts", &r); err != nil {
		return result{code: Unknown, text: err.Error()}
	}
	res := result{code: OK}
	crit, warn := 0, 0
	for _, a := range r.Alerts {
		if a.Resolved != nil {
			continue
		}
		res.extra = append(res.extra, fmt.Sprintf("%s: %s — %s", a.Severity, a.Subject, a.Message))
		if a.AckedBy != "" {
			continue // acknowledged: known, not paging again
		}
		if a.Severity == "critical" {
			crit++
		} else {
			warn++
		}
	}
	switch {
	case crit > 0:
		res.code = Critical
	case warn > 0:
		res.code = Warning
	}
	res.text = fmt.Sprintf("%d critical, %d warning alerts open", crit, warn)
	res.perf = []string{fmt.Sprintf("critical=%d;;1;0", crit), fmt.Sprintf("warning=%d;1;;0", warn)}
	return res
}

// checkmk writes a Checkmk special agent's output: the host's own local checks, and each
// machine as a piggyback host with its state and CPU.
func checkmk(ctx context.Context, c client, w io.Writer) error {
	var s snapshot
	if err := c.get(ctx, "/api/v1/summary", &s); err != nil {
		return err
	}
	fmt.Fprintln(w, "<<<local:sep(0)>>>")
	for _, r := range []struct {
		name string
		res  result
	}{{"VirtoMir host", checkHost(ctx, c, 0, 0)}, {"VirtoMir pools", checkPools(ctx, c, 85, 95)}, {"VirtoMir alerts", checkAlerts(ctx, c)}} {
		perfs := "-"
		if len(r.res.perf) > 0 {
			perfs = strings.Join(r.res.perf, "|")
		}
		fmt.Fprintf(w, "%d \"%s\" %s %s\n", r.res.code, r.name, perfs, r.res.text)
	}
	doms := s.Domains
	sort.Slice(doms, func(i, j int) bool { return doms[i].Name < doms[j].Name })
	for _, d := range doms {
		code := OK
		if d.State != "running" {
			code = Warning
		}
		fmt.Fprintf(w, "<<<<%s>>>>\n<<<local:sep(0)>>>\n%d \"VirtoMir machine\" cpu=%.1f;;;0;100|vcpus=%d %s, %d vCPUs, CPU %.0f %%\n<<<<>>>>\n",
			d.Name, code, d.CPUPercent, d.VCPUs, d.State, d.VCPUs, d.CPUPercent)
	}
	return nil
}

func perf(label string, v float64, unit string, warn, crit, min, max float64) string {
	f := func(x float64) string {
		if x <= 0 {
			return ""
		}
		return fmt.Sprintf("%g", x)
	}
	return fmt.Sprintf("'%s'=%.1f%s;%s;%s;%g;%g", label, v, unit, f(warn), f(crit), min, max)
}

// pinnedTrust trusts exactly the certificate with this SHA-256 fingerprint (hex, colons allowed).
func pinnedTrust(fingerprint string) (*tls.Config, error) {
	want, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(fingerprint), ":", ""))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("--fingerprint is the certificate's SHA-256 in hex")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pin below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return fmt.Errorf("certificate fingerprint %x is not the pinned one", got)
			}
			return nil
		},
	}, nil
}

func run(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("check_virtomir", flag.ContinueOnError)
	fs.SetOutput(stdout)
	url := fs.String("url", "https://localhost:8420", "VirtoMir address")
	token := fs.String("token", os.Getenv("VIRTOMIR_TOKEN"), "API token (default $VIRTOMIR_TOKEN)")
	insecure := fs.Bool("insecure", false, "do not check the TLS certificate")
	fingerprint := fs.String("fingerprint", "", "trust only the certificate with this SHA-256 (hex)")
	timeout := fs.Duration("timeout", 15*time.Second, "give up after this long (UNKNOWN)")
	host := fs.Bool("host", false, "check libvirt, host CPU and memory")
	machine := fs.String("machine", "", "check one machine (name or UUID): running, CPU")
	pools := fs.Bool("pools", false, "check how full the storage pools are")
	clusterMode := fs.Bool("cluster", false, "check the cluster members and HA quorum")
	alertsMode := fs.Bool("alerts", false, "check VirtoMir's open alerts")
	cmk := fs.Bool("checkmk", false, "write a Checkmk special agent's output (piggyback hosts)")
	warn := fs.Float64("w", 0, "warning threshold in % (host, machine, pools)")
	crit := fs.Float64("c", 0, "critical threshold in %")
	if err := fs.Parse(args); err != nil {
		return Unknown
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	switch {
	case *fingerprint != "":
		cfg, err := pinnedTrust(*fingerprint)
		if err != nil {
			fmt.Fprintln(stdout, "UNKNOWN - "+err.Error())
			return Unknown
		}
		tr.TLSClientConfig = cfg
	case *insecure:
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // asked for with --insecure
	}
	c := client{base: *url, token: *token, http: &http.Client{Transport: tr, Timeout: *timeout}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *cmk {
		if err := checkmk(ctx, c, stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	var r result
	switch {
	case *host:
		r = checkHost(ctx, c, *warn, *crit)
	case *machine != "":
		r = checkMachine(ctx, c, *machine, *warn, *crit)
	case *pools:
		w, cr := *warn, *crit
		if w == 0 && cr == 0 {
			w, cr = 85, 95
		}
		r = checkPools(ctx, c, w, cr)
	case *clusterMode:
		r = checkCluster(ctx, c)
	case *alertsMode:
		r = checkAlerts(ctx, c)
	default:
		fmt.Fprintln(stdout, "UNKNOWN - choose a check: --host, --machine NAME, --pools, --cluster, --alerts or --checkmk")
		return Unknown
	}
	fmt.Fprintln(stdout, "VIRTOMIR "+r.String())
	return r.code
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
