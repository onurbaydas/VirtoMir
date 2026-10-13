<script lang="ts">
  // Picks the right console for a machine: graphical (VNC) or text (serial), and explains
  // what to do when the machine is off or has no console of that kind.
  import Monitor from '@lucide/svelte/icons/monitor'
  import SquareTerminal from '@lucide/svelte/icons/square-terminal'
  import Play from '@lucide/svelte/icons/play'
  import { api, errorText, type Domain } from '../api'
  import { isActive, live } from '../live.svelte'
  import { session } from '../session.svelte'
  import VncConsole from './VncConsole.svelte'
  import SerialConsole from './SerialConsole.svelte'

  interface Info {
    running: boolean
    vnc?: { password?: string }
    serial: string[]
  }
  let { d, standalone = false }: { d: Domain; standalone?: boolean } = $props()

  let info = $state<Info | null>(null)
  let error = $state('')
  let kind = $state<'vnc' | 'serial' | null>(null)

  let running = $derived(isActive(d.state) || d.state === 'paused')

  async function load() {
    try {
      const i = (await fetch(`/api/v1/domains/${encodeURIComponent(d.uuid)}/console/info`).then(async (r) => {
        if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? r.statusText)
        return r.json()
      })) as Info
      info = i
      error = ''
      if (!kind || (kind === 'vnc' && !i.vnc) || (kind === 'serial' && !i.serial.length)) kind = i.vnc ? 'vnc' : i.serial.length ? 'serial' : null
    } catch (e) {
      error = errorText(e)
    }
  }
  // Reload when the machine starts or stops (ports and consoles change).
  $effect(() => {
    void running
    void load()
  })
</script>

<div class="panel" class:standalone>
  {#if !standalone && info && (info.vnc || info.serial.length)}
    <div class="kinds" role="tablist" aria-label="Console type">
      <button role="tab" aria-selected={kind === 'vnc'} class:on={kind === 'vnc'} disabled={!info.vnc} onclick={() => (kind = 'vnc')}>
        <Monitor size={14} /> Screen
      </button>
      <button role="tab" aria-selected={kind === 'serial'} class:on={kind === 'serial'} disabled={!info.serial.length} onclick={() => (kind = 'serial')}>
        <SquareTerminal size={14} /> Text console
      </button>
    </div>
  {/if}

  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if !info}
    <div class="msg ui-panel"><span class="spin"></span></div>
  {:else if !running}
    <div class="msg ui-panel">
      <Monitor size={32} strokeWidth={1.5} />
      <strong>{d.name} is off</strong>
      <span>Start it to see its screen. The console connects by itself once it runs.</span>
      <button class="ui-btn primary" disabled={!session.can('operator')} title={session.why('operator')} onclick={() => live.act(d, 'start')}>
        <Play size={14} /> Start
      </button>
    </div>
  {:else if kind === 'vnc'}
    {#key d.uuid}
      <VncConsole uuid={d.uuid} name={d.name} password={info.vnc?.password} {standalone} />
    {/key}
  {:else if kind === 'serial'}
    <SerialConsole uuid={d.uuid} devices={info.serial} {standalone} />
  {:else}
    <div class="msg ui-panel">
      <strong>No console</strong>
      <span>This machine has neither a VNC display nor a serial console. Add one in the XML editor (a <span class="mono">&lt;graphics type='vnc'/&gt;</span> device).</span>
    </div>
  {/if}
</div>

<style>
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
    border-radius: 9px;
    background: var(--surface-2);
  }
  .kinds button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 12px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .kinds button.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
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
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
