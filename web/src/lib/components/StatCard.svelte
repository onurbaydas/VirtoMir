<script lang="ts">
  import type { Component } from 'svelte'
  import type { Snippet } from 'svelte'

  interface Props {
    label: string
    icon: Component<{ size?: number; strokeWidth?: number }>
    /** Main figure, e.g. "42". */
    value: string
    unit?: string
    sub?: string
    loading?: boolean
    chart?: Snippet
  }
  let { label, icon: Icon, value, unit, sub, loading = false, chart }: Props = $props()
</script>

<article class="card" class:loading>
  <header>
    <span class="icon"><Icon size={15} strokeWidth={2} /></span>
    <span class="label">{label}</span>
  </header>
  {#if loading}
    <div class="sk sk-value"></div>
    <div class="sk sk-sub"></div>
  {:else}
    <div class="value num">
      {value}{#if unit}<span class="unit">{unit}</span>{/if}
    </div>
    {#if sub}<div class="sub">{sub}</div>{/if}
  {/if}
  {#if chart}<div class="chart">{@render chart()}</div>{/if}
</article>

<style>
  .card {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 0 16px;
    min-height: 150px;
    background: var(--surface);
    border: var(--frame) solid var(--ink);
    box-shadow: var(--shadow-hard);
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: center;
    gap: 7px;
    margin: 0 -16px 10px;
    padding: 4px 10px;
    background: #000;
    color: #f4f1e8;
    font-family: var(--font-display);
    font-size: 13.5px;
    font-stretch: 80%;
    font-weight: 800;
  }
  .icon {
    display: grid;
    place-items: center;
    color: var(--ochre);
  }
  .value {
    font-family: var(--font-display);
    font-size: 40px;
    font-stretch: 62%;
    font-weight: 900;
    line-height: 1;
    font-variant-numeric: tabular-nums;
    overflow-wrap: anywhere;
  }
  .unit {
    margin-left: 4px;
    font-family: var(--font-sans);
    font-size: 14px;
    font-stretch: 100%;
    font-weight: 400;
    color: var(--text-2);
  }
  .sub {
    color: var(--text-2);
    font-size: 12.5px;
  }
  .chart {
    margin: auto -16px 0;
    padding-top: 10px;
  }
  .sk {
    background: var(--skeleton);
    animation: shimmer 1.2s steps(2, jump-none) infinite;
  }
  .sk-value {
    width: 90px;
    height: 34px;
  }
  .sk-sub {
    width: 140px;
    height: 13px;
    margin-top: 8px;
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
</style>
