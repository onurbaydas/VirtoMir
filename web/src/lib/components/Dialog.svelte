<script lang="ts">
  // Form dialog on the native <dialog> element: focus trap, Escape and backdrop come free.
  // Dangerous one-step actions use inline two-click confirms instead (see ActionMenu).
  import X from '@lucide/svelte/icons/x'
  import type { Snippet } from 'svelte'

  interface Props {
    open: boolean
    title: string
    subtitle?: string
    width?: number
    onclose: () => void
    children: Snippet
    footer?: Snippet
  }
  let { open, title, subtitle, width = 480, onclose, children, footer }: Props = $props()

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
        <button class="x" aria-label="Close" onclick={onclose}><X size={16} /></button>
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
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-lg);
    background: var(--bg-elev);
    color: var(--text);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
  }
  dialog[open] {
    animation: pop var(--dur) var(--ease-out);
  }
  dialog::backdrop {
    background: rgba(5, 8, 12, 0.55);
    backdrop-filter: blur(2px);
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
    padding: 16px 18px 12px;
  }
  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 650;
  }
  header p {
    margin: 4px 0 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .x {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
  }
  .x:hover {
    background: var(--surface-hover);
    color: var(--text);
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
    border-top: 1px solid var(--border);
    background: var(--surface);
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
  }
</style>
