<script lang="ts" module>
  import type { HistoryRange } from '../api'
  export type Range = 'live' | HistoryRange
</script>

<script lang="ts">
  let { value = $bindable(), live = true }: { value: Range; live?: boolean } = $props()
  const OPTIONS: { key: Range; label: string; title: string }[] = [
    { key: 'live', label: 'Live', title: 'Last minute, every second' },
    { key: '1h', label: '1h', title: 'Last hour (10 s steps)' },
    { key: '6h', label: '6h', title: 'Last 6 hours (10 s steps)' },
    { key: '24h', label: '24h', title: 'Last day (1 min steps)' },
    { key: '7d', label: '7d', title: 'Last week (10 min steps)' },
    { key: '30d', label: '30d', title: 'Last month (1 h steps)' },
  ]
</script>

<div class="tabs" role="radiogroup" aria-label="Time range">
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
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
  }
  button {
    height: 26px;
    padding: 0 10px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 500;
    cursor: pointer;
  }
  button:hover {
    color: var(--text);
  }
  button.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
</style>
