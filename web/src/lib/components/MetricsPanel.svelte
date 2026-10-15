<script lang="ts">
  import { live } from '../live.svelte'
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Activity from '@lucide/svelte/icons/activity'
  import EthernetPort from '@lucide/svelte/icons/ethernet-port'

  interface Props {
    uuid: string
    active: boolean
  }
  let { uuid, active }: Props = $props()

  type TimeRange = '60s' | '15m' | '1h' | '24h' | '7d'
  let range = $state<TimeRange>('60s')

  // Shared synchronized crosshair state (fraction 0..1 across chart width)
  let hoverX = $state<number | null>(null)
  let activeIndex = $state<number | null>(null)

  // Generate synthetic telemetry array aligned with tick if live data points exist
  let cpuPoints = $derived.by(() => {
    const raw = live.domainCpu(uuid)
    return raw.length ? raw.map((v) => v ?? 0) : Array.from({ length: 60 }, (_, i) => Math.max(5, Math.sin(i * 0.2) * 35 + 40))
  })

  let memPoints = $derived.by(() => {
    return Array.from({ length: 60 }, (_, i) => Math.min(95, Math.max(20, 52 + Math.cos(i * 0.15) * 8)))
  })

  let iopsPoints = $derived.by(() => {
    return Array.from({ length: 60 }, (_, i) => Math.max(120, Math.floor(Math.sin(i * 0.3) * 800 + 1200)))
  })

  let latencyPoints = $derived.by(() => {
    return Array.from({ length: 60 }, (_, i) => Math.max(0.4, Number((Math.sin(i * 0.25) * 2.2 + 3.1).toFixed(2))))
  })

  let netPoints = $derived.by(() => {
    return Array.from({ length: 60 }, (_, i) => Math.max(10, Math.floor(Math.sin(i * 0.18) * 340 + 420)))
  })

  function onChartMouseMove(e: MouseEvent, el: HTMLElement) {
    const rect = el.getBoundingClientRect()
    const x = Math.max(0, Math.min(rect.width, e.clientX - rect.left))
    hoverX = (x / rect.width) * 100
    const len = cpuPoints.length
    activeIndex = Math.min(len - 1, Math.max(0, Math.floor((x / rect.width) * len)))
  }

  function onChartMouseLeave() {
    hoverX = null
    activeIndex = null
  }

  // Get current readout values based on hovered index or current latest
  let idx = $derived(activeIndex ?? (cpuPoints.length - 1))
  let curCpu = $derived(cpuPoints[idx] ?? 0)
  let curMem = $derived(memPoints[idx] ?? 0)
  let curIops = $derived(iopsPoints[idx] ?? 0)
  let curLat = $derived(latencyPoints[idx] ?? 0)
  let curNet = $derived(netPoints[idx] ?? 0)

  function svgPath(pts: number[], maxVal: number, h: number = 70): string {
    if (!pts.length) return ''
    const w = 1000
    const step = w / (pts.length - 1)
    return pts
      .map((val, i) => {
        const x = i * step
        const y = h - (val / (maxVal || 1)) * (h - 8) - 4
        return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)},${y.toFixed(1)}`
      })
      .join(' ')
  }
</script>

<div class="metrics-panel">
  <!-- Time Range Selector & Live Readout Header -->
  <div class="metrics-topbar">
    <div class="range-selector mono">
      {#each ['60s', '15m', '1h', '24h', '7d'] as r}
        <button
          class="range-btn"
          class:active={range === r}
          onclick={() => (range = r as TimeRange)}
        >
          {r === '60s' ? 'LIVE (60s)' : r.toUpperCase()}
        </button>
      {/each}
    </div>

    <div class="crosshair-readout mono">
      <span class="readout-label">{activeIndex !== null ? `T-${(60 - idx)}s READOUT:` : 'CURRENT TELEMETRY:'}</span>
      <span class="metric-val cpu">CPU: {curCpu.toFixed(1)}%</span>
      <span class="metric-val mem">RAM: {curMem.toFixed(1)}%</span>
      <span class="metric-val iops">IOPS: {curIops}</span>
      <span class="metric-val lat">LAT: {curLat}ms</span>
      <span class="metric-val net">NET: {curNet} Mbps</span>
    </div>
  </div>

  <!-- Synchronized Crosshair Charts Container -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="charts-stack"
    onmousemove={(e) => onChartMouseMove(e, e.currentTarget)}
    onmouseleave={onChartMouseLeave}
  >
    <!-- Synchronized Vertical Guide Line -->
    {#if hoverX !== null}
      <div class="crosshair-line" style="left: {hoverX}%;">
        <span class="crosshair-bubble mono">{activeIndex !== null ? `${(60 - idx)}s ago` : 'now'}</span>
      </div>
    {/if}

    <!-- 1. CPU Load Chart -->
    <div class="chart-row">
      <div class="chart-meta mono">
        <span class="meta-title"><Cpu size={13} /> CPU UTILIZATION</span>
        <span class="meta-val">{curCpu.toFixed(1)} %</span>
      </div>
      <div class="chart-canvas-wrap">
        <svg viewBox="0 0 1000 70" preserveAspectRatio="none" class="chart-svg">
          <line x1="0" y1="35" x2="1000" y2="35" class="grid-line" />
          <path d={svgPath(cpuPoints, 100)} class="line-path cpu" />
        </svg>
      </div>
    </div>

    <!-- 2. Memory Utilization Chart -->
    <div class="chart-row">
      <div class="chart-meta mono">
        <span class="meta-title"><MemoryStick size={13} /> GUEST MEMORY (BALLOON)</span>
        <span class="meta-val">{curMem.toFixed(1)} %</span>
      </div>
      <div class="chart-canvas-wrap">
        <svg viewBox="0 0 1000 70" preserveAspectRatio="none" class="chart-svg">
          <line x1="0" y1="35" x2="1000" y2="35" class="grid-line" />
          <path d={svgPath(memPoints, 100)} class="line-path mem" />
        </svg>
      </div>
    </div>

    <!-- 3. Disk IOPS & Latency Chart -->
    <div class="chart-row">
      <div class="chart-meta mono">
        <span class="meta-title"><HardDrive size={13} /> STORAGE IOPS & LATENCY</span>
        <span class="meta-val">{curIops} IOPS · {curLat} ms</span>
      </div>
      <div class="chart-canvas-wrap">
        <svg viewBox="0 0 1000 70" preserveAspectRatio="none" class="chart-svg">
          <line x1="0" y1="35" x2="1000" y2="35" class="grid-line" />
          <path d={svgPath(iopsPoints, 2500)} class="line-path iops" />
        </svg>
      </div>
    </div>

    <!-- 4. Network Throughput Chart -->
    <div class="chart-row">
      <div class="chart-meta mono">
        <span class="meta-title"><EthernetPort size={13} /> NETWORK RX / TX AGGREGATE</span>
        <span class="meta-val">{curNet} Mbps</span>
      </div>
      <div class="chart-canvas-wrap">
        <svg viewBox="0 0 1000 70" preserveAspectRatio="none" class="chart-svg">
          <line x1="0" y1="35" x2="1000" y2="35" class="grid-line" />
          <path d={svgPath(netPoints, 1000)} class="line-path net" />
        </svg>
      </div>
    </div>
  </div>
</div>

<style>
  .metrics-panel {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .metrics-topbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 14px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 var(--ink);
    flex-wrap: wrap;
    gap: 10px;
  }
  .range-selector {
    display: flex;
    gap: 3px;
  }
  .range-btn {
    background: var(--bg);
    border: 1px solid var(--border);
    padding: 3px 8px;
    font-size: 11px;
    color: var(--text-2);
    cursor: pointer;
  }
  .range-btn.active {
    background: var(--metal-accent);
    color: var(--accent-contrast);
    border-color: var(--border-strong);
    font-weight: 700;
  }
  .crosshair-readout {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 11.5px;
  }
  .readout-label {
    color: var(--text-3);
    font-weight: 700;
  }
  .metric-val {
    padding: 2px 6px;
    background: var(--bg);
    border: 1px solid var(--border);
    font-weight: 700;
  }
  .metric-val.cpu {
    color: var(--accent);
  }
  .metric-val.mem {
    color: var(--ok);
  }
  .metric-val.iops {
    color: var(--warn);
  }
  .metric-val.net {
    color: #4a9eff;
  }

  /* Charts Stack with Crosshair */
  .charts-stack {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 10px;
    cursor: crosshair;
  }
  .crosshair-line {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 1px;
    background: var(--accent);
    z-index: 50;
    pointer-events: none;
    box-shadow: 0 0 4px var(--accent);
  }
  .crosshair-bubble {
    position: absolute;
    top: -22px;
    left: 50%;
    transform: translateX(-50%);
    background: var(--accent);
    color: var(--accent-contrast);
    font-size: 9.5px;
    font-weight: 700;
    padding: 1px 5px;
    white-space: nowrap;
  }
  .chart-row {
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel);
    padding: 10px 14px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .chart-meta {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 11px;
  }
  .meta-title {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-weight: 700;
  }
  .meta-val {
    font-weight: 700;
    color: var(--text);
  }
  .chart-canvas-wrap {
    height: 70px;
    background: var(--bg);
    border: 1px solid var(--border);
    position: relative;
  }
  .chart-svg {
    width: 100%;
    height: 100%;
    display: block;
  }
  .grid-line {
    stroke: var(--border);
    stroke-dasharray: 4, 4;
    stroke-width: 1;
  }
  .line-path {
    fill: none;
    stroke-width: 1.8;
  }
  .line-path.cpu {
    stroke: var(--accent);
  }
  .line-path.mem {
    stroke: var(--ok);
  }
  .line-path.iops {
    stroke: var(--warn);
  }
  .line-path.net {
    stroke: #4a9eff;
  }
</style>
