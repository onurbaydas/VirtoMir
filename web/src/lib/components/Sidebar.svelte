<script lang="ts">
  import Server from '@lucide/svelte/icons/server'
  import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard'
  import Sun from '@lucide/svelte/icons/sun'
  import Moon from '@lucide/svelte/icons/moon'
  import MonitorCog from '@lucide/svelte/icons/monitor-cog'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Network from '@lucide/svelte/icons/network'
  import Plus from '@lucide/svelte/icons/plus'
  import LogOut from '@lucide/svelte/icons/log-out'
  import UserRound from '@lucide/svelte/icons/user-round'
  import ClockArrowUp from '@lucide/svelte/icons/clock-arrow-up'
  import Bell from '@lucide/svelte/icons/bell'
  import Archive from '@lucide/svelte/icons/archive'
  import Boxes from '@lucide/svelte/icons/boxes'
  import CircuitBoard from '@lucide/svelte/icons/circuit-board'
  import FileText from '@lucide/svelte/icons/file-text'
  import { cluster } from '../cluster.svelte'
  import { alerts } from '../alerts.svelte'
  import { session } from '../session.svelte'
  import { isActive, live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { theme, type ThemePref } from '../theme.svelte'


  type Status = 'up' | 'degraded' | 'down' | 'pending'
  let status = $derived<Status>(
    !live.streamOpen ? 'down' : live.snapshot?.connected === false ? 'degraded' : live.snapshot ? 'up' : 'pending',
  )
  const STATUS_TEXT: Record<Status, string> = {
    up: 'Connected',
    degraded: 'libvirt unreachable',
    down: 'VirtoMir unreachable',
    pending: 'Connecting…',
  }

  const ALL_NAV = [
    { name: 'overview', href: '/', label: 'Overview', icon: LayoutDashboard, role: 'viewer' },
    { name: 'machines', href: '/machines', label: 'Machines', icon: Boxes, role: 'viewer' },
    { name: 'storage', href: '/storage', label: 'Storage', icon: HardDrive, role: 'viewer' },
    { name: 'networks', href: '/networks', label: 'Networks', icon: Network, role: 'viewer' },
    { name: 'hardware', href: '/hardware', label: 'Hardware', icon: CircuitBoard, role: 'viewer' },
    { name: 'backups', href: '/backups', label: 'Backups', icon: Archive, role: 'viewer' },
    { name: 'alerts', href: '/alerts', label: 'Alerts', icon: Bell, role: 'viewer' },
    { name: 'activity', href: '/activity', label: 'Activity', icon: ClockArrowUp, role: 'admin' },
    { name: 'reports', href: '/reports', label: 'Reports', icon: FileText, role: 'viewer' },
  ] as const
  // Cluster: admins always (that is where hosts are added), others once there is one.
  let NAV = $derived([
    ...ALL_NAV.filter((n) => session.can(n.role)),
    ...(session.can('admin') || cluster.clustered
      ? [{ name: 'cluster', href: '/cluster', label: 'Cluster', icon: Server, role: 'viewer' } as const]
      : []),
  ])

  let domains = $derived(live.snapshot?.domains ?? [])
  let vmFilter = $state('')
  let filteredDomains = $derived(
    vmFilter.trim() ? domains.filter((d) => d.name.toLowerCase().includes(vmFilter.toLowerCase())) : domains,
  )
  let current = $derived(router.route.name === 'machine' ? router.route.uuid : null)


  const THEMES: { key: ThemePref; icon: typeof Sun; label: string }[] = [
    { key: 'light', icon: Sun, label: 'Light' },
    { key: 'system', icon: MonitorCog, label: 'Match system' },
    { key: 'dark', icon: Moon, label: 'Dark' },
  ]
</script>

<header class="mobile">
  <a class="m-brand" href="/" onclick={router.link}><img src="/favicon.svg" alt="VirtoMir" width="22" height="22" /></a>
  <nav class="m-nav" aria-label="Main">
    {#each NAV as n (n.name)}
      <a class:active={router.route.name === n.name} href={router.scoped(n.href)} onclick={router.link}>{n.label}</a>
    {/each}
    <a class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} aria-label="New machine"><Plus size={16} /></a>
  </nav>
</header>

<aside>
  <a class="brand" href="/" onclick={router.link}>
    <img src="/favicon.svg" alt="" width="22" height="22" />
    <span class="wordmark">VirtoMir</span>
  </a>

  <div class="host">
    <span class="host-dot {status}" title={STATUS_TEXT[status]}></span>
    <div class="host-text">
      <span class="host-name">{(cluster.clustered ? cluster.current?.name : null) ?? live.host?.hostname?.split('.')[0] ?? '…'}</span>
      <span class="host-status">{status === 'up' ? (cluster.clustered ? `${cluster.members.length} hosts in cluster` : 'Connected') : STATUS_TEXT[status]}</span>
    </div>
  </div>
  {#if cluster.clustered}
    <div class="hosts" role="radiogroup" aria-label="Shown host">
      {#each cluster.members.filter((m) => m.id !== cluster.current?.id) as m (m.id)}
        <button
          class="item other-host"
          role="radio"
          aria-checked="false"
          disabled={!m.reachable}
          title={m.reachable ? `Show ${m.name}` : (m.error ?? 'Not reachable')}
          onclick={() => cluster.open(m.id)}
        >
          <span class="mdot" class:on={m.reachable} class:crashed={!m.reachable}></span>
          <span class="mname">{m.name}</span>
        </button>
      {/each}
    </div>
  {/if}

  <nav aria-label="Main">
    {#each NAV as n (n.name)}
      {@const Icon = n.icon}
      <a
        class="item"
        class:active={router.route.name === n.name}
        href={router.scoped(n.href)}
        aria-current={router.route.name === n.name ? 'page' : undefined}
        onclick={router.link}
      >
        <Icon size={16} />
        <span class="label">{n.label}</span>
        {#if n.name === 'machines' && domains.length > 0}
          <span class="count">{domains.length}</span>
        {:else if n.name === 'alerts' && alerts.open > 0}
          <span class="count alert" class:crit={alerts.critical} title="{alerts.open} unacknowledged">{alerts.open}</span>
        {/if}
      </a>
    {/each}
  </nav>

  <div class="section">
    <span>Machines</span>
    {#if session.canCreate}
      <a class="add" class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} title="New machine" aria-label="New machine">
        <Plus size={14} />
      </a>
    {/if}
  </div>
  {#if domains.length > 6}
    <input class="filter" type="search" bind:value={vmFilter} placeholder="Filter" spellcheck="false" aria-label="Filter machines" />
  {/if}
  <div class="machines">
    {#each filteredDomains as d (d.uuid)}
      <a
        class="item machine"
        class:active={current === d.uuid}
        href={machineHref(d.uuid)}
        aria-current={current === d.uuid ? 'page' : undefined}
        onclick={router.link}
        title="{d.name}: {d.state}"
      >
        <span class="mdot" class:on={isActive(d.state)} class:paused={d.state === 'paused'} class:crashed={d.state === 'crashed'}></span>
        <span class="mname">{d.name}</span>
        {#if d.template}<span class="tag">template</span>{/if}
      </a>
    {:else}
      <span class="empty">No machines yet</span>
    {/each}
  </div>

  <footer>
    {#if session.info}
      <a class="who" class:active={router.route.name === 'account'} href="/account" onclick={router.link} title="Your account, API tokens and updates">
        <span class="avatar"><UserRound size={14} /></span>
        <span class="uname">{session.info.user ?? 'dev mode'}<small>{session.role}</small></span>
      </a>
      {#if session.info.user}
        <button class="icon" title="Sign out" aria-label="Sign out" onclick={() => session.logout()}><LogOut size={15} /></button>
      {/if}
    {/if}
    <div class="themes" role="radiogroup" aria-label="Theme">
      {#each THEMES as t (t.key)}
        {@const Icon = t.icon}
        <button role="radio" aria-checked={theme.pref === t.key} class:active={theme.pref === t.key} title={t.label} aria-label={t.label} onclick={() => theme.set(t.key)}>
          <Icon size={14} />
        </button>
      {/each}
    </div>
  </footer>
</aside>

<style>
  aside {
    position: sticky;
    top: 0;
    display: flex;
    flex-direction: column;
    flex: none;
    width: var(--sidebar-w);
    height: 100vh;
    padding: 14px 10px 10px;
    border-right: 1px solid var(--border);
    background: var(--bg);
    overflow: hidden;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 4px 8px 14px;
    color: var(--text);
    text-decoration: none;
  }
  .wordmark {
    font-size: 15px;
    font-weight: 650;
    letter-spacing: -0.01em;
  }
  .host {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 4px 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .host-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--idle);
  }
  .host-dot.up {
    background: var(--ok);
  }
  .host-dot.degraded {
    background: var(--warn);
  }
  .host-dot.down {
    background: var(--danger);
  }
  .host-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .host-name {
    overflow: hidden;
    font-size: 13px;
    font-weight: 600;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .host-status {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .hosts {
    display: flex;
    flex-direction: column;
    margin-bottom: 8px;
  }
  aside nav {
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 32px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-2);
    font-size: 13.5px;
    text-align: left;
    text-decoration: none;
    cursor: pointer;
    transition:
      background var(--dur-fast),
      color var(--dur-fast);
  }
  .item:hover:not(:disabled) {
    background: var(--surface-hover);
    color: var(--text);
  }
  .item.active {
    background: var(--surface-2);
    color: var(--text);
    font-weight: 500;
  }
  .item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .label {
    flex: 1;
  }
  .count {
    min-width: 20px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--surface-hover);
    color: var(--text-2);
    font-size: 11.5px;
    text-align: center;
  }
  .count.alert {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
  }
  .count.alert.crit {
    background: color-mix(in srgb, var(--danger) 18%, transparent);
    color: var(--danger);
  }
  .section {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 18px 10px 6px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 500;
  }
  .add {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: var(--radius-sm);
    color: var(--text-3);
  }
  .add:hover,
  .add.active {
    background: var(--surface-hover);
    color: var(--text);
  }
  .filter {
    margin: 0 4px 6px;
    height: 30px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  .filter:focus {
    border-color: var(--accent);
  }
  .machines {
    display: flex;
    flex: 1;
    flex-direction: column;
    gap: 1px;
    min-height: 0;
    overflow-y: auto;
  }
  .machine {
    min-height: 30px;
    font-size: 13px;
  }
  .mdot {
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--border-strong);
  }
  .mdot.on {
    background: var(--ok);
  }
  .mdot.paused {
    background: var(--warn);
  }
  .mdot.crashed {
    background: var(--danger);
  }
  .mname {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tag {
    color: var(--text-3);
    font-size: 11px;
  }
  .empty {
    padding: 4px 10px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  footer {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-top: 8px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
  }
  .who {
    display: flex;
    flex: 1;
    align-items: center;
    gap: 9px;
    min-width: 0;
    padding: 5px 6px;
    border-radius: var(--radius-sm);
    color: var(--text);
    text-decoration: none;
  }
  .who:hover,
  .who.active {
    background: var(--surface-hover);
  }
  .avatar {
    display: grid;
    flex: none;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 50%;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .uname {
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
    font-size: 13px;
    font-weight: 500;
    line-height: 1.2;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .uname small {
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 400;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-3);
    cursor: pointer;
  }
  .icon:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .themes {
    display: flex;
    padding: 2px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .themes button {
    display: grid;
    place-items: center;
    width: 24px;
    height: 22px;
    border: 0;
    border-radius: 4px;
    background: none;
    color: var(--text-3);
    cursor: pointer;
  }
  .themes button.active {
    background: var(--surface-hover);
    color: var(--text);
  }
  .mobile {
    display: none;
  }
  @media (max-width: 900px) {
    aside {
      display: none;
    }
    .mobile {
      position: sticky;
      top: 0;
      z-index: 20;
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 8px 12px;
      border-bottom: 1px solid var(--border);
      background: color-mix(in srgb, var(--bg) 92%, transparent);
      backdrop-filter: blur(8px);
    }
    .m-nav {
      display: flex;
      gap: 2px;
      overflow-x: auto;
    }
    .m-nav a {
      display: inline-flex;
      align-items: center;
      padding: 6px 10px;
      border-radius: var(--radius-sm);
      color: var(--text-2);
      font-size: 13.5px;
      text-decoration: none;
      white-space: nowrap;
    }
    .m-nav a.active {
      background: var(--surface-2);
      color: var(--text);
    }
  }
</style>
