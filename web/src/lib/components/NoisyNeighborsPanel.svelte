<script lang="ts">
  // Who is using this host right now: the machines with the most CPU, disk and network load,
  // from the live samples. When the host itself is busy, these are the ones to move.
  import Activity from '@lucide/svelte/icons/activity'
  import { live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { formatBps } from '../format'

  type Row = { uuid: string; name: string; cpu: number; disk: number; iops: number; lat: number; net: number; drops: number }

  let rows = $derived.by((): Row[] => {
    void live.tick
    const cpus = live.host?.cpus || 1
    return (live.snapshot?.domains ?? [])
      .filter((d) => d.state === 'running')
      .map((d) => {
        const disks = d.disks ?? []
        const nets = d.nets ?? []
        return {
          uuid: d.uuid,
          name: d.name,
          // Share of the whole host's CPU, not of the machine's own vCPUs.
          cpu: (d.cpuPercent * d.vcpus) / cpus,
          disk: disks.reduce((n, x) => n + x.readBps + x.writeBps, 0),
          iops: disks.reduce((n, x) => n + x.readIops + x.writeIops, 0),
          lat: Math.max(0, ...disks.map((x) => Math.max(x.readLatencyMs ?? 0, x.writeLatencyMs ?? 0))),
          net: nets.reduce((n, x) => n + x.rxBps + x.txBps, 0),
          drops: nets.reduce((n, x) => n + (x.problemsPs ?? 0), 0),
        }
      })
  })
  let hostCpu = $derived(live.snapshot?.host.cpuPercent ?? 0)
  let top = $derived([...rows].sort((a, b) => b.cpu - a.cpu || b.disk - a.disk || b.net - a.net).slice(0, 6))
  let busy = $derived(hostCpu >= 80)
</script>

{#if rows.length}
  <section class="ui-panel nn">
    <header>
      <h2><Activity size={16} /> Busiest machines right now</h2>
      <span class="muted" class:warn={busy}>host CPU {hostCpu.toFixed(0)} %{busy ? ' — busy: consider moving one of these' : ''}</span>
    </header>
    <table class="t">
      <thead><tr><th>Machine</th><th>Share of host CPU</th><th>Disk</th><th>IOPS</th><th>Worst latency</th><th>Network</th></tr></thead>
      <tbody>
        {#each top as r (r.uuid)}
          <tr>
            <td><a href={machineHref(r.uuid)} onclick={router.link}>{r.name}</a></td>
            <td class="mono"><span class="bar"><i style="width: {Math.min(100, r.cpu)}%"></i></span> {r.cpu.toFixed(1)} %</td>
            <td class="mono">{formatBps(r.disk)}</td>
            <td class="mono">{r.iops.toFixed(0)}</td>
            <td class="mono" class:warn={r.lat >= 20}>{r.lat ? `${r.lat.toFixed(1)} ms` : '—'}</td>
            <td class="mono">{formatBps(r.net)}{#if r.drops >= 1}<span class="warn"> · {r.drops.toFixed(0)} drops/s</span>{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>
{/if}

<style>
  .nn { padding: 14px 16px; margin: 14px 0; }
  header { display: flex; justify-content: space-between; gap: 12px; align-items: baseline; flex-wrap: wrap; margin-bottom: 8px; }
  h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 6px 8px; border-bottom: 1px solid var(--border); }
  .t td { padding: 6px 8px; border-bottom: 1px solid var(--border); }
  .bar { display: inline-block; width: 60px; height: 6px; background: var(--bg-elev); vertical-align: middle; margin-right: 6px; }
  .bar i { display: block; height: 100%; background: var(--accent); }
  .muted { color: var(--text-2); font-size: 13px; }
  .warn { color: var(--warn); }
</style>
