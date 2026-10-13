<script lang="ts">
  import Monitor from '@lucide/svelte/icons/monitor'
  import type { Domain } from '../api'
  import { ACTIONS, isActive, live } from '../live.svelte'
  import { formatKiB, formatPercent, plural } from '../format'
  import { machineHref, router } from '../router.svelte'
  import StatePill from './StatePill.svelte'
  import Sparkline from './Sparkline.svelte'
  import MachineActions from './MachineActions.svelte'

  let { d }: { d: Domain } = $props()

  let pending = $derived(live.pending[d.uuid])
  let active = $derived(isActive(d.state))

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
      <a class="title" href={machineHref(d.uuid)} onclick={router.link}>{d.name}</a>
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

  <div class="actions" role="cell"><MachineActions {d} /></div>
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
    color: var(--text);
    text-decoration: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .title:hover {
    color: var(--accent);
  }
  /* The whole name cell is clickable, not just the text. */
  .title::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .name {
    position: relative;
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
</style>
