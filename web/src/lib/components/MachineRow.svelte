<script lang="ts">
  import { t } from '../i18n.svelte'
  import Monitor from '@lucide/svelte/icons/monitor'
  import LayoutTemplate from '@lucide/svelte/icons/layout-template'
  import Box from '@lucide/svelte/icons/box'
  import type { Domain } from '../api'
  import { ACTIONS, isActive, live } from '../live.svelte'
  import { formatKiB, formatPercent, plural } from '../format'
  import { machineHref, router } from '../router.svelte'
  import StatePill from './StatePill.svelte'
  import Sparkline from './Sparkline.svelte'
  import MachineActions from './MachineActions.svelte'
  import type { PlacementViolation } from '../enterprise'

  let { d, selected = false, onselect, hostName, projectName, violation }: { d: Domain; selected?: boolean; onselect?: (on: boolean) => void; hostName?: string; projectName?: string; violation?: PlacementViolation } = $props()

  let pending = $derived(live.pending[d.uuid])
  let active = $derived(isActive(d.state))

  let memLabel = $derived(
    d.memoryKiB === d.maxMemoryKiB
      ? formatKiB(d.memoryKiB)
      : `${formatKiB(d.memoryKiB)} / ${formatKiB(d.maxMemoryKiB)}`,
  )
</script>

<div class="row" class:off={!active && d.state !== 'paused'} class:selected role="row">
  <div class="name" role="cell">
    {#if onselect}
      <input class="sel" type="checkbox" checked={selected} aria-label={t('Select {name}', { name: d.name })} onchange={(e) => onselect(e.currentTarget.checked)} />
    {/if}
    <span class="avatar" class:on={active} title={d.template ? t('Template') : d.kind === 'container' ? t('Container') : undefined}>
      {#if d.template}
        <LayoutTemplate size={16} />
      {:else if d.kind === 'container'}
        <Box size={16} />
      {:else}
        <Monitor size={16} />
      {/if}
    </span>
    <div class="titles">
      <div class="title-line">
        <a class="title" href={machineHref(d.uuid)} onclick={router.link}>{d.name}</a>
        {#if d.kind === 'container'}
          <span class="kind-badge lxc" title={t('System Container (LXC)')}>LXC</span>
        {/if}
        {#if hostName}
          <span class="host-badge" title={t('Running on {host}', { host: hostName })}>
            <span class="host-dot"></span>{hostName}
          </span>
        {/if}
      </div>
      <span class="uuid mono" title={d.uuid}>
        {d.uuid.slice(0, 8)}
        {#if violation}
          <span
            class="viol-badge"
            class:hard={violation.hard}
            class:soft={!violation.hard}
            title="{violation.ruleName}: {violation.message}"
          >
            {violation.hard ? t('violation') : t('warning')}
          </span>
        {/if}
        {#if !d.persistent} {t('· transient')}{/if}{#if d.autostart} {t('· autostart')}{/if}{#if d.template} {t('· template')}{:else if d.linkedFrom} {t('· linked clone')}{/if}
      </span>
    </div>
  </div>

  <div role="cell"><StatePill state={d.state} progress={pending ? ACTIONS[pending.action].progress : undefined} /></div>

  <div class="metric" role="cell" title={projectName ?? t('No project (global)')}>
    {#if projectName}<span class="proj">{projectName}</span>{:else}<span class="proj none">—</span>{/if}
  </div>

  <div class="metric num" role="cell" title={t('Virtual CPUs')}>{plural(d.vcpus, 'vCPU')}</div>

  <div class="metric num" role="cell" title={d.memoryKiB === d.maxMemoryKiB ? t('Memory') : t('Current / maximum memory (balloon)')}>{memLabel}</div>

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
  .row.selected {
    background: var(--accent-soft);
  }
  .sel {
    position: relative;
    z-index: 1; /* above the name link's click area */
    flex: none;
    width: 16px;
    height: 16px;
    margin: 0;
    accent-color: var(--accent);
    cursor: pointer;
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
    border-radius: var(--radius-sm);
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
  .title-line {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .host-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--accent);
    padding: 1px 6px;
    letter-spacing: 0.05em;
    flex-shrink: 0;
  }
  .kind-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px 5px;
    border-radius: var(--radius-sm);
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    flex-shrink: 0;
  }
  .kind-badge.lxc {
    background: color-mix(in srgb, var(--info) 15%, transparent);
    color: var(--info);
    border: 1px solid color-mix(in srgb, var(--info) 30%, transparent);
  }
  .host-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--ok);
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
  .viol-badge {
    position: relative;
    z-index: 1;
    display: inline-flex;
    align-items: center;
    padding: 0 4px;
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    border-radius: var(--radius-sm);
    margin-left: 4px;
    cursor: help;
  }
  .viol-badge.hard {
    background: var(--danger-soft);
    color: var(--danger);
    border: 1px solid var(--danger);
  }
  .viol-badge.soft {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
    border: 1px solid var(--warn);
  }
  .metric {
    color: var(--text-2);
    white-space: nowrap;
  }
  .proj {
    display: inline-block;
    max-width: 110px;
    overflow: hidden;
    padding: 1px 6px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11.5px;
    text-overflow: ellipsis;
  }
  .proj.none {
    border: 0;
    background: none;
    padding: 0;
    color: var(--text-3);
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
