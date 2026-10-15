<script lang="ts">
  import { live } from '../live.svelte'
  import { router, machineHref } from '../router.svelte'
  import { cluster } from '../cluster.svelte'
  import { alerts } from '../alerts.svelte'
  import { theme } from '../theme.svelte'
  import { toasts } from '../toast.svelte'
  import Sun from '@lucide/svelte/icons/sun'
  import Moon from '@lucide/svelte/icons/moon'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Plus from '@lucide/svelte/icons/plus'
  import Box from '@lucide/svelte/icons/box'
  import Layers from '@lucide/svelte/icons/layers'
  import Download from '@lucide/svelte/icons/download'
  import X from '@lucide/svelte/icons/x'

  type Density = 'compact' | 'standard' | 'relaxed'
  let density = $state<Density>('standard')
  let safeMode = $state(false)

  // Pinned workspace tabs
  interface PinnedTab {
    id: string
    title: string
    href: string
    type: 'vm' | 'node' | 'pool' | 'net'
  }
  let pinnedTabs = $state<PinnedTab[]>([
    { id: 'overview', title: 'HOST: ' + (live.host?.hostname ?? 'LOCAL'), href: '/', type: 'node' }
  ])

  $effect(() => {
    // Sync current route to pinned tabs
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
        pinnedTabs = [...pinnedTabs, { id, title: 'VMS INVENTORY', href: '/machines', type: 'vm' }]
      }
    } else if (r.name === 'storage' && r.pool) {
      const id = `pool-${r.pool}`
      if (!pinnedTabs.some((t) => t.id === id)) {
        pinnedTabs = [...pinnedTabs, { id, title: `POOL: ${r.pool}`, href: `/storage?pool=${r.pool}`, type: 'pool' }]
      }
    }
  })

  function closePinned(id: string, e: MouseEvent) {
    e.stopPropagation()
    pinnedTabs = pinnedTabs.filter((t) => t.id !== id)
  }

  function cycleDensity() {
    const next: Record<Density, Density> = { compact: 'standard', standard: 'relaxed', relaxed: 'compact' }
    density = next[density]
    document.documentElement.dataset.density = density
    toasts.push({ kind: 'info', title: `Density: ${density.toUpperCase()}` })
  }

  function toggleSafeMode() {
    safeMode = !safeMode
    document.documentElement.dataset.safemode = String(safeMode)
    if (safeMode) {
      toasts.push({ kind: 'warn', title: 'SAFE-MODE ARMED', detail: 'Destructive power and disk deletions are locked.' })
    } else {
      toasts.push({ kind: 'info', title: 'SAFE-MODE OFF', detail: 'Standard operator safeguards active.' })
    }
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
      <span class="logo-badge mono">v3.4-PRO</span>
    </a>

    <div class="breadcrumbs mono">
      <span class="crumb">[DC: {cluster.clustered ? 'CLUSTER' : 'LOCAL-DC'}]</span>
      <span class="sep">/</span>
      <span class="crumb">[NODE: {live.host?.hostname ?? 'HV-01'}]</span>
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
    <div class="health-block ok" title="Quorum Consensus Status">
      <span class="dot">●</span>
      <span>Q: 5/5 OK</span>
    </div>

    <div class="health-block ok" title="Storage Fabric Health">
      <span>STOR: HEALTH_OK</span>
    </div>

    {#if alerts.open > 0}
      <a class="health-block warn" href="/alerts" onclick={router.link} title="{alerts.open} unacknowledged alerts">
        <span>▲ {alerts.open} WARN</span>
      </a>
    {:else}
      <div class="health-block ok" title="0 Alerts Active">
        <span>ALERTS: 0</span>
      </div>
    {/if}

    <div class="health-block" title="WebSocket Latency">
      <span>WS: 2ms</span>
    </div>

    <!-- Global Actions -->
    <a class="action-btn accent" href="/machines/new" onclick={router.link}>
      <Plus size={13} />
      <span>+ NEW VM</span>
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

    <!-- Density Switch -->
    <button class="control-btn" onclick={cycleDensity} title="Cycle Density (Compact / Standard / Relaxed)">
      <span>[{density.slice(0, 3).toUpperCase()}]</span>
    </button>

    <!-- Safe-Mode Lock -->
    <button class="control-btn safe-btn" class:armed={safeMode} onclick={toggleSafeMode} title="Emergency Safe-Mode Lock">
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
    font-size: 11px;
    color: var(--text-2);
  }
  .crumb {
    padding: 2px 6px;
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
    flex: 1;
    max-width: 440px;
  }
  .cmd-palette-btn {
    width: 100%;
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
  }
  .health-block {
    display: flex;
    align-items: center;
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
