<script lang="ts">
  import { t as tt } from '../i18n.svelte'
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
        {#if t.sticky}<span class="spin"></span>{:else if t.kind === 'error'}<CircleAlert size={17} />{:else if t.kind === 'warn'}<TriangleAlert size={17} />{:else}<Check size={17} />{/if}
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
      <button class="close" aria-label={tt('Dismiss')} onclick={() => toasts.dismiss(t.id)}><X size={15} /></button>
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
    padding: 12px 10px 12px 12px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-left: 8px solid var(--c);
    border-radius: var(--radius-sm);
    box-shadow:
      var(--bevel),
      var(--shadow-pop);
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
  .spin {
    display: block;
    width: 15px;
    height: 15px;
    margin: 1px;
    border: 2px solid var(--info);
    border-right-color: transparent;
    border-radius: var(--radius-sm);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
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
    font-family: var(--font-display);
    font-weight: 700;
    letter-spacing: 0.03em;
    overflow-wrap: anywhere;
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
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--metal);
    box-shadow: var(--bevel);
    font-family: var(--font-display);
    font-size: 12px;
    font-weight: 700;
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
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
  }
  .close:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
</style>
