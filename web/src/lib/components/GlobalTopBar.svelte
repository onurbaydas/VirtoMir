<script lang="ts">
  import { live } from '../live.svelte'
  import { router, machineHref } from '../router.svelte'
  import { cluster } from '../cluster.svelte'
  import { alerts } from '../alerts.svelte'
  import { theme } from '../theme.svelte'
  import { toasts } from '../toast.svelte'
  import { api } from '../api'
  import { ent } from '../enterprise'
  import Sun from '@lucide/svelte/icons/sun'
  import Moon from '@lucide/svelte/icons/moon'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Plus from '@lucide/svelte/icons/plus'

  // Real health: daemon version, HA quorum (clustered only), storage pools, live data age.
  let version = $state('')
  let newer = $state('') // a newer release on GitHub
  let quorum = $state<{ votes: number; total: number; ok: boolean } | null>(null)
  let storage = $state<{ ok: boolean; text: string } | null>(null)
  let now = $state(Date.now())

  $effect(() => {
    void api.get<{ version: string }>('/api/v1/version').then((v) => (version = v.version)).catch(() => {})
    void api.get<{ available: boolean; latest?: { version: string } }>('/api/v1/update')
      .then((u) => (newer = u.available ? (u.latest?.version ?? '') : ''))
      .catch(() => {})
    const tick = setInterval(() => (now = Date.now()), 1000)
    const poll = async () => {
      try {
        const pools = await api.pools()
        const stopped = pools.filter((p) => !p.active)
        const full = pools.filter((p) => p.active && p.capacityBytes > 0 && p.allocationBytes / p.capacityBytes > 0.9)
        storage = stopped.length || full.length
          ? { ok: false, text: stopped.length ? `${stopped.length} POOL STOPPED` : `${full[0].name} ${Math.round((full[0].allocationBytes / full[0].capacityBytes) * 100)}% FULL` }
          : { ok: true, text: `${pools.length} POOLS OK` }
      } catch {
        storage = null
      }
      if (cluster.clustered) {
        try {
          const s = await ent.haStatus()
          quorum = { votes: s.quorum.votes, total: s.quorum.totalVotes, ok: s.quorum.quorate }
        } catch {
          quorum = null
        }
      } else quorum = null
    }
    void poll()
    const t = setInterval(() => void poll(), 15_000)
    return () => {
      clearInterval(t)
      clearInterval(tick)
    }
  })
  let age = $derived(live.lastUpdate ? Math.max(0, Math.round((now - live.lastUpdate) / 1000)) : null)

  let safeMode = $state(localStorage.getItem('virtomir.safemode') === 'true')
  $effect(() => {
    document.documentElement.dataset.safemode = String(safeMode)
  })

  // Pinned workspace tabs
  interface PinnedTab {
    id: string
    title: string
    href: string
    type: 'vm' | 'node' | 'pool' | 'net'
  }
  let pinnedTabs = $state<PinnedTab[]>([{ id: 'overview', title: 'HOST', href: '/', type: 'node' }])

  $effect(() => {
    const r = router.route
    if (r.name === 'machine' && r.uuid) {
      const d = (live.snapshot?.domains ?? []).find((x) => x.uuid === r.uuid)
      const title = d ? `VM: ${d.name}` : `VM: ${r.uuid.slice(0, 8)}`
      if (!pinnedTabs.some((t) => t.id === r.uuid)) {
        pinnedTabs = [...pinnedTabs, { id: r.uuid, title, href: machineHref(r.uuid), type: 'vm' }]
      }
    } else if (r.name === 'machines') {
      const id = 'vms-list'
      if (!pinnedTabs.some((t) => t.id === id)) {
        pinnedTabs = [...pinnedTabs, { id, title: 'MACHINES', href: '/machines', type: 'vm' }]
      }
    } else if (r.name === 'storage' && r.pool) {
      const id = `pool-${r.pool}`
      if (!pinnedTabs.some((t) => t.id === id)) {
        pinnedTabs = [...pinnedTabs, { id, title: `POOL: ${r.pool}`, href: `/storage?pool=${r.pool}`, type: 'pool' }]
      }
    }
  })

  function closePinned(id: string, e: MouseEvent) {
    e.preventDefault()
    e.stopPropagation()
    pinnedTabs = pinnedTabs.filter((t) => t.id !== id)
  }

  function toggleSafeMode() {
    safeMode = !safeMode
    try {
      localStorage.setItem('virtomir.safemode', String(safeMode))
    } catch {
      /* private window */
    }
    toasts.push(
      safeMode
        ? { kind: 'warn', title: 'Safe mode on', detail: 'Every power action asks you to type the machine name first.' }
        : { kind: 'info', title: 'Safe mode off', detail: 'Only force off and hard reset ask for the machine name.' },
    )
  }

  function openCommandPalette() {
    window.dispatchEvent(new CustomEvent('open-command-palette'))
  }
</script>

<header class="global-top-bar">
  <!-- Left Section: Logo & Breadcrumbs -->
  <div class="left-group">
    <a class="deck-logo" href="/" onclick={router.link} title="VirtoMir Control Deck">
      <span class="logo-box"></span>
      <span class="logo-text">VIRTOMIR</span>
      {#if version}<span class="logo-badge mono">{version}</span>{/if}
    </a>

    <div class="breadcrumbs mono">
      <span class="crumb">[{cluster.clustered ? `CLUSTER: ${cluster.members.length} HOSTS` : 'STANDALONE'}]</span>
      <span class="sep">/</span>
      <span class="crumb">[HOST: {live.host?.hostname?.split('.')[0] ?? '…'}]</span>
      {#if router.route.name === 'machines'}
        <span class="sep">/</span>
        <span class="crumb active">[WORKLOADS: VMS]</span>
      {:else if router.route.name === 'machine'}
        <span class="sep">/</span>
        <span class="crumb active">[VM: {live.snapshot?.domains.find((d) => d.uuid === (router.route as any).uuid)?.name ?? 'CURRENT'}]</span>
      {/if}
    </div>
  </div>

  <!-- Center Section: Command Palette Trigger -->
  <div class="center-group">
    <button class="cmd-palette-btn" onclick={openCommandPalette} title="Command Palette (Ctrl+K)">
      <span class="prompt">>_</span>
      <span class="text">COMMAND & SEARCH...</span>
      <kbd>CTRL+K</kbd>
    </button>
  </div>

  <!-- Right Section: Cluster Health & Controls -->
  <div class="right-group mono">
    {#if newer}
      <a class="health-block warn" href="/account" onclick={router.link} title="A newer VirtoMir release is available">
        <span>UPDATE {newer}</span>
      </a>
    {/if}

    {#if quorum}
      <a class="health-block" class:ok={quorum.ok} class:warn={!quorum.ok} href="/cluster" onclick={router.link} title="HA quorum: votes this host sees">
        <span class="dot">●</span>
        <span>Q: {quorum.votes}/{quorum.total} {quorum.ok ? 'OK' : 'LOST'}</span>
      </a>
    {/if}

    {#if storage}
      <a class="health-block" class:ok={storage.ok} class:warn={!storage.ok} href="/storage" onclick={router.link} title="Storage pools of this host">
        <span>STOR: {storage.text}</span>
      </a>
    {/if}

    {#if alerts.open > 0}
      <a class="health-block warn" href="/alerts" onclick={router.link} title="{alerts.open} unacknowledged alerts">
        <span>▲ {alerts.open} WARN</span>
      </a>
    {:else}
      <div class="health-block ok" title="0 Alerts Active">
        <span>ALERTS: 0</span>
      </div>
    {/if}

    <div class="health-block" class:warn={age !== null && age > 5} title="Age of the newest live sample (updated every second)">
      <span>LIVE: {age === null ? '—' : `${age}s`}</span>
    </div>

    <!-- Global Actions -->
    <a class="action-btn accent" href="/machines/new" onclick={router.link}>
      <Plus size={13} />
      <span>NEW VM</span>
    </a>

    <!-- Theme Switch -->
    <button
      class="icon-control-btn"
      onclick={() => theme.set(theme.resolved === 'dark' ? 'light' : 'dark')}
      title="Toggle Theme: {theme.resolved === 'dark' ? 'Matte Obsidian' : 'Warm Limestone'}"
    >
      {#if theme.resolved === 'dark'}
        <Sun size={14} />
      {:else}
        <Moon size={14} />
      {/if}
    </button>

    <!-- Safe-Mode Lock -->
    <button class="control-btn safe-btn" class:armed={safeMode} onclick={toggleSafeMode} title="Safe mode: every power action asks for the machine name">
      {#if safeMode}
        <ShieldCheck size={13} />
        <span>SAFE: ON</span>
      {:else}
        <ShieldAlert size={13} />
        <span>SAFE: OFF</span>
      {/if}
    </button>
  </div>
</header>

<!-- Pinned Workspace Tabs Bar -->
<nav class="pinned-bar" aria-label="Pinned tabs">
  <div class="pinned-scroll">
    {#each pinnedTabs as tab (tab.id)}
      <a
        class="pinned-tab mono"
        class:active={(router.route.name === 'machine' && (router.route as any).uuid === tab.id) || (tab.href === '/' && router.route.name === 'overview')}
        href={tab.href}
        onclick={router.link}
      >
        <span class="type-badge">{tab.type === 'vm' ? '■' : tab.type === 'pool' ? '⛁' : '❖'}</span>
        <span class="title">{tab.title}</span>
        {#if pinnedTabs.length > 1}
          <button class="close-tab" onclick={(e) => closePinned(tab.id, e)} title="Close tab">✕</button>
        {/if}
      </a>
    {/each}
  </div>
</nav>

<style>
  .global-top-bar {
    height: 42px;
    background: var(--bg-surface, var(--surface));
    border-bottom: 1px solid var(--border-strong);
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 12px;
    gap: 12px;
    z-index: 100;
    flex-shrink: 0;
  }
  .left-group {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
    flex-shrink: 1;
    overflow: hidden;
  }
  .deck-logo {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 4px 8px;
    background: var(--bg-elev);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
    text-decoration: none;
    color: var(--text);
  }
  .logo-box {
    width: 9px;
    height: 9px;
    background: var(--accent);
  }
  .logo-text {
    font-family: var(--font-display);
    font-size: 13px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .logo-badge {
    font-size: 9.5px;
    color: var(--text-3);
  }
  .breadcrumbs {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    overflow: hidden;
    font-size: 11px;
    color: var(--text-2);
  }
  .crumb {
    padding: 2px 6px;
    white-space: nowrap;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    background: var(--bg-elev);
    border: 1px solid var(--border);
  }
  .crumb.active {
    border-color: var(--accent);
    color: var(--accent);
    font-weight: 700;
  }
  .sep {
    color: var(--text-3);
  }
  .center-group {
    flex: 1 1 120px;
    min-width: 0;
    max-width: 440px;
  }
  .cmd-palette-btn {
    width: 100%;
    white-space: nowrap;
    overflow: hidden;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 28px;
    padding: 0 10px;
    background: var(--bg-elev);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 11px;
    cursor: pointer;
  }
  .cmd-palette-btn:hover {
    border-color: var(--accent);
    color: var(--text);
  }
  .prompt {
    color: var(--accent);
    font-weight: 700;
  }
  .text {
    flex: 1;
    text-align: left;
  }
  .cmd-palette-btn kbd {
    padding: 1px 4px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-3);
    font-size: 9.5px;
  }
  .right-group {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    flex-shrink: 0;
    white-space: nowrap;
  }
  .health-block {
    display: flex;
    align-items: center;
    white-space: nowrap;
    gap: 5px;
    padding: 3px 7px;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    color: var(--text-2);
    text-decoration: none;
  }
  .health-block.ok {
    border-color: color-mix(in srgb, var(--ok) 40%, transparent);
    color: var(--ok);
  }
  .health-block.warn {
    border-color: var(--warn);
    color: var(--warn);
    background: color-mix(in srgb, var(--warn) 10%, var(--bg-elev));
  }
  .dot {
    font-size: 9px;
  }
  .action-btn {
    display: flex;
    align-items: center;
    gap: 5px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border-strong);
    background: var(--metal);
    box-shadow: var(--bevel);
    color: var(--text);
    font-family: var(--font-display);
    font-size: 11.5px;
    font-weight: 700;
    text-decoration: none;
    letter-spacing: 0.05em;
    cursor: pointer;
  }
  .action-btn.accent {
    background: var(--metal-accent);
    color: var(--accent-contrast);
  }
  .icon-control-btn,
  .control-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 28px;
    padding: 0 8px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 11px;
    cursor: pointer;
  }
  .control-btn:hover,
  .icon-control-btn:hover {
    color: var(--text);
    border-color: var(--accent);
  }
  .safe-btn.armed {
    background: color-mix(in srgb, var(--warn) 15%, var(--surface));
    border-color: var(--warn);
    color: var(--warn);
    font-weight: 700;
  }

  /* Pinned Tabs Bar */
  .pinned-bar {
    height: 30px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: flex-end;
    padding: 0 12px;
    overflow-x: auto;
    flex-shrink: 0;
  }
  .pinned-scroll {
    display: flex;
    gap: 2px;
    height: 100%;
    align-items: flex-end;
  }
  .pinned-tab {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 26px;
    padding: 0 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-bottom: none;
    color: var(--text-2);
    font-size: 11px;
    text-decoration: none;
    white-space: nowrap;
    cursor: pointer;
  }
  .pinned-tab:hover {
    color: var(--text);
  }
  .pinned-tab.active {
    background: var(--bg);
    border-top: 2px solid var(--accent);
    color: var(--accent);
    font-weight: 700;
  }
  .type-badge {
    font-size: 10px;
  }
  .close-tab {
    background: none;
    border: none;
    color: inherit;
    font-size: 10px;
    padding: 0 2px;
    cursor: pointer;
    opacity: 0.6;
  }
  .close-tab:hover {
    opacity: 1;
    color: var(--danger);
  }
  @media (max-width: 900px) {
    .breadcrumbs,
    .center-group,
    .health-block {
      display: none;
    }
  }
</style>
