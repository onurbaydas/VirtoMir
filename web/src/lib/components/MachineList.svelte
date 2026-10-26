<script lang="ts">
  import { t } from '../i18n.svelte'
  import { session } from '../session.svelte'
  import Search from '@lucide/svelte/icons/search'
  import MonitorOff from '@lucide/svelte/icons/monitor-off'
  import Server from '@lucide/svelte/icons/server'
  import { isActive, live } from '../live.svelte'
  import { cluster } from '../cluster.svelte'
  import { api, type Action, type Domain, type Snapshot } from '../api'
  import MachineRow from './MachineRow.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import Play from '@lucide/svelte/icons/play'
  import Power from '@lucide/svelte/icons/power'
  import Zap from '@lucide/svelte/icons/zap'
  import { router } from '../router.svelte'
  import { projects } from '../projects.svelte'
  import { ent, violationsByDomain, type PlacementViolation } from '../enterprise'

  type Filter = 'all' | 'running' | 'stopped'
  let filter = $state<Filter>('all')
  let hostFilter = $state<string | null>(null) // null = all nodes
  let projectFilter = $state('') // '' = all projects, 'none' = only global machines
  let query = $state('')

  $effect(() => {
    if (!projects.loaded) void projects.load().catch(() => {})
  })

  interface ClusterDomain extends Domain {
    hostId?: string
    hostName?: string
  }

  let peerSnaps = $state<Record<string, Snapshot | null>>({})
  let violations = $state<Record<string, PlacementViolation>>({})

  async function pollViolations() {
    if (!cluster.clustered) return
    try {
      const list = await ent.placementViolations()
      violations = violationsByDomain(list)
    } catch {
      // placement not available
    }
  }

  $effect(() => {
    if (!cluster.clustered) return
    void pollViolations()
    const iv = setInterval(() => void pollViolations(), 5000)
    return () => clearInterval(iv)
  })

  async function pollPeers() {
    if (!cluster.clustered) return
    await Promise.all(
      cluster.members.map(async (m) => {
        if (!m.reachable || m.self) return
        try {
          peerSnaps[m.id] = await api.summary(m.id)
        } catch {
          peerSnaps[m.id] = null
        }
      }),
    )
  }

  $effect(() => {
    if (cluster.clustered) {
      void pollPeers()
      const iv = setInterval(() => void pollPeers(), 4000)
      return () => clearInterval(iv)
    }
  })

  let allDomains = $derived.by(() => {
    const list: ClusterDomain[] = []
    const selfName = cluster.current?.name ?? 'local'
    const selfId = cluster.current?.id ?? ''

    for (const d of live.snapshot?.domains ?? []) {
      list.push({ ...d, hostId: selfId, hostName: selfName })
    }

    if (cluster.clustered) {
      for (const m of cluster.members) {
        if (m.self) continue
        const snap = peerSnaps[m.id]
        if (snap?.domains) {
          for (const d of snap.domains) {
            list.push({ ...d, hostId: m.id, hostName: m.name })
          }
        }
      }
    }
    return list
  })

  let domains = $derived.by(() => {
    if (!cluster.clustered || !hostFilter) return allDomains
    return allDomains.filter((d) => d.hostId === hostFilter)
  })

  let counts = $derived({
    all: domains.length,
    running: domains.filter((d) => isActive(d.state) || d.state === 'paused').length,
    stopped: domains.filter((d) => !isActive(d.state) && d.state !== 'paused').length,
  })

  let shown = $derived(
    domains.filter((d) => {
      const on = isActive(d.state) || d.state === 'paused'
      if (filter === 'running' && !on) return false
      if (filter === 'stopped' && on) return false
      if (projectFilter === 'none' && d.project) return false
      if (projectFilter !== '' && projectFilter !== 'none' && (d.project ?? '') !== projectFilter) return false
      const q = query.trim().toLowerCase()
      return (
        !q ||
        d.name.toLowerCase().includes(q) ||
        d.uuid.startsWith(q) ||
        (d.hostName && d.hostName.toLowerCase().includes(q))
      )
    }),
  )
  let loading = $derived(live.snapshot === null)

  // ---- Bulk actions ---------------------------------------------------------------------
  let selected = $state<Set<string>>(new Set())
  // Forget machines that disappeared.
  let picked = $derived(domains.filter((d) => selected.has(d.uuid)))
  let allShown = $derived(shown.length > 0 && shown.every((d) => selected.has(d.uuid)))
  function select(uuid: string, on: boolean) {
    const next = new Set(selected)
    if (on) next.add(uuid)
    else next.delete(uuid)
    selected = next
  }
  function selectAll(on: boolean) {
    selected = on ? new Set(shown.map((d) => d.uuid)) : new Set()
  }
  const canStart = (d: Domain) => d.state === 'shutoff' || d.state === 'crashed'
  const canStop = (d: Domain) => isActive(d.state)
  const canKill = (d: Domain) => isActive(d.state) || d.state === 'paused'
  function bulk(action: Action, ok: (d: Domain) => boolean) {
    for (const d of picked.filter(ok)) void live.act(d, action)
    selected = new Set()
  }
</script>

<section class="panel">
  {#if cluster.clustered}
    <div class="node-filters" role="group" aria-label={t('Filter by host')}>
      <button
        class="node-pill"
        class:active={hostFilter === null}
        onclick={() => (hostFilter = null)}
      >
        <Server size={13} />
        <span>{t('ALL NODES')}</span>
        <span class="pill-count">{allDomains.length}</span>
      </button>
      {#each cluster.members as m (m.id)}
        {@const hostVMs = allDomains.filter((d) => d.hostId === m.id).length}
        <button
          class="node-pill"
          class:active={hostFilter === m.id}
          onclick={() => (hostFilter = m.id)}
        >
          <span class="node-dot" class:down={!m.reachable}></span>
          <span>{m.name}</span>
          <span class="pill-count">{hostVMs}</span>
        </button>
      {/each}
    </div>
  {/if}

  <header class="head">
    <h2>{t('Machines')}</h2>
    {#if counts.all > 0}
      <div class="tabs" role="tablist">
        {#each [['all', t('All')], ['running', t('Running')], ['stopped', t('Stopped')]] as [key, label] (key)}
          <button
            role="tab"
            aria-selected={filter === key}
            class:active={filter === key}
            onclick={() => (filter = key as Filter)}
          >
            {label}<span class="count num">{counts[key as Filter]}</span>
          </button>
        {/each}
      </div>
      {#if projects.list.length}
        <label class="proj-filter">
          <span class="sr-only">{t('Filter by project')}</span>
          <select bind:value={projectFilter} aria-label={t('Filter by project')}>
            <option value="">{t('All projects')}</option>
            <option value="none">{t('No project (global)')}</option>
            {#each projects.list as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
          </select>
        </label>
      {/if}
      <label class="search">
        <Search size={15} />
        <input type="search" placeholder={t('Find by name or UUID')} bind:value={query} spellcheck="false" />
      </label>
    {/if}
  </header>

  {#if picked.length}
    <div class="bulk" role="toolbar" aria-label={t('Actions for selected machines')}>
      <strong>{t('{n} selected', { n: picked.length })}</strong>
      <button class="ui-btn sm" disabled={!picked.some(canStart)} onclick={() => bulk('start', canStart)}>
        <Play size={14} /> {t('Start {v}', { v: picked.filter(canStart).length || '' })}
      </button>
      <button class="ui-btn sm" disabled={!picked.some(canStop)} onclick={() => bulk('shutdown', canStop)}>
        <Power size={14} /> {t('Shut down {v}', { v: picked.filter(canStop).length || '' })}
      </button>
      <ConfirmButton
        label={t('Force off {v}', { v: picked.filter(canKill).length || '' })}
        confirm={t('Cut power to {v}?', { v: picked.filter(canKill).length })}
        icon={Zap}
        disabled={!picked.some(canKill)}
        run={() => bulk('force-off', canKill)}
      />
      <span class="spacer"></span>
      <button class="ui-btn sm ghost" onclick={() => (selected = new Set())}>{t('Clear')}</button>
    </div>
  {/if}

  <div class="table" role="table" aria-label={t('Virtual machines')}>
    <div class="colhead" role="row">
      <span role="columnheader" class="name-h">
        {#if shown.length && session.can('operator')}
          <input type="checkbox" checked={allShown} aria-label={t('Select all shown machines')} onchange={(e) => selectAll(e.currentTarget.checked)} />
        {/if}
        {t('Name')}
      </span>
      <span role="columnheader">{t('State')}</span>
      <span role="columnheader">{t('Project')}</span>
      <span role="columnheader">{t('CPUs')}</span>
      <span role="columnheader">{t('Memory')}</span>
      <span role="columnheader">{t('CPU usage · 60 s')}</span>
      <span role="columnheader" class="sr">{t('Actions')}</span>
    </div>

    {#if loading}
      {#each [0, 1, 2] as i (i)}
        <div class="sk-row"><span class="sk" style:width="{180 - i * 30}px"></span></div>
      {/each}
    {:else if shown.length === 0}
      <div class="empty">
        <MonitorOff size={28} strokeWidth={1.5} />
        {#if counts.all === 0}
          <strong>{t('No virtual machines on this host yet')}</strong>
          <span>{t('Create one here, or with virt-manager or virsh — it appears instantly.')}</span>
          {#if session.can('admin')}<a class="ui-btn primary new" href="/machines/new" onclick={router.link}>{t('New machine')}</a>{/if}
        {:else}
          <strong>{t('Nothing matches')}</strong>
          <button class="link" onclick={() => ((filter = 'all'), (query = ''), (projectFilter = ''), (hostFilter = null))}>{t('Show all machines')}</button>
        {/if}
      </div>
    {:else}
      {#each shown as d (d.uuid)}
        <MachineRow {d} projectName={d.project ? projects.name(d.project) : undefined} hostName={cluster.clustered ? d.hostName : undefined} violation={violations[d.uuid]} selected={selected.has(d.uuid)} onselect={session.can('operator') ? (on) => select(d.uuid, on) : undefined} />
      {/each}
    {/if}
  </div>
</section>

<style>
  .panel {
    --cols: minmax(200px, 1.6fr) 150px 110px 80px 120px minmax(140px, 1.2fr) 190px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 14px 16px;
    flex-wrap: wrap;
  }
  h2 {
    margin: 0 8px 0 0;
    font-size: 16px;
    font-weight: 600;
  }
  .tabs {
    display: flex;
    gap: 2px;
    padding: 3px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  .tabs button {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 28px;
    padding: 0 11px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    transition:
      background var(--dur-fast),
      color var(--dur-fast);
  }
  .tabs button:hover {
    color: var(--text);
  }
  .tabs button.active {
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--bevel);
  }
  .count {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .proj-filter select {
    height: 34px;
    padding: 0 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-elev);
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  .proj-filter select:focus {
    border-color: var(--accent);
  }
  .search {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
    width: min(280px, 100%);
    height: 34px;
    padding: 0 11px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-elev);
    color: var(--text-3);
    transition: border-color var(--dur-fast);
  }
  .search:focus-within {
    border-color: var(--accent);
  }
  .search input {
    flex: 1;
    min-width: 0;
    border: 0;
    background: transparent;
    outline: none;
    font-size: 13.5px;
  }
  .bulk {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 8px 16px;
    border-top: 1px solid var(--border);
    background: var(--accent-soft);
    animation: slide var(--dur) var(--ease-out);
  }
  .bulk strong {
    margin-right: 6px;
    font-size: 13px;
  }
  .spacer {
    flex: 1;
  }
  @keyframes slide {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
  .name-h {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .name-h input {
    width: 16px;
    height: 16px;
    margin: 0;
    accent-color: var(--accent);
  }
  .colhead {
    display: grid;
    grid-template-columns: var(--cols);
    gap: 16px;
    padding: 8px 16px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .sk-row {
    display: flex;
    align-items: center;
    height: 64px;
    padding: 0 16px;
    border-top: 1px solid var(--border);
  }
  .sk {
    height: 14px;
    border-radius: var(--radius-sm);
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 48px 16px 56px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    margin-top: 6px;
    color: var(--text);
    font-weight: 600;
  }
  .new {
    margin-top: 10px;
  }
  .link {
    border: 0;
    background: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0;
  }

  .node-filters {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 16px;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .node-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 11.5px;
    font-weight: 700;
    cursor: pointer;
    color: var(--text-2);
    border-radius: var(--radius-sm);
    transition: all var(--dur-fast);
  }
  .node-pill:hover {
    border-color: var(--accent);
    color: var(--text);
  }
  .node-pill.active {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .pill-count {
    padding: 1px 5px;
    background: color-mix(in srgb, currentColor 18%, transparent);
    font-size: 10.5px;
    font-weight: 800;
  }
  .node-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--ok);
  }
  .node-dot.down {
    background: var(--danger);
  }

  @media (max-width: 1100px) {
    .panel {
      --cols: minmax(180px, 1.5fr) 140px 110px minmax(120px, 1fr) 170px;
    }
    .colhead > :nth-child(3),
    .panel :global(.row > :nth-child(3)) {
      display: none;
    }
  }
  @media (max-width: 760px) {
    .panel {
      --cols: minmax(0, 1fr) auto;
    }
    .colhead {
      display: none;
    }
    .panel :global(.row) {
      padding: 10px 16px;
      row-gap: 8px;
    }
    .panel :global(.row > :nth-child(3)),
    .panel :global(.row > :nth-child(4)),
    .panel :global(.row > :nth-child(5)) {
      display: none;
    }
    .panel :global(.row > :nth-child(7)) {
      grid-column: 1 / -1;
      justify-content: flex-start;
    }
  }
</style>
