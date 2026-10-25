# Contrib: monitoring VirtoMir with Prometheus and Grafana

VirtoMir exports [Prometheus](https://prometheus.io) metrics at
`GET /api/v1/metrics` (host CPU/RAM and I/O, per-machine CPU, memory, disk and
network rates, storage pools, open alerts). This folder gives you everything to
graph and alert on them out of the box:

| File | What it is |
| --- | --- |
| `grafana/virtomir-overview.json` | Host health, totals, top machines, pool fill |
| `grafana/virtomir-machine.json` | One machine (CPU, memory, disks, network) |
| `prometheus/virtomir-rules.yml` | Alert rules: scrape failing, libvirt disconnected, host CPU/RAM high, pool >90 %, machine down |

All VirtoMir metrics are **gauges sampled once a second**: rates are already
per-second and percentages are 0–100 (host CPU: 100 = all CPUs busy, machine
CPU: 100 = all of that machine's vCPUs), so dashboards and rules use the raw
values without `rate()` or rescaling.

## 1. Create an API token

The metrics endpoint needs an API token; the **viewer** role is enough (nothing
here can change machines). In the VirtoMir UI open **Account → API tokens**,
create a token named e.g. `prometheus` with the viewer role, and copy the
`vmt_…` value. On multi-host setups create one token per host, or use a
cluster-level token.

## 2. Scrape it with Prometheus

Add this to `prometheus.yml` (adjust scheme, host and port — the token goes in
`authorization.credentials`, which Prometheus sends as a bearer token):

```yaml
scrape_configs:
  - job_name: virtomir
    scheme: https            # http if you run virtomird without TLS (development only)
    metrics_path: /api/v1/metrics
    authorization:
      credentials: vmt_YOUR_TOKEN_HERE
    static_configs:
      - targets: ["virtomir.example.org:8420"]
```

With self-signed TLS (the default) you also need to trust the certificate, for
example:

```yaml
    tls_config:
      ca_file: /etc/prometheus/virtomir.crt
```

If Prometheus runs on the VirtoMir host itself, scrape `localhost:8420` to
avoid TLS entirely (`scheme: http` is fine for a loopback-only listener).

## 3. Load the alert rules

Copy `prometheus/virtomir-rules.yml` next to your Prometheus config and
reference it under `rule_files`:

```yaml
rule_files:
  - /etc/prometheus/virtomir-rules.yml
```

Validate it at any time with:

```sh
promtool check rules contrib/prometheus/virtomir-rules.yml
```

Two rules deserve a note:

- **VirtomirMachineDown** uses the same condition as VirtoMir's own built-in
  `vm.down` alert: only machines set to autostart fire it, because machines
  without autostart are expected to be off.
- **VirtomirScrapeFailing** covers an unreachable daemon *and* a revoked or
  expired token — both look the same to Prometheus.

## 4. Import the Grafana dashboards

In Grafana: **Dashboards → New → Import**, then either upload the JSON file or
paste its contents. Both dashboards ask for one thing on import: the
**Prometheus datasource** to use — pick the one that scrapes VirtoMir, and every
panel is wired to it.

The machine dashboard has a **Machine** dropdown at the top; it also supports
multi-select and All to compare machines.

## 5. Verify

After a few scrapes (set `scrape_interval: 15s` or so), the following should
work in the Grafana explore view:

```promql
virtomir_up                       # 1 for every scraped VirtoMir
virtomir_host_cpu_percent          # the host panel comes alive
count(virtomir_domain_running == 1)  # how many machines are running
```

If `up{job="virtomir"}` stays 0, check the token first (it is the most common
cause), then scheme/TLS/port.

## Metric quick reference

Run `curl -H "Authorization: Bearer vmt_…" https://host:8420/api/v1/metrics`
for the live list with help texts; the important families:

- Host: `virtomir_host_cpu_percent`, `virtomir_host_memory_total_bytes`,
  `virtomir_host_memory_used_bytes`, `virtomir_host_disk_*_bytes_per_second`,
  `virtomir_host_network_*_bytes_per_second`, `virtomir_libvirt_connected`
- Machines (`uuid`, `name` labels; `device`/`interface`/`vcpu` where applicable):
  `virtomir_domain_running`, `virtomir_domain_cpu_percent`,
  `virtomir_domain_vcpu_percent`, `virtomir_domain_memory_bytes`,
  `virtomir_domain_host_rss_bytes`, `virtomir_domain_guest_memory_*_bytes`,
  `virtomir_domain_disk_*`, `virtomir_domain_network_*`
- Pools (`pool`, `type` labels): `virtomir_pool_active`,
  `virtomir_pool_capacity_bytes`, `virtomir_pool_allocation_bytes`,
  `virtomir_pool_available_bytes`
- Alerts: `virtomir_alerts_open` (`severity` label)
