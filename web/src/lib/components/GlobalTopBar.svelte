<script lang="ts">
  // The bar above every page: where you are, search / commands, and only the signals that
  // need attention (an update, lost quorum, a full or stopped pool, alerts, running tasks).
  import { live } from '../live.svelte'
  import { router } from '../router.svelte'
  import { cluster } from '../cluster.svelte'
  import { alerts } from '../alerts.svelte'
  import { session } from '../session.svelte'
  import { api, type Job } from '../api'
  import { ent } from '../enterprise'
  import Search from '@lucide/svelte/icons/search'
  import Plus from '@lucide/svelte/icons/plus'
  import ListChecks from '@lucide/svelte/icons/list-checks'
  import Bell from '@lucide/svelte/icons/bell'
  import ArrowUpCircle from '@lucide/svelte/icons/circle-arrow-up'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'

  let newer = $state('')
  let quorum = $state<{ votes: number; total: number; ok: boolean } | null>(null)
  let storageIssue = $state('')
  let running = $state(0)

  $effect(() => {
    void api.get<{ available: boolean; latest?: { version: string } }>('/api/v1/update')
      .then((u) => (newer = u.available ? (u.latest?.version ?? '') : ''))
      .catch(() => {})
    const poll = async () => {
      try {
        const pools = await api.pools()
        const stopped = pools.filter((p) => !p.active)
        const full = pools.find((p) => p.active && p.capacityBytes > 0 && p.allocationBytes / p.capacityBytes > 0.9)
        storageIssue = stopped.length ? `${stopped.length} storage pool${stopped.length > 1 ? 's' : ''} stopped` : full ? `${full.name} is ${Math.round((full.allocationBytes / full.capacityBytes) * 100)}% full` : ''
      } catch {
        storageIssue = ''
      }
      try {
        running = (await api.jobs()).filter((j: Job) => j.state === 'running').length
      } catch {
        /* the connection banner covers outages */
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
    const t = setInterval(() => void poll(), 10_000)
    return () => clearInterval(t)
  })

  const PAGE: Record<string, string> = {
    overview: 'Overview', machines: 'Machines', new: 'New machine', import: 'Import', storage: 'Storage',
    networks: 'Networks', backups: 'Backups', alerts: 'Alerts', activity: 'Activity', account: 'Account',
    hardware: 'Hardware', reports: 'Reports', cluster: 'Cluster',
  }
  let hostName = $derived((cluster.clustered ? cluster.current?.name : null) ?? live.host?.hostname?.split('.')[0] ?? '')
  let machineName = $derived(
    router.route.name === 'machine' ? (live.snapshot?.domains.find((d) => d.uuid === (router.route as { uuid: string }).uuid)?.name ?? '') : '',
  )
  let pageName = $derived(router.route.name === 'machine' ? 'Machines' : (PAGE[router.route.name] ?? ''))

  const openPalette = () => window.dispatchEvent(new CustomEvent('open-command-palette'))
  const toggleDrawer = () => window.dispatchEvent(new CustomEvent('toggle-drawer'))
  const isMac = typeof navigator !== 'undefined' && /Mac/.test(navigator.platform)
</script>

<header class="bar">
  <nav class="crumbs" aria-label="Location">
    {#if hostName}<span class="host">{hostName}</span><span class="sep">/</span>{/if}
    {#if machineName}
      <a href="/machines" onclick={router.link}>{pageName}</a><span class="sep">/</span><span class="here">{machineName}</span>
    {:else}
      <span class="here">{pageName}</span>
    {/if}
  </nav>

  <button class="search" onclick={openPalette} title="Search machines and pages, or run a command">
    <Search size={14} />
    <span>Search or run a command…</span>
    <kbd>{isMac ? '⌘' : 'Ctrl'} K</kbd>
  </button>

  <div class="right">
    {#if newer}
      <a class="signal accent" href="/account" onclick={router.link} title="A newer VirtoMir release is available"><ArrowUpCircle size={14} /> {newer}</a>
    {/if}
    {#if quorum && !quorum.ok}
      <a class="signal danger" href="/cluster" onclick={router.link} title="High availability: this host sees {quorum.votes} of {quorum.total} votes"><TriangleAlert size={14} /> No quorum</a>
    {/if}
    {#if storageIssue}
      <a class="signal warn" href="/storage" onclick={router.link}><TriangleAlert size={14} /> {storageIssue}</a>
    {/if}
    {#if alerts.open > 0}
      <a class="signal" class:danger={alerts.critical} class:warn={!alerts.critical} href="/alerts" onclick={router.link} title="Unacknowledged alerts"><Bell size={14} /> {alerts.open}</a>
    {/if}
    <button class="icon-btn" onclick={toggleDrawer} title="Tasks, recent changes and API calls (`)">
      <ListChecks size={16} />
      {#if running}<span class="dot-count">{running}</span>{/if}
    </button>
    {#if session.canCreate}
      <a class="ui-btn primary sm" href="/machines/new" onclick={router.link}><Plus size={14} /> New machine</a>
    {/if}
  </div>
</header>

<style>
  .bar {
    position: sticky;
    top: 0;
    z-index: 15;
    display: flex;
    align-items: center;
    gap: 16px;
    height: 52px;
    padding: 0 28px;
    border-bottom: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg) 88%, transparent);
    backdrop-filter: blur(10px);
  }
  .crumbs {
    display: flex;
    flex: 1 1 0;
    align-items: center;
    gap: 8px;
    min-width: 0;
    overflow: hidden;
    color: var(--text-3);
    font-size: 13.5px;
    white-space: nowrap;
  }
  .crumbs a {
    color: var(--text-2);
    text-decoration: none;
  }
  .crumbs a:hover {
    color: var(--text);
  }
  .host {
    color: var(--text-2);
  }
  .here {
    overflow: hidden;
    color: var(--text);
    font-weight: 500;
    text-overflow: ellipsis;
  }
  .search {
    display: flex;
    flex: 0 1 360px;
    align-items: center;
    gap: 8px;
    min-width: 0;
    height: 32px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text-3);
    font-size: 13px;
    cursor: pointer;
    white-space: nowrap;
  }
  .search:hover {
    border-color: var(--border-strong);
    color: var(--text-2);
  }
  .search span {
    flex: 1;
    overflow: hidden;
    text-align: left;
    text-overflow: ellipsis;
  }
  kbd {
    padding: 1px 6px;
    border: 1px solid var(--border);
    border-radius: 4px;
    font-family: var(--font-sans);
    font-size: 11px;
  }
  .right {
    display: flex;
    flex: 1 1 0;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    white-space: nowrap;
  }
  .signal {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 10px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 500;
    text-decoration: none;
  }
  .signal.accent {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .signal.warn {
    background: color-mix(in srgb, var(--warn) 14%, transparent);
    color: var(--warn);
  }
  .signal.danger {
    background: color-mix(in srgb, var(--danger) 14%, transparent);
    color: var(--danger);
  }
  .icon-btn {
    position: relative;
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-2);
    cursor: pointer;
  }
  .icon-btn:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .dot-count {
    position: absolute;
    top: 2px;
    right: 1px;
    min-width: 15px;
    height: 15px;
    padding: 0 4px;
    border-radius: 999px;
    background: var(--accent);
    color: var(--accent-contrast);
    font-size: 10px;
    font-weight: 600;
    line-height: 15px;
  }
  @media (max-width: 1100px) {
    .search span {
      display: none;
    }
    .search {
      flex: none;
    }
  }
  @media (max-width: 900px) {
    .bar {
      display: none;
    }
  }
</style>
