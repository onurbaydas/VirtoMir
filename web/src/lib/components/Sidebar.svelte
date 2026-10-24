<script lang="ts">
  import { t, lang, LANGS, setLang } from '../i18n.svelte'
  import Server from '@lucide/svelte/icons/server'
  import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard'
  import Sun from '@lucide/svelte/icons/sun'
  import Moon from '@lucide/svelte/icons/moon'
  import MonitorCog from '@lucide/svelte/icons/monitor-cog'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Database from '@lucide/svelte/icons/database'
  import Network from '@lucide/svelte/icons/network'
  import Waypoints from '@lucide/svelte/icons/waypoints'
  import RouterIcon from '@lucide/svelte/icons/router'
  import Plus from '@lucide/svelte/icons/plus'
  import LogOut from '@lucide/svelte/icons/log-out'
  import UserRound from '@lucide/svelte/icons/user-round'
  import ClockArrowUp from '@lucide/svelte/icons/clock-arrow-up'
  import Bell from '@lucide/svelte/icons/bell'
  import Archive from '@lucide/svelte/icons/archive'
  import Boxes from '@lucide/svelte/icons/boxes'
  import CircuitBoard from '@lucide/svelte/icons/circuit-board'
  import FileText from '@lucide/svelte/icons/file-text'
  import TrendingUp from '@lucide/svelte/icons/trending-up'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import FolderKanban from '@lucide/svelte/icons/folder-kanban'
  import ShoppingBag from '@lucide/svelte/icons/shopping-bag'
  import BookOpen from '@lucide/svelte/icons/book-open'
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
    down: t('VirtoMir unreachable'),
    pending: 'Connecting…',
  }

  const ALL_NAV = [
    { name: 'overview', href: '/', label: t('Overview'), icon: LayoutDashboard, role: 'viewer' },
    { name: 'machines', href: '/machines', label: t('Machines'), icon: Boxes, role: 'viewer' },
    { name: 'storage', href: '/storage', label: t('Storage'), icon: HardDrive, role: 'viewer' },
    { name: 'ceph', href: '/ceph', label: t('Ceph'), icon: Database, role: 'viewer' },
    { name: 'library', href: '/library', label: t('Content Library'), icon: BookOpen, role: 'viewer' },
    { name: 'networks', href: '/networks', label: t('Networks'), icon: Network, role: 'viewer' },
    { name: 'sdn', href: '/sdn', label: t('SDN'), icon: Waypoints, role: 'viewer' },
    { name: 'physnet', href: '/physnet', label: t('Physical network'), icon: RouterIcon, role: 'viewer' },
    { name: 'hardware', href: '/hardware', label: t('Hardware'), icon: CircuitBoard, role: 'viewer' },
    { name: 'backups', href: '/backups', label: t('Backups'), icon: Archive, role: 'viewer' },
    { name: 'alerts', href: '/alerts', label: t('Alerts'), icon: Bell, role: 'viewer' },
    { name: 'activity', href: '/activity', label: t('Activity'), icon: ClockArrowUp, role: 'admin' },
    { name: 'projects', href: '/projects', label: t('Projects'), icon: FolderKanban, role: 'admin' },
    { name: 'catalog', href: '/catalog', label: t('Catalog'), icon: ShoppingBag, role: 'viewer' },
    { name: 'insights', href: '/insights', label: t('Insights'), icon: TrendingUp, role: 'viewer' },
    { name: 'reports', href: '/reports', label: t('Reports'), icon: FileText, role: 'viewer' },
    { name: 'compliance', href: '/compliance', label: t('Compliance'), icon: ShieldCheck, role: 'viewer' },
  ] as const
  // Cluster: admins always (that is where hosts are added), others once there is one.
  let NAV = $derived([
    ...ALL_NAV.filter((n) => session.can(n.role)),
    ...(session.can('admin') || cluster.clustered
      ? [{ name: 'cluster', href: '/cluster', label: t('Cluster'), icon: Server, role: 'viewer' } as const]
      : []),
  ])

  let domains = $derived(live.snapshot?.domains ?? [])
  let vmFilter = $state('')
  let filteredDomains = $derived(
    vmFilter.trim() ? domains.filter((d) => d.name.toLowerCase().includes(vmFilter.toLowerCase())) : domains,
  )
  let current = $derived(router.route.name === 'machine' ? router.route.uuid : null)


  const THEMES: { key: ThemePref; icon: typeof Sun; label: string }[] = [
    { key: 'light', icon: Sun, label: t('Light') },
    { key: 'system', icon: MonitorCog, label: t('Match system') },
    { key: 'dark', icon: Moon, label: t('Dark') },
  ]
</script>

<header class="mobile">
  <a class="m-brand" href="/" onclick={router.link}><img src={import.meta.env.BASE_URL + 'favicon.svg'} alt="VirtoMir" width="22" height="22" /></a>
  <nav class="m-nav" aria-label={t('Main')}>
    {#each NAV as n (n.name)}
      <a class:active={router.route.name === n.name} href={router.scoped(n.href)} onclick={router.link}>{n.label}</a>
    {/each}
    <a class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} aria-label={t('New machine')}><Plus size={16} /></a>
  </nav>
</header>

<aside>
  <a class="brand" href="/" onclick={router.link}>
    <img src={import.meta.env.BASE_URL + 'favicon.svg'} alt="" width="26" height="26" />
    <span class="wordmark">VirtoMir</span>
  </a>

  <div class="host">
    <span class="host-dot {status}" title={STATUS_TEXT[status]}></span>
    <div class="host-text">
      <span class="host-name">{(cluster.clustered ? cluster.current?.name : null) ?? live.host?.hostname?.split('.')[0] ?? '…'}</span>
      <span class="host-status">{status === 'up' ? (cluster.clustered ? t('{length} hosts in cluster', { length: cluster.members.length }) : 'Connected') : STATUS_TEXT[status]}</span>
    </div>
  </div>
  {#if cluster.clustered}
    <div class="hosts" role="radiogroup" aria-label={t('Shown host')}>
      {#each cluster.members.filter((m) => m.id !== cluster.current?.id) as m (m.id)}
        <button
          class="item other-host"
          role="radio"
          aria-checked="false"
          disabled={!m.reachable}
          title={m.reachable ? `Show ${m.name}` : (m.error ?? t('Not reachable'))}
          onclick={() => cluster.open(m.id)}
        >
          <span class="mdot" class:on={m.reachable} class:crashed={!m.reachable}></span>
          <span class="mname">{m.name}</span>
        </button>
      {/each}
    </div>
  {/if}

  <nav aria-label={t('Main')}>
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
          <span class="count alert" class:crit={alerts.critical} title={t('{open} unacknowledged', { open: alerts.open })}>{alerts.open}</span>
        {/if}
      </a>
    {/each}
  </nav>

  <div class="section">
    <span>{t('Machines')}</span>
    {#if session.canCreate}
      <a class="add" class:active={router.route.name === 'new'} href="/machines/new" onclick={router.link} title={t('New machine')} aria-label={t('New machine')}>
        <Plus size={14} />
      </a>
    {/if}
  </div>
  {#if domains.length > 6}
    <input class="filter" type="search" bind:value={vmFilter} placeholder={t('Filter')} spellcheck="false" aria-label={t('Filter machines')} />
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
        {#if d.template}<span class="tag">{t('template')}</span>{/if}
      </a>
    {:else}
      <span class="empty">{t('No machines yet')}</span>
    {/each}
  </div>

  <footer>
    {#if session.info}
      <a class="who" class:active={router.route.name === 'account'} href="/account" onclick={router.link} title={t('Your account, API tokens and updates')}>
        <span class="avatar"><UserRound size={14} /></span>
        <span class="uname">{session.info.user ?? 'dev mode'}<small>{session.role}</small></span>
      </a>
      {#if session.info.user}
        <button class="icon" title={t('Sign out')} aria-label={t('Sign out')} onclick={() => session.logout()}><LogOut size={15} /></button>
      {/if}
    {/if}
    <div class="themes" role="radiogroup" aria-label={t('Theme')}>
      {#each THEMES as t (t.key)}
        {@const Icon = t.icon}
        <button role="radio" aria-checked={theme.pref === t.key} class:active={theme.pref === t.key} title={t.label} aria-label={t.label} onclick={() => theme.set(t.key)}>
          <Icon size={14} />
        </button>
      {/each}
    </div>
    <div class="themes langs" role="radiogroup" aria-label="Language / Dil">
      {#each LANGS as l (l.id)}
        <button role="radio" aria-checked={lang === l.id} class:active={lang === l.id} title={l.label} onclick={() => lang !== l.id && setLang(l.id)}>{l.id.toUpperCase()}</button>
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
    border-right: var(--frame) solid var(--ink);
    background: var(--surface);
    overflow: hidden;
  }
  .brand {
    position: relative;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 14px 16px;
    border-bottom: var(--frame) solid var(--ink);
    background: #000;
    color: #f4f1e8;
    text-decoration: none;
    isolation: isolate;
    overflow: hidden;
  }
  /* A painted stroke behind the wordmark (Kline). */
  .brand::after {
    content: '';
    position: absolute;
    z-index: -1;
    right: -30px;
    bottom: 4px;
    width: 150px;
    height: 16px;
    background: var(--cadmium);
    -webkit-mask: var(--brush-stroke) no-repeat center / 100% 100%;
    mask: var(--brush-stroke) no-repeat center / 100% 100%;
  }
  .brand img {
    border: 2px solid #f4f1e8;
  }
  .wordmark {
    font-family: var(--font-display);
    font-size: 24px;
    font-stretch: 62%;
    font-weight: 900;
    line-height: 1;
    letter-spacing: 0.01em;
  }
  .host {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 12px 10px 8px;
    padding: 7px 9px;
    border: var(--frame) solid var(--ink);
    background: var(--bg-elev);
    box-shadow: var(--bevel-in);
  }
  .host-dot {
    flex: none;
    width: 10px;
    height: 10px;
    border: 1px solid var(--ink);
    background: var(--idle);
  }
  .host-dot.up {
    background: var(--ok);
  }
  .host-dot.degraded {
    background: var(--ochre);
  }
  .host-dot.down {
    background: var(--cadmium);
  }
  .host-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .host-name {
    overflow: hidden;
    font-family: var(--font-mono);
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
    margin: 0 10px 8px;
  }
  aside nav {
    display: flex;
    flex-direction: column;
    padding: 2px 10px;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 30px;
    padding: 0 8px;
    border: 0;
    background: none;
    color: var(--text);
    font-family: var(--font-display);
    font-size: 15px;
    font-stretch: 80%;
    font-weight: 600;
    text-align: left;
    text-decoration: none;
    cursor: pointer;
  }
  .item:hover:not(:disabled) {
    text-decoration: underline;
    text-decoration-thickness: 2px;
    text-underline-offset: 3px;
  }
  .item.active {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .item.active:hover {
    text-decoration: none;
  }
  .item:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .label {
    flex: 1;
  }
  .count {
    min-width: 22px;
    padding: 0 5px;
    border: 1px solid currentColor;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    text-align: center;
  }
  .count.alert {
    border-color: var(--ink);
    background: var(--ochre);
    color: #000;
  }
  .count.alert.crit {
    background: var(--cadmium);
    color: #fff;
  }
  .section {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 16px 10px 4px;
    padding: 0 8px 3px;
    border-bottom: var(--frame) solid var(--ink);
    font-family: var(--font-display);
    font-size: 13px;
    font-stretch: 75%;
    font-weight: 800;
  }
  .add {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border: 1px solid var(--ink);
    background: var(--surface);
    box-shadow: var(--bevel);
    color: var(--text);
  }
  .add:hover,
  .add.active {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .filter {
    margin: 4px 10px 6px;
    height: 28px;
    padding: 0 8px;
    border: var(--frame) solid var(--ink);
    background: var(--bg-elev);
    box-shadow: var(--bevel-in);
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  .machines {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
    padding: 2px 10px;
    overflow-y: auto;
  }
  .machine {
    min-height: 26px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    font-stretch: 100%;
    font-weight: 500;
  }
  .mdot {
    flex: none;
    width: 8px;
    height: 8px;
    border: 1px solid var(--ink);
    background: var(--bg-elev);
  }
  .mdot.on {
    background: var(--ok);
  }
  .mdot.paused {
    background: var(--ochre);
  }
  .mdot.crashed {
    background: var(--cadmium);
  }
  .mname {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tag {
    padding: 0 4px;
    border: 1px solid currentColor;
    font-size: 10px;
  }
  .empty {
    padding: 4px 8px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  footer {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    border-top: var(--frame) solid var(--ink);
    background: var(--surface-2);
  }
  .who {
    display: flex;
    flex: 1;
    align-items: center;
    gap: 8px;
    min-width: 120px; /* the theme and language switches wrap below instead of squeezing the name */
    padding: 3px 4px;
    color: var(--text);
    text-decoration: none;
  }
  .who:hover,
  .who.active {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .avatar {
    display: grid;
    flex: none;
    place-items: center;
    width: 26px;
    height: 26px;
    border: var(--frame) solid var(--ink);
    background: var(--ochre);
    color: #000;
  }
  .uname {
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
    font-weight: 700;
    font-size: 13px;
    line-height: 1.15;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .uname small {
    font-weight: 400;
    font-size: 11.5px;
    opacity: 0.75;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: var(--frame) solid var(--ink);
    background: var(--surface);
    box-shadow: var(--bevel);
    color: var(--text);
    cursor: pointer;
  }
  .icon:active {
    box-shadow: var(--bevel-in);
  }
  .themes {
    display: flex;
    border: var(--frame) solid var(--ink);
    background: var(--bg-elev);
  }
  .themes button {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border: 0;
    background: none;
    color: var(--text-2);
    cursor: pointer;
  }
  .langs {
    margin-left: auto;
  }
  .langs button {
    width: 26px;
    font: 600 10.5px var(--font-mono, monospace);
  }
  .themes button.active {
    background: var(--invert-bg);
    color: var(--invert-fg);
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
      border-bottom: var(--frame) solid var(--ink);
      background: var(--surface);
    }
    .m-nav {
      display: flex;
      gap: 4px;
      overflow-x: auto;
    }
    .m-nav a {
      display: inline-flex;
      align-items: center;
      padding: 5px 9px;
      color: var(--text);
      font-family: var(--font-display);
      font-size: 15px;
      font-stretch: 80%;
      font-weight: 600;
      text-decoration: none;
      white-space: nowrap;
    }
    .m-nav a.active {
      background: var(--invert-bg);
      color: var(--invert-fg);
    }
  }
</style>
