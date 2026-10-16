<script lang="ts">
  import { t } from '../i18n.svelte'
  // Form dialog on the native <dialog> element: focus trap, Escape and backdrop come free.
  // Dangerous one-step actions use inline two-click confirms instead (see ActionMenu).
  import X from '@lucide/svelte/icons/x'
  import type { Snippet } from 'svelte'

  interface Props {
    open?: boolean
    title: string
    subtitle?: string
    width?: number
    onclose?: () => void
    children: Snippet
    footer?: Snippet
  }
  let { open = $bindable(false), title, subtitle, width = 480, onclose = () => (open = false), children, footer }: Props = $props()

  let el: HTMLDialogElement
  $effect(() => {
    if (open && !el.open) el.showModal()
    else if (!open && el.open) el.close()
  })
</script>

<dialog
  bind:this={el}
  style:--w="{width}px"
  onclose={onclose}
  onclick={(e) => e.target === el && onclose()}
  aria-labelledby="dlg-title"
>
  {#if open}
    <div class="inner">
      <header>
        <div>
          <h2 id="dlg-title">{title}</h2>
          {#if subtitle}<p>{subtitle}</p>{/if}
        </div>
        <button class="x" aria-label={t('Close')} onclick={onclose}><X size={16} /></button>
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  {/if}
</dialog>

<style>
  dialog {
    width: min(var(--w), calc(100vw - 32px));
    max-height: calc(100vh - 48px);
    padding: 0;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text);
    box-shadow:
      var(--bevel),
      8px 8px 0 var(--ink);
    overflow: hidden;
  }
  dialog[open] {
    animation: pop var(--dur) var(--ease-out);
  }
  dialog::backdrop {
    background: rgba(0, 0, 0, 0.6);
  }
  .inner {
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 48px);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 14px;
    padding: 10px 10px 10px 18px;
    border-bottom: var(--frame) solid var(--border-strong);
    background: var(--brushed), var(--metal-head);
    box-shadow: inset 0 3px 0 var(--accent);
  }
  h2 {
    margin: 3px 0 0;
    font-size: 17px;
    font-weight: 700;
    overflow-wrap: anywhere;
  }
  header p {
    margin: 2px 0 0;
    color: var(--text-2);
    font-size: 13px;
  }
  .x {
    display: grid;
    place-items: center;
    width: 28px;
    flex: none;
    height: 28px;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--metal);
    box-shadow: var(--bevel);
    color: var(--text);
    cursor: pointer;
  }
  .x:hover {
    background: var(--danger);
    color: #fff;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 4px 18px 18px;
    overflow-y: auto;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 18px;
    border-top: var(--frame) solid var(--border-strong);
    background: var(--metal-head);
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translate(-6px, -6px);
    }
  }
</style>
