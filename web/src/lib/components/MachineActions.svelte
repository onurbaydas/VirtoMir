<script lang="ts">
  import { session } from '../session.svelte'
  // Primary power button + "more" menu for one machine (list rows and the machine page).
  import type { Domain } from '../api'
  import { ACTIONS, live } from '../live.svelte'
  import { menuItems, primaryAction } from '../machine'
  import ActionMenu, { type MenuItem } from './ActionMenu.svelte'
  import Rocket from '@lucide/svelte/icons/rocket'
  import { openDeploy } from '../deploy.svelte'

  let { d, size = 'md', extra = [] }: { d: Domain; size?: 'md' | 'lg'; extra?: MenuItem[] } = $props()

  let pending = $derived(live.pending[d.uuid])
  let primary = $derived(primaryAction(d))
  let menu = $derived([...menuItems(d), ...extra])
</script>

<div class="actions {size}">
  {#if d.template}
    <button class="btn accent" disabled={!session.can('admin')} title={session.why('admin') || 'Create a machine from this template'} onclick={() => openDeploy(d)}>
      <Rocket size={size === 'lg' ? 15 : 14} strokeWidth={2.25} />
      New machine
    </button>
  {:else if primary}
    {@const Icon = primary.icon}
    <button
      class="btn"
      class:accent={primary.accent}
      disabled={!!pending || !session.can('operator')}
      title={session.why('operator')}
      onclick={() => live.act(d, primary.action)}
    >
      <Icon size={size === 'lg' ? 15 : 14} strokeWidth={2.25} />
      {ACTIONS[primary.action].label}
    </button>
  {/if}
  <ActionMenu items={menu} label="More actions for {d.name}" />
</div>

<style>
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
  .lg .btn {
    height: 36px;
    padding: 0 16px;
    font-size: 13.5px;
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
