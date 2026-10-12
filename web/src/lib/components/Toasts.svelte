<script lang="ts">
  import { fly } from 'svelte/transition'
  import { flip } from 'svelte/animate'
  import X from '@lucide/svelte/icons/x'
  import CircleAlert from '@lucide/svelte/icons/circle-alert'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import Check from '@lucide/svelte/icons/check'
  import { toasts } from '../toast.svelte'
</script>

<div class="stack" aria-live="polite">
  {#each toasts.items as t (t.id)}
    <div class="toast {t.kind}" in:fly={{ y: 12, duration: 220 }} out:fly={{ x: 24, duration: 160 }} animate:flip={{ duration: 200 }}>
      <span class="icon">
        {#if t.kind === 'error'}<CircleAlert size={17} />{:else if t.kind === 'warn'}<TriangleAlert size={17} />{:else}<Check size={17} />{/if}
      </span>
      <div class="body">
        <strong>{t.title}</strong>
        {#if t.detail}<span>{t.detail}</span>{/if}
        {#if t.action}
          {@const a = t.action}
          <button
            class="act"
            class:danger={a.danger}
            onclick={() => {
              toasts.dismiss(t.id)
              a.run()
            }}>{a.label}</button
          >
        {/if}
      </div>
      <button class="close" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}><X size={15} /></button>
    </div>
  {/each}
</div>

<style>
  .stack {
    position: fixed;
    right: 20px;
    bottom: 20px;
    z-index: 50;
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: min(380px, calc(100vw - 40px));
  }
  .toast {
    --c: var(--ok);
    display: flex;
    gap: 11px;
    padding: 13px 12px 13px 14px;
    background: var(--bg-elev);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .toast.error {
    --c: var(--danger);
  }
  .toast.warn {
    --c: var(--warn);
  }
  .icon {
    color: var(--c);
    padding-top: 1px;
  }
  .body {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  strong {
    font-weight: 600;
  }
  .body span {
    color: var(--text-2);
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  .act {
    margin-top: 8px;
    height: 28px;
    padding: 0 12px;
    border: 1px solid var(--border-strong);
    border-radius: 7px;
    background: var(--surface-2);
    font-size: 12.5px;
    font-weight: 600;
    cursor: pointer;
  }
  .act.danger {
    background: var(--danger);
    border-color: transparent;
    color: #fff;
  }
  .close {
    align-self: flex-start;
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
  }
  .close:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
</style>
