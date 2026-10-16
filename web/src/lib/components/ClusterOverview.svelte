<script lang="ts">
  // All hosts at a glance: a live card per member and every machine of the cluster in one
  // list. Each member's newest snapshot is polled (GET …/summary), no stream per host.
  import Search from '@lucide/svelte/icons/search'
  import { api, type ClusterMember, type Domain, type Snapshot } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatKiB } from '../format'
  import { isActive } from '../live.svelte'
  import { router } from '../router.svelte'
  import StatePill from './StatePill.svelte'
  import HistoryChart from './HistoryChart.svelte'
  import RangeTabs, { type Range } from './RangeTabs.svelte'
  import type { HistoryRange } from '../api'

  // History of every host side by side (each host keeps its own, ADR-0013).
  const COLORS = ['--accent', '--accent-2', '--info', '--warn', '--ok', '--danger']
  let histRange = $state<Range>('24h')
  let histLines = $derived((key: string) =>
    cluster.members
      .filter((m) => m.reachable)
      .map((m, i) => ({ key, label: m.name, color: COLORS[i % COLORS.length], host: m.id, dashed: i >= COLORS.length })),
  )

  let snaps = $state<Record<string, Snapshot | null>>({})

  async function poll() {
    await Promise.all(
      cluster.members.map(async (m) => {
        if (!m.reachable) {
          snaps[m.id] = null
          return
        }
        try {
          snaps[m.id] = await api.summary(m.id)
        } catch {
          snaps[m.id] = null
        }
      }),
    )
  }

  $effect(() => {
    void cluster.members.length // re-poll when membership changes
    void poll()
    const t = setInterval(() => void poll(), 3000)
    return () => clearInterval(t)
  })

  interface Row extends Domain {
    host: ClusterMember
  }
  let filter = $state('')
  let rows = $derived.by(() => {
    const out: Row[] = []
    for (const m of cluster.members) for (const d of snaps[m.id]?.domains ?? []) out.push({ ...d, host: m })
    const q = filter.trim().toLowerCase()
    return out
      .filter((r) => !q || r.name.toLowerCase().includes(q) || r.uuid.startsWith(q) || r.host.name.toLowerCase().includes(q))
      .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }) || a.host.name.localeCompare(b.host.name))
  })

  const memPct = (s: Snapshot) => (s.host.memTotalKiB ? (s.host.memUsedKiB / s.host.memTotalKiB) * 100 : 0)
  const hostID = (m: ClusterMember) => (m.self ? null : m.id)
</script>

<section class="hosts" aria-label="Hosts">
  {#each cluster.members as m (m.id)}
    {@const s = snaps[m.id]}
    {@const doms = s?.domains ?? []}
    <button class="ui-panel host" class:down={!m.reachable} class:current={cluster.current?.id === m.id} disabled={!m.reachable} onclick={() => cluster.open(m.id)}>
      <div class="h-head">
        <span class="dot" class:ok={m.reachable && s?.connected !== false} class:warn={m.reachable && s?.connected === false}></span>
        <strong>{m.name}</strong>
        {#if m.self}<span class="pill">this host</span>{/if}
      </div>
      {#if !m.reachable}
        <span class="h-err">{m.error ?? 'Not reachable'}</span>
      {:else if !s}
        <span class="muted">Loading…</span>
      {:else if !s.connected}
        <span class="h-err">libvirt is not answering</span>
      {:else}
        <div class="meter" title="CPU {s.host.cpuPercent.toFixed(1)} %">
          <span class="m-label">CPU</span>
          <span class="bar"><span style:width="{Math.min(100, s.host.cpuPercent)}%"></span></span>
          <span class="m-val mono">{s.host.cpuPercent.toFixed(0)} %</span>
        </div>
        <div class="meter" title="{formatKiB(s.host.memUsedKiB)} of {formatKiB(s.host.memTotalKiB)}">
          <span class="m-label">Memory</span>
          <span class="bar mem"><span style:width="{memPct(s)}%"></span></span>
          <span class="m-val mono">{memPct(s).toFixed(0)} %</span>
        </div>
        <span class="muted">{doms.filter((d) => isActive(d.state)).length} of {doms.length} machines running</span>
      {/if}
    </button>
  {/each}
</section>

<section class="ui-panel hist">
  <header class="a-head">
    <h2>History by host</h2>
    <RangeTabs bind:value={histRange} live={false} />
  </header>
  <div class="h-grid">
    {#key cluster.members.map((m) => m.id + m.reachable).join()}
      <HistoryChart title="CPU" unit="percent" range={histRange as HistoryRange} lines={histLines('host.cpu')} />
      <HistoryChart title="Memory" unit="percent" range={histRange as HistoryRange} lines={histLines('host.mem')} />
    {/key}
  </div>
</section>

<section class="ui-panel all">
  <header class="a-head">
    <h2>All machines <span class="count">{rows.length}</span></h2>
    <label class="search">
      <Search size={14} />
      <input class="ui-input" placeholder="Find by name, UUID or host" bind:value={filter} />
    </label>
  </header>
  {#if rows.length}
    <table>
      <thead>
        <tr><th>Name</th><th>Host</th><th>State</th><th>CPUs</th><th>Memory</th><th class="num">CPU usage</th></tr>
      </thead>
      <tbody>
        {#each rows as r (r.host.id + r.uuid)}
          <tr>
            <td>
              <a href={router.hostHref(hostID(r.host), `/machines/${r.uuid}`)} onclick={(e) => { e.preventDefault(); router.go(hostID(r.host), `/machines/${r.uuid}`) }}>{r.name}</a>
              {#if r.template}<span class="pill">template</span>{/if}
            </td>
            <td>{r.host.name}</td>
            <td><StatePill state={r.state} /></td>
            <td>{r.vcpus} vCPU</td>
            <td>{formatKiB(r.memoryKiB)}</td>
            <td class="num mono">{isActive(r.state) ? `${r.cpuPercent.toFixed(1)} %` : '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="muted empty">{filter ? 'Nothing matches.' : 'No machines on any host yet.'}</p>
  {/if}
</section>

<style>
  .hosts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 12px;
    margin-bottom: 14px;
  }
  .host {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px 16px;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
    transition: border-color var(--dur);
  }
  .host:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .host.current {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .host:disabled {
    cursor: not-allowed;
    opacity: 0.75;
  }
  .h-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: var(--radius-sm);
    background: var(--danger);
  }
  .dot.ok {
    background: var(--ok);
  }
  .dot.warn {
    background: var(--warn);
  }
  .meter {
    display: grid;
    grid-template-columns: 56px 1fr 40px;
    align-items: center;
    gap: 8px;
    font-size: 12px;
  }
  .m-label {
    color: var(--text-3);
  }
  .m-val {
    text-align: right;
  }
  .bar {
    height: 6px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.4s;
  }
  .bar.mem span {
    background: var(--accent-2);
  }
  .h-err {
    color: var(--danger);
    font-size: 12px;
  }
  .muted {
    color: var(--text-3);
    font-size: 12px;
  }
  .pill {
    padding: 1px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11px;
    font-weight: 500;
    margin-left: 6px;
  }
  .hist {
    margin-bottom: 14px;
  }
  .h-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: 16px;
    padding: 14px 16px;
  }
  .all {
    margin-bottom: 14px;
    overflow-x: auto;
  }
  .a-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .a-head h2 {
    margin: 0;
    font-size: 15px;
  }
  .count {
    color: var(--text-3);
    font-weight: 500;
    margin-left: 4px;
  }
  .search {
    position: relative;
    display: flex;
    align-items: center;
    width: min(280px, 50%);
  }
  .search :global(svg) {
    position: absolute;
    left: 10px;
    color: var(--text-3);
  }
  .search input {
    width: 100%;
    padding-left: 30px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th {
    text-align: left;
    padding: 9px 16px;
    color: var(--text-3);
    font-size: 11px;
    font-weight: 600;
  }
  td {
    padding: 10px 16px;
    border-top: 1px solid var(--border);
    white-space: nowrap;
  }
  td a {
    color: var(--text);
    font-weight: 600;
    text-decoration: none;
  }
  td a:hover {
    color: var(--accent);
  }
  .num {
    text-align: right;
  }
  .empty {
    padding: 18px 16px;
    margin: 0;
  }
</style>
