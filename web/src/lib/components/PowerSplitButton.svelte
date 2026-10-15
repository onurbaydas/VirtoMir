<script lang="ts">
  import { onMount } from 'svelte'
  import Play from '@lucide/svelte/icons/play'
  import Power from '@lucide/svelte/icons/power'
  import RotateCw from '@lucide/svelte/icons/rotate-cw'
  import Pause from '@lucide/svelte/icons/pause'
  import Zap from '@lucide/svelte/icons/zap'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import HardDriveDownload from '@lucide/svelte/icons/hard-drive-download'
  import type { Domain } from '../api'
  import { isActive, live } from '../live.svelte'
  import { session } from '../session.svelte'
  import DestructiveGuardrailModal from './DestructiveGuardrailModal.svelte'
  import { toasts } from '../toast.svelte'

  interface Props {
    d: Domain
    size?: 'md' | 'lg'
  }
  let { d, size = 'lg' }: Props = $props()

  let open = $state(false)
  let menuRef = $state<HTMLDivElement>()
  let guardrailOpen = $state(false)
  let guardrailAction = $state<'force-off' | 'hard-reset'>('force-off')

  let active = $derived(isActive(d.state))
  let paused = $derived(d.state === 'paused')
  let pending = $derived(live.pending[d.uuid])

  function isSafeMode(): boolean {
    return typeof document !== 'undefined' && document.documentElement.dataset.safemode === 'true'
  }

  function toggleMenu(e: MouseEvent) {
    e.stopPropagation()
    open = !open
  }

  function closeMenu() {
    open = false
  }

  onMount(() => {
    function onDocClick(e: MouseEvent) {
      if (menuRef && !menuRef.contains(e.target as Node)) {
        open = false
      }
    }
    window.addEventListener('click', onDocClick)
    return () => window.removeEventListener('click', onDocClick)
  })

  function handlePrimary() {
    closeMenu()
    if (!session.can('operator')) return
    if (active) {
      live.act(d, 'shutdown')
    } else if (paused) {
      live.act(d, 'resume')
    } else {
      live.act(d, 'start')
    }
  }

  function handleAction(act: 'shutdown' | 'reboot' | 'pause' | 'force-off' | 'hard-reset' | 'hibernate') {
    closeMenu()
    if (!session.can('operator')) return

    if (act === 'force-off' || act === 'hard-reset' || (isSafeMode() && (act === 'shutdown' || act === 'reboot'))) {
      guardrailAction = act === 'hard-reset' ? 'hard-reset' : 'force-off'
      guardrailOpen = true
      return
    }

    if (act === 'hibernate') {
      toasts.push({ kind: 'info', title: 'Hibernate state dump initiated', detail: `Saving RAM snapshot for ${d.name}...` })
      live.act(d, 'pause')
      return
    }

    live.act(d, act)
  }

  function onGuardrailConfirm() {
    guardrailOpen = false
    if (guardrailAction === 'force-off') {
      live.act(d, 'force-off')
    } else {
      live.act(d, 'reboot')
    }
  }
</script>

<div class="power-split-wrap {size}" bind:this={menuRef}>
  <div class="button-group">
    <!-- Main Power Action -->
    <button
      class="main-act-btn"
      class:accent={!active}
      class:running={active}
      disabled={!!pending || !session.can('operator')}
      title={session.why('operator')}
      onclick={handlePrimary}
    >
      {#if active}
        <Power size={size === 'lg' ? 15 : 13} />
        <span>ACPI SHUTDOWN</span>
      {:else if paused}
        <Play size={size === 'lg' ? 15 : 13} />
        <span>RESUME</span>
      {:else}
        <Play size={size === 'lg' ? 15 : 13} />
        <span>START MACHINE</span>
      {/if}
    </button>

    <!-- Split Chevron Trigger -->
    <button
      class="split-chev-btn"
      class:accent={!active}
      class:running={active}
      class:menu-open={open}
      disabled={!!pending || !session.can('operator')}
      title="Advanced Power Control Options"
      onclick={toggleMenu}
      aria-label="Power menu options"
    >
      <ChevronDown size={14} />
    </button>
  </div>

  <!-- Neo-Brutalist Dropdown Menu -->
  {#if open}
    <div class="power-dropdown mono">
      <div class="dropdown-header">
        <span>POWER ENGINE CONTROL</span>
      </div>

      {#if active}
        <button class="menu-item" onclick={() => handleAction('shutdown')}>
          <Power size={13} />
          <div class="item-text">
            <strong>ACPI SHUTDOWN</strong>
            <span class="sub">Graceful in-guest OS signal</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('reboot')}>
          <RotateCw size={13} />
          <div class="item-text">
            <strong>SOFT REBOOT</strong>
            <span class="sub">Standard OS restart</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('pause')}>
          <Pause size={13} />
          <div class="item-text">
            <strong>SUSPEND TO RAM</strong>
            <span class="sub">Freeze CPU cycles (QEMU pause)</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('hibernate')}>
          <HardDriveDownload size={13} />
          <div class="item-text">
            <strong>HIBERNATE TO DISK</strong>
            <span class="sub">Dump active memory to image</span>
          </div>
        </button>

        <div class="menu-divider"></div>

        <button class="menu-item warn-item" onclick={() => handleAction('hard-reset')}>
          <RotateCw size={13} />
          <div class="item-text">
            <strong>HARD RESET (COLD)</strong>
            <span class="sub">Instant CPU vector reset</span>
          </div>
        </button>

        <button class="menu-item danger-item" onclick={() => handleAction('force-off')}>
          <Zap size={13} />
          <div class="item-text">
            <strong>FORCE STOP / PULL PLUG</strong>
            <span class="sub">SIGKILL destroy (loss of unsaved data)</span>
          </div>
        </button>
      {:else}
        <button class="menu-item" onclick={() => handleAction('shutdown')}>
          <Play size={13} />
          <div class="item-text">
            <strong>NORMAL POWER ON</strong>
            <span class="sub">Initialize QEMU process</span>
          </div>
        </button>
      {/if}
    </div>
  {/if}
</div>

<DestructiveGuardrailModal
  open={guardrailOpen}
  action={guardrailAction}
  targetName={d.name}
  targetUuid={d.uuid}
  onconfirm={onGuardrailConfirm}
  onclose={() => (guardrailOpen = false)}
/>

<style>
  .power-split-wrap {
    position: relative;
    display: inline-flex;
  }
  .button-group {
    display: inline-flex;
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 var(--ink);
  }
  .main-act-btn {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 34px;
    padding: 0 14px;
    border: none;
    border-right: 1px solid var(--border-strong);
    background: var(--metal);
    color: var(--text);
    font-family: var(--font-display);
    font-size: 12.5px;
    font-weight: 700;
    letter-spacing: 0.06em;
    cursor: pointer;
  }
  .lg .main-act-btn {
    height: 38px;
    padding: 0 16px;
    font-size: 13px;
  }
  .main-act-btn.accent {
    background: var(--metal-accent);
    color: var(--accent-contrast);
  }
  .main-act-btn.running {
    background: var(--surface);
    color: var(--text);
  }
  .split-chev-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 34px;
    border: none;
    background: var(--metal);
    color: var(--text);
    cursor: pointer;
  }
  .lg .split-chev-btn {
    width: 32px;
    height: 38px;
  }
  .split-chev-btn.accent {
    background: var(--metal-accent);
    color: var(--accent-contrast);
    border-left: 1px solid rgba(0, 0, 0, 0.2);
  }
  .main-act-btn:hover:not(:disabled),
  .split-chev-btn:hover:not(:disabled) {
    background: var(--metal-hover);
    color: var(--text);
  }
  .main-act-btn.accent:hover:not(:disabled),
  .split-chev-btn.accent:hover:not(:disabled) {
    background: var(--accent-strong);
  }
  .main-act-btn:disabled,
  .split-chev-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  /* Dropdown Menu */
  .power-dropdown {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    min-width: 280px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    box-shadow: 3px 3px 0 var(--ink);
    z-index: 200;
    padding: 4px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .dropdown-header {
    padding: 6px 10px;
    font-size: 10px;
    font-weight: 700;
    color: var(--text-3);
    border-bottom: 1px solid var(--border);
    letter-spacing: 0.08em;
  }
  .menu-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 10px;
    background: transparent;
    border: 1px solid transparent;
    color: var(--text);
    text-align: left;
    cursor: pointer;
    font-family: inherit;
  }
  .menu-item:hover {
    background: var(--surface);
    border-color: var(--border);
  }
  .item-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .item-text strong {
    font-size: 11.5px;
    font-weight: 700;
    letter-spacing: 0.04em;
  }
  .item-text .sub {
    font-size: 10px;
    color: var(--text-3);
  }
  .menu-divider {
    height: 1px;
    background: var(--border);
    margin: 3px 0;
  }
  .warn-item:hover {
    background: color-mix(in srgb, var(--warn) 12%, var(--bg));
    border-color: var(--warn);
    color: var(--warn);
  }
  .danger-item:hover {
    background: color-mix(in srgb, var(--danger) 15%, var(--bg));
    border-color: var(--danger);
    color: var(--danger);
  }
  .danger-item:hover .sub,
  .warn-item:hover .sub {
    color: var(--text-2);
  }
</style>
