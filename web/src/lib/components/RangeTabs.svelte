<script lang="ts" module>
  import type { HistoryRange } from '../api'
  export type Range = 'live' | HistoryRange
</script>

<script lang="ts">
  import { t } from '../i18n.svelte'
  let { value = $bindable(), live = true }: { value: Range; live?: boolean } = $props()
  const OPTIONS: { key: Range; label: string; title: string }[] = [
    { key: 'live', label: t('Live'), title: t('Last minute, every second') },
    { key: '1h', label: '1h', title: t('Last hour (10 s steps)') },
    { key: '6h', label: '6h', title: t('Last 6 hours (10 s steps)') },
    { key: '24h', label: '24h', title: t('Last day (1 min steps)') },
    { key: '7d', label: '7d', title: t('Last week (10 min steps)') },
    { key: '30d', label: '30d', title: t('Last month (1 h steps)') },
  ]
</script>

<div class="tabs" role="radiogroup" aria-label={t('Time range')}>
  {#each OPTIONS.filter((o) => live || o.key !== 'live') as o (o.key)}
    <button role="radio" aria-checked={value === o.key} class:on={value === o.key} title={o.title} onclick={() => (value = o.key)}>
      {o.label}
    </button>
  {/each}
</div>

<style>
  .tabs {
    display: inline-flex;
    gap: 2px;
    padding: 2px;
    border: 1px solid var(--border-strong);
    background: var(--bg-elev);
    box-shadow: var(--bevel-in);
  }
  button {
    height: 26px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }
  button:hover {
    color: var(--text);
  }
  button.on {
    background: var(--metal-accent);
    color: var(--accent-contrast);
    outline: 1px solid var(--border-strong);
    box-shadow: var(--bevel);
  }
</style>
