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

    live.act(d, act === 'hibernate' ? 'save' : act)
  }

  function onGuardrailConfirm() {
    guardrailOpen = false
    live.act(d, guardrailAction === 'force-off' ? 'force-off' : 'reset')
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
        <span>Shut down</span>
      {:else if paused}
        <Play size={size === 'lg' ? 15 : 13} />
        <span>Resume</span>
      {:else}
        <Play size={size === 'lg' ? 15 : 13} />
        <span>Start</span>
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
    <div class="power-dropdown">
      <div class="dropdown-header">
        <span>Power</span>
      </div>

      {#if active}
        <button class="menu-item" onclick={() => handleAction('shutdown')}>
          <Power size={13} />
          <div class="item-text">
            <strong>Shut down</strong>
            <span class="sub">Graceful in-guest OS signal</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('reboot')}>
          <RotateCw size={13} />
          <div class="item-text">
            <strong>Reboot</strong>
            <span class="sub">Standard OS restart</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('pause')}>
          <Pause size={13} />
          <div class="item-text">
            <strong>Pause</strong>
            <span class="sub">Freeze the vCPUs; memory stays in use</span>
          </div>
        </button>

        <button class="menu-item" onclick={() => handleAction('hibernate')}>
          <HardDriveDownload size={13} />
          <div class="item-text">
            <strong>Save to disk</strong>
            <span class="sub">Write memory to disk and stop; next start resumes</span>
          </div>
        </button>

        <div class="menu-divider"></div>

        <button class="menu-item warn-item" onclick={() => handleAction('hard-reset')}>
          <RotateCw size={13} />
          <div class="item-text">
            <strong>Hard reset</strong>
            <span class="sub">Like the reset button: no shutdown</span>
          </div>
        </button>

        <button class="menu-item danger-item" onclick={() => handleAction('force-off')}>
          <Zap size={13} />
          <div class="item-text">
            <strong>Force off</strong>
            <span class="sub">Like pulling the plug: unsaved data is lost</span>
          </div>
        </button>
      {:else}
        <button class="menu-item" onclick={handlePrimary}>
          <Play size={13} />
          <div class="item-text">
            <strong>{paused ? 'Resume' : 'Start'}</strong>
            <span class="sub">{paused ? 'Continue where it was frozen' : 'Boot (or resume a machine saved to disk)'}</span>
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
