<script lang="ts">
  import Server from '@lucide/svelte/icons/server'
  import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard'
  import Sun from '@lucide/svelte/icons/sun'
  import Moon from '@lucide/svelte/icons/moon'
  import MonitorCog from '@lucide/svelte/icons/monitor-cog'
  import { live } from '../live.svelte'
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

  const THEMES: { key: ThemePref; icon: typeof Sun; label: string }[] = [
    { key: 'light', icon: Sun, label: 'Light' },
    { key: 'system', icon: MonitorCog, label: 'Match system' },
    { key: 'dark', icon: Moon, label: 'Dark' },
  ]
</script>

<aside>
  <div class="brand">
    <img src="/favicon.svg" alt="" width="28" height="28" />
    <div>
      <div class="wordmark">VirtoMir</div>
      <div class="tagline">Control Deck</div>
    </div>
  </div>

  <nav>
    <div class="section">Host</div>
    <div class="host">
      <span class="host-icon"><Server size={16} /></span>
      <div class="host-text">
        <span class="host-name">{live.host?.hostname ?? '…'}</span>
        <span class="host-status {status}"><span class="dot"></span>{STATUS_TEXT[status]}</span>
      </div>
    </div>
    <a class="nav-item active" href="/" aria-current="page">
      <LayoutDashboard size={16} />
      Overview
    </a>
  </nav>

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
  footer {
    margin-top: auto;
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
  }
</style>
