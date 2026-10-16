<script lang="ts">
  import { t } from '../i18n.svelte'
  // Picks the right console for a machine: graphical (VNC or SPICE) or text (serial), and explains
  // what to do when the machine is off or has no console of that kind.
  import Monitor from '@lucide/svelte/icons/monitor'
  import SquareTerminal from '@lucide/svelte/icons/square-terminal'
  import Play from '@lucide/svelte/icons/play'
  import { api, errorText, scoped, type Domain } from '../api'
  import { isActive, live } from '../live.svelte'
  import { session } from '../session.svelte'
  import VncConsole from './VncConsole.svelte'
  import SerialConsole from './SerialConsole.svelte'
  import SpiceConsole from './SpiceConsole.svelte'
  import { toasts } from '../toast.svelte'

  interface Info {
    running: boolean
    vnc?: { password?: string }
    serial: string[]
    /** Monitors the screen console can show (extra ones need a restart after a change). */
    heads?: number
    spice?: { password?: string }
    /** Protocol in the definition: what the next start uses. */
    display: '' | 'vnc' | 'spice'
  }
  let { d, standalone = false, head: firstHead = 0 }: { d: Domain; standalone?: boolean; head?: number } = $props()
  // svelte-ignore state_referenced_locally
  let head = $state(firstHead)

  let info = $state<Info | null>(null)
  let error = $state('')
  let kind = $state<'vnc' | 'serial' | null>(null)

  let running = $derived(isActive(d.state) || d.state === 'paused')

  async function load() {
    try {
      const i = (await fetch(scoped(`/api/v1/domains/${encodeURIComponent(d.uuid)}/console/info`)).then(async (r) => {
        if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? r.statusText)
        return r.json()
      })) as Info
      info = i
      error = ''
      if (head >= (i.heads ?? 1)) head = 0
      const screen = i.vnc || i.spice
      if (!kind || (kind === 'vnc' && !screen) || (kind === 'serial' && !i.serial.length)) kind = screen ? 'vnc' : i.serial.length ? 'serial' : null
    } catch (e) {
      error = errorText(e)
    }
  }
  // VNC ⇄ SPICE (ADR-0046): takes effect at the next start.
  let switching = $state(false)
  async function switchDisplay(to: 'vnc' | 'spice') {
    switching = true
    try {
      await api.put(`/api/v1/domains/${encodeURIComponent(d.uuid)}/display`, { protocol: to })
      toasts.push({ kind: 'info', title: t('{name} uses {v} from its next start', { name: d.name, v: to.toUpperCase() }), detail: running ? 'Shut it down and start it again (a reboot inside the guest is not enough).' : undefined })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Display not changed'), detail: errorText(e) })
    } finally {
      switching = false
    }
  }
  let liveProto = $derived(info?.spice ? 'spice' : info?.vnc ? 'vnc' : '')

  // Reload when the machine starts or stops (ports and consoles change).
  $effect(() => {
    void running
    void load()
  })
</script>

<div class="panel" class:standalone>
  {#if !standalone && info && (info.vnc || info.spice || info.serial.length)}
    <div class="kinds" role="tablist" aria-label={t('Console type')}>
      <button role="tab" aria-selected={kind === 'vnc'} class:on={kind === 'vnc'} disabled={!info.vnc && !info.spice} onclick={() => (kind = 'vnc')}>
        <Monitor size={14} /> {t('Screen')}
      </button>
      <button role="tab" aria-selected={kind === 'serial'} class:on={kind === 'serial'} disabled={!info.serial.length} onclick={() => (kind = 'serial')}>
        <SquareTerminal size={14} /> {t('Text console')}
      </button>
    </div>
    {#if kind === 'vnc' && !info.spice && (info.heads ?? 1) > 1}
      <div class="kinds" role="tablist" aria-label={t('Monitor')}>
        {#each Array.from({ length: info.heads ?? 1 }, (_, i) => i) as h (h)}
          <button role="tab" aria-selected={head === h} class:on={head === h} onclick={() => (head = h)}>Monitor {h + 1}</button>
        {/each}
      </div>
    {/if}
  {/if}

  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if !info}
    <div class="msg ui-panel"><span class="spin"></span></div>
  {:else if !running}
    <div class="msg ui-panel">
      <Monitor size={32} strokeWidth={1.5} />
      <strong>{d.name} is off</strong>
      <span>{t('Start it to see its screen. The console connects by itself once it runs.')}</span>
      <button class="ui-btn primary" disabled={!session.can('operator')} title={session.why('operator')} onclick={() => live.act(d, 'start')}>
        <Play size={14} /> {t('Start')}
      </button>
    </div>
  {:else if kind === 'vnc' && info.spice}
    {#key d.uuid}
      <SpiceConsole uuid={d.uuid} name={d.name} password={info.spice.password} {standalone} />
    {/key}
  {:else if kind === 'vnc'}
    {#key `${d.uuid}/${head}`}
      <VncConsole uuid={d.uuid} name={d.name} password={info.vnc?.password} {head} {standalone} />
    {/key}
  {:else if kind === 'serial'}
    <SerialConsole uuid={d.uuid} devices={info.serial} {standalone} />
  {:else}
    <div class="msg ui-panel">
      <strong>{t('No console')}</strong>
      <span>{t('This machine has neither a VNC display nor a serial console. Add one in the XML editor (a')} <span class="mono">&lt;graphics type='vnc'/&gt;</span> {t('device).')}</span>
    </div>
  {/if}
  {#if !standalone && info?.display && session.can('admin')}
    <p class="proto">
      {t('Screen over')} <b>{(liveProto || info.display).toUpperCase()}</b>{liveProto && info.display !== liveProto ? t(', {v} from the next start', { v: info.display.toUpperCase() }) : ''}.
      {#if info.display === 'vnc'}
        <button class="link" disabled={switching} onclick={() => switchDisplay('spice')}>{t('Use SPICE')}</button> {t('— the guest\'s resolution follows the window and the clipboard works with spice-vdagent.')}
      {:else}
        <button class="link" disabled={switching} onclick={() => switchDisplay('vnc')}>{t('Use VNC')}</button> {t('— works without any guest agent, with keyboard layouts and several monitors.')}
      {/if}
    </p>
  {/if}
</div>

<style>
  .proto {
    margin: 0;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
  }
  .panel {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .panel.standalone {
    gap: 0;
  }
  .kinds {
    display: inline-flex;
    align-self: flex-start;
    gap: 2px;
    padding: 3px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .kinds button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 12px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .kinds button.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--bevel);
  }
  .kinds button:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .msg {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 64px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .msg strong {
    color: var(--text);
    font-size: 15px;
  }
  .spin {
    width: 22px;
    height: 22px;
    border: 2.5px solid var(--accent);
    border-right-color: transparent;
    border-radius: var(--radius-sm);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
