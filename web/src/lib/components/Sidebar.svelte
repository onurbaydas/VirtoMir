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
  import { cluster } from '../cluster.svelte'
  import { alerts } from '../alerts.svelte'
  import { session } from '../session.svelte'
  import { isActive, live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { theme, type ThemePref } from '../theme.svelte'

  let { version }: { version: string } = $props()

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
    { name: 'storage', href: '/storage', label: 'Storage', icon: HardDrive, role: 'viewer' },
    { name: 'networks', href: '/networks', label: 'Networks', icon: Network, role: 'viewer' },
    { name: 'backups', href: '/backups', label: 'Backups', icon: Archive, role: 'viewer' },
    { name: 'alerts', href: '/alerts', label: 'Alerts', icon: Bell, role: 'viewer' },
    { name: 'activity', href: '/activity', label: 'Activity', icon: ClockArrowUp, role: 'admin' },
  ] as const
  // Cluster: admins always (that is where hosts are added), others once there is one.
  let NAV = $derived([
    ...ALL_NAV.filter((n) => session.can(n.role)),
    ...(session.can('admin') || cluster.clustered
      ? [{ name: 'cluster', href: '/cluster', label: 'Cluster', icon: Boxes, role: 'viewer' } as const]
      : []),
  ])

  let domains = $derived(live.snapshot?.domains ?? [])
  let current = $derived(router.route.name === 'machine' ? router.route.uuid : null)

  const THEMES: { key: ThemePref; icon: typeof Sun; label: string }[] = [
    { key: 'light', icon: Sun, label: 'Light' },
    { key: 'system', icon: MonitorCog, label: 'Match system' },
    { key: 'dark', icon: Moon, label: 'Dark' },
  ]
</script>

<header class="mobile">
  <a class="m-brand" href="/" onclick={router.link}><img src="/favicon.svg" alt="VirtoMir" width="24" height="24" /></a>
  <nav class="m-nav" aria-label="Main">
    {#each NAV as n (n.name)}
      <a class:active={router.route.name === n.name} href={router.scoped(n.href)} onclick={router.link}>{n.label}</a>
    {/each}
    <a class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} aria-label="New machine"><Plus size={16} /></a>
  </nav>
</header>

<aside>
  <div class="brand">
    <img src="/favicon.svg" alt="" width="28" height="28" />
    <div>
      <div class="wordmark">VirtoMir</div>
      <div class="tagline">Control Deck</div>
    </div>
  </div>

  <nav>
    <div class="section">{cluster.clustered ? 'Hosts' : 'Host'}</div>
    <div class="host">
      <span class="host-icon"><Server size={16} /></span>
      <div class="host-text">
        <span class="host-name">{(cluster.clustered ? cluster.current?.name : null) ?? live.host?.hostname ?? '…'}</span>
        <span class="host-status {status}"><span class="dot"></span>{STATUS_TEXT[status]}</span>
      </div>
    </div>
    {#if cluster.clustered}
      <div class="hosts" role="radiogroup" aria-label="Shown host">
        {#each cluster.members.filter((m) => m.id !== cluster.current?.id) as m (m.id)}
          <button
            class="nav-item other-host"
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
    {#each NAV as n (n.name)}
      {@const Icon = n.icon}
      <a
        class="nav-item"
        class:active={router.route.name === n.name}
        href={router.scoped(n.href)}
        aria-current={router.route.name === n.name ? 'page' : undefined}
        onclick={router.link}
      >
        <Icon size={16} />
        {n.label}
        {#if n.name === 'alerts' && alerts.open > 0}
          <span class="badge" class:crit={alerts.critical} title="{alerts.open} unacknowledged">{alerts.open}</span>
        {/if}
      </a>
    {/each}

    <div class="section machines-head">
      Machines
      {#if session.can('admin')}
        <a class="add" class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} title="New machine" aria-label="New machine">
          <Plus size={14} />
        </a>
      {/if}
    </div>
    {#if domains.length}
      <div class="machines">
        {#each domains as d (d.uuid)}
          <a
            class="nav-item machine"
            class:active={current === d.uuid}
            href={machineHref(d.uuid)}
            aria-current={current === d.uuid ? 'page' : undefined}
            onclick={router.link}
            title="{d.name}: {d.state}"
          >
            <span
              class="mdot"
              class:on={isActive(d.state)}
              class:paused={d.state === 'paused'}
              class:crashed={d.state === 'crashed'}
            ></span>
            <span class="mname">{d.name}</span>
          </a>
        {/each}
      </div>
    {/if}
  </nav>

  {#if session.info}
    <div class="user">
      <a class="who" class:active={router.route.name === 'account'} href="/account" onclick={router.link} title="Your role and API tokens">
        <span class="avatar"><UserRound size={14} /></span>
        <span class="uname">{session.info.user ?? 'dev mode'}<small>{session.role}</small></span>
      </a>
      {#if session.info.user}
        <button class="logout" title="Sign out" aria-label="Sign out" onclick={() => session.logout()}><LogOut size={15} /></button>
      {/if}
    </div>
  {/if}
  <footer>
    <div class="themes" role="radiogroup" aria-label="Theme">
      {#each THEMES as t (t.key)}
        {@const Icon = t.icon}
        <button
          role="radio"
          aria-checked={theme.pref === t.key}
          class:active={theme.pref === t.key}
          title={t.label}
          aria-label={t.label}
          onclick={() => theme.set(t.key)}
        >
          <Icon size={15} />
        </button>
      {/each}
    </div>
    <span class="version mono" title="virtomird build">{version}</span>
  </footer>
</aside>

<style>
  aside {
    position: sticky;
    top: 0;
    display: flex;
    flex-direction: column;
    width: var(--sidebar-w);
    height: 100vh;
    padding: 18px 14px;
    background: var(--bg-elev);
    border-right: 1px solid var(--border);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 0 6px 22px;
  }
  .wordmark {
    font-size: 16px;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .tagline {
    font-size: 11.5px;
    color: var(--text-3);
    letter-spacing: 0.02em;
  }
  .section {
    padding: 0 8px 8px;
    color: var(--text-3);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .host {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 8px;
    margin-bottom: 6px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .host-icon {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border-radius: 8px;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .host-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .host-name {
    font-weight: 600;
    font-size: 13.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .host-status {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--text-3);
  }
  .host-status .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--idle);
  }
  .host-status.up .dot {
    background: var(--ok);
  }
  .host-status.degraded .dot {
    background: var(--warn);
  }
  .host-status.down .dot {
    background: var(--danger);
  }
  .hosts {
    display: flex;
    flex-direction: column;
    margin: -2px 0 8px;
  }
  .other-host {
    height: 30px;
    border: none;
    background: none;
    font: inherit;
    font-size: 13px;
    cursor: pointer;
    text-align: left;
  }
  .other-host:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 36px;
    padding: 0 10px;
    border-radius: 8px;
    color: var(--text-2);
    text-decoration: none;
    font-weight: 500;
  }
  .nav-item:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .nav-item.active {
    background: var(--accent-soft);
    color: var(--accent);
  }
  aside nav {
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1;
  }
  .badge {
    margin-left: auto;
    min-width: 20px;
    height: 20px;
    padding: 0 6px;
    border-radius: 999px;
    background: var(--warn);
    color: #1a1204;
    font-size: 11.5px;
    font-weight: 700;
    line-height: 20px;
    text-align: center;
  }
  .badge.crit {
    background: var(--danger);
    color: #fff;
  }
  .machines-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 18px;
  }
  .add {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 6px;
    color: var(--text-3);
  }
  .add:hover,
  .add.active {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .user {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 8px 6px;
    margin-bottom: 8px;
    border-top: 1px solid var(--border);
  }
  .user .avatar {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 50%;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 4px;
    margin: -4px;
    border-radius: 8px;
    color: inherit;
    text-decoration: none;
  }
  .who:hover,
  .who.active {
    background: var(--surface-hover);
  }
  .uname {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 13px;
    font-weight: 500;
  }
  .uname small {
    color: var(--text-3);
    font-size: 11px;
    font-weight: 400;
  }
  .logout {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
  }
  .logout:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .mobile {
    display: none;
  }
  .machines {
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-y: auto;
    min-height: 0;
    margin: 0 -4px;
    padding: 0 4px 12px;
  }
  .machine {
    height: 32px;
    font-size: 13px;
  }
  .mdot {
    flex: none;
    width: 8px;
    height: 8px;
    margin: 0 4px;
    border-radius: 50%;
    border: 1.5px solid var(--idle);
  }
  .mdot.on {
    background: var(--ok);
    border-color: var(--ok);
  }
  .mdot.paused {
    background: var(--warn);
    border-color: var(--warn);
  }
  .mdot.crashed {
    background: var(--danger);
    border-color: var(--danger);
  }
  .mname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  footer {
    margin-top: auto;
    padding-top: 12px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 4px;
  }
  .themes {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
  }
  .themes button {
    display: grid;
    place-items: center;
    width: 28px;
    height: 26px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
  }
  .themes button:hover {
    color: var(--text);
  }
  .themes button.active {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .version {
    font-size: 11px;
    color: var(--text-3);
  }

  @media (max-width: 900px) {
    aside {
      display: none;
    }
    .mobile {
      position: sticky;
      top: 0;
      z-index: 30;
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 10px 16px;
      background: var(--bg-elev);
      border-bottom: 1px solid var(--border);
    }
    .m-brand {
      display: grid;
    }
    .m-nav {
      display: flex;
      gap: 2px;
      overflow-x: auto;
    }
    .m-nav a {
      display: grid;
      place-items: center;
      height: 30px;
      padding: 0 10px;
      border-radius: 7px;
      color: var(--text-2);
      font-size: 13px;
      font-weight: 500;
      text-decoration: none;
      white-space: nowrap;
    }
    .m-nav a.active {
      background: var(--accent-soft);
      color: var(--accent);
    }
  }
</style>
