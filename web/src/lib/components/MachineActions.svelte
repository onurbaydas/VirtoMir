<script lang="ts">
  import { session } from '../session.svelte'
  // Primary power button + "more" menu for one machine (list rows and the machine page).
  import type { Domain } from '../api'
  import { ACTIONS, live } from '../live.svelte'
  import { menuItems, primaryAction } from '../machine'
  import ActionMenu, { type MenuItem } from './ActionMenu.svelte'
  import Rocket from '@lucide/svelte/icons/rocket'
  import { openDeploy } from '../deploy.svelte'
  import PowerSplitButton from './PowerSplitButton.svelte'

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
    <PowerSplitButton {d} {size} />
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
    height: 30px;
    padding: 0 12px;
    border: var(--frame) solid var(--border-strong);
    background: var(--metal);
    box-shadow: var(--bevel);
    color: var(--text);
    font-family: var(--font-display);
    font-size: 12.5px;
    font-weight: 700;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    white-space: nowrap;
    cursor: pointer;
    transition: background var(--dur-fast);
  }
  .lg .btn {
    height: 36px;
    padding: 0 16px;
    font-size: 13.5px;
  }
  .btn:hover:not(:disabled) {
    background: var(--metal-hover);
  }
  .btn:active:not(:disabled) {
    background: var(--metal-pressed);
    box-shadow: var(--bevel-in);
    transform: translate(1px, 1px);
  }
  /* In lists the accent is only the icon; the machine page's big button is filled. */
  .btn.accent :global(svg) {
    color: var(--accent);
  }
  .lg .btn.accent {
    background: var(--metal-accent);
    color: var(--accent-contrast);
    box-shadow:
      var(--bevel),
      3px 3px 0 var(--ink);
  }
  .lg .btn.accent :global(svg) {
    color: inherit;
  }
  .lg .btn.accent:hover:not(:disabled) {
    background: var(--accent-strong);
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: progress;
  }
</style>
