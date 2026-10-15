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
    padding: 16px 18px 0;
    min-height: 148px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: 0;
    overflow: hidden;
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: -16px -18px 10px;
    padding: 6px 18px;
    border-bottom: var(--frame) solid var(--border-strong);
    background: var(--metal-head);
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 12.5px;
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    color: var(--accent);
  }
  .value {
    font-family: var(--font-mono);
    font-size: 30px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.1;
    overflow-wrap: anywhere;
  }
  .unit {
    margin-left: 3px;
    font-family: var(--font-display);
    font-size: 15px;
    font-weight: 600;
    letter-spacing: 0.04em;
    color: var(--text-3);
  }
  .sub {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .chart {
    margin: auto -18px 0;
    padding-top: 10px;
  }
  .sk {
    border-radius: 0;
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  .sk-value {
    width: 90px;
    height: 32px;
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
