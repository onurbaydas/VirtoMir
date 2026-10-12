<script lang="ts">
  import Play from '@lucide/svelte/icons/play'
  import Power from '@lucide/svelte/icons/power'
  import RotateCw from '@lucide/svelte/icons/rotate-cw'
  import Pause from '@lucide/svelte/icons/pause'
  import Zap from '@lucide/svelte/icons/zap'
  import Copy from '@lucide/svelte/icons/copy'
  import Monitor from '@lucide/svelte/icons/monitor'
  import type { Action, Domain } from '../api'
  import { ACTIONS, isActive, live } from '../live.svelte'
  import { formatKiB, formatPercent, plural } from '../format'
  import { toasts } from '../toast.svelte'
  import StatePill from './StatePill.svelte'
  import Sparkline from './Sparkline.svelte'
  import ActionMenu, { type MenuItem } from './ActionMenu.svelte'

  let { d }: { d: Domain } = $props()

  let pending = $derived(live.pending[d.uuid])
  let active = $derived(isActive(d.state))

  /** The one obvious next step for the machine's current state. */
  let primary = $derived.by((): { action: Action; icon: typeof Play; accent?: boolean } | null => {
    switch (d.state) {
      case 'shutoff':
      case 'crashed':
      case 'unknown':
        return { action: 'start', icon: Play, accent: true }
      case 'paused':
        return { action: 'resume', icon: Play, accent: true }
      case 'running':
      case 'blocked':
        return { action: 'shutdown', icon: Power }
      default:
        return null
    }
  })

  let menu = $derived.by((): MenuItem[] => {
    const items: MenuItem[] = []
    const act = (a: Action) => () => live.act(d, a)
    if (d.state === 'running' || d.state === 'blocked') {
      items.push({ label: 'Reboot', icon: RotateCw, run: act('reboot') })
      items.push({ label: 'Pause', icon: Pause, run: act('pause') })
    }
    if (active || d.state === 'paused') {
      items.push({
        label: 'Force off',
        icon: Zap,
        run: act('force-off'),
        danger: { confirm: 'Click again to force off' },
        hint: 'Cuts power immediately (virsh destroy). Unsaved data in the guest is lost.',
      })
    }
    items.push({ label: 'Copy UUID', icon: Copy, run: () => copy(d.uuid, 'UUID') })
    return items
  })

  async function copy(text: string, what: string) {
    try {
      await navigator.clipboard.writeText(text)
      toasts.push({ kind: 'info', title: `${what} copied`, detail: text })
    } catch {
      toasts.push({ kind: 'error', title: `Could not copy ${what}`, detail: text })
    }
  }

  let memLabel = $derived(
    d.memoryKiB === d.maxMemoryKiB
      ? formatKiB(d.memoryKiB)
      : `${formatKiB(d.memoryKiB)} / ${formatKiB(d.maxMemoryKiB)}`,
  )
</script>

<div class="row" class:off={!active && d.state !== 'paused'} role="row">
  <div class="name" role="cell">
    <span class="avatar" class:on={active}><Monitor size={16} /></span>
    <div class="titles">
      <span class="title">{d.name}</span>
      <span class="uuid mono" title={d.uuid}>{d.uuid.slice(0, 8)}{#if !d.persistent} · transient{/if}{#if d.autostart} · autostart{/if}</span>
    </div>
  </div>

  <div role="cell"><StatePill state={d.state} progress={pending ? ACTIONS[pending.action].progress : undefined} /></div>

  <div class="metric num" role="cell" title="Virtual CPUs">{plural(d.vcpus, 'vCPU')}</div>

  <div class="metric num" role="cell" title={d.memoryKiB === d.maxMemoryKiB ? 'Memory' : 'Current / maximum memory (balloon)'}>{memLabel}</div>

  <div class="cpu" role="cell">
    {#if active}
      <div class="cpu-chart"><Sparkline data={live.domainCpu(d.uuid)} tick={live.tick} height={28} /></div>
      <span class="cpu-val mono">{formatPercent(d.cpuPercent)}<small>%</small></span>
    {:else}
      <span class="cpu-idle">—</span>
    {/if}
  </div>

  <div class="actions" role="cell">
    {#if primary}
      {@const Icon = primary.icon}
      <button
        class="btn"
        class:accent={primary.accent}
        disabled={!!pending}
        onclick={() => live.act(d, primary.action)}
      >
        <Icon size={14} strokeWidth={2.25} />
        {ACTIONS[primary.action].label}
      </button>
    {/if}
    <ActionMenu items={menu} label="More actions for {d.name}" />
  </div>
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: var(--cols);
    align-items: center;
    gap: 16px;
    min-height: 64px;
    padding: 0 16px;
    border-top: 1px solid var(--border);
    transition: background var(--dur-fast);
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .name {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }
  .avatar {
    flex: none;
    display: grid;
    place-items: center;
    width: 34px;
    height: 34px;
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--text-3);
    transition:
      color var(--dur),
      background var(--dur);
  }
  .avatar.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .titles {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .title {
    font-weight: 600;
    font-size: 14.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .uuid {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .metric {
    color: var(--text-2);
    white-space: nowrap;
  }
  .off .metric {
    color: var(--text-3);
  }
  .cpu {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .cpu-chart {
    flex: 1;
    min-width: 0;
  }
  .cpu-val {
    width: 48px;
    text-align: right;
    font-size: 13px;
  }
  .cpu-val small {
    color: var(--text-3);
    margin-left: 1px;
  }
  .cpu-idle {
    color: var(--text-3);
  }
  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 6px;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    background: var(--surface-2);
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
    transition:
      background var(--dur-fast),
      border-color var(--dur-fast),
      opacity var(--dur-fast);
  }
  .btn:hover:not(:disabled) {
    background: var(--surface-hover);
    border-color: var(--text-3);
  }
  .btn.accent {
    background: var(--accent);
    border-color: transparent;
    color: var(--accent-contrast);
  }
  .btn.accent:hover:not(:disabled) {
    background: var(--accent-strong);
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: progress;
  }
</style>
