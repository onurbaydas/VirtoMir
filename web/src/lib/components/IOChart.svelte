<script lang="ts">
  // Live two-series throughput chart (e.g. read/write, receive/send) over the last minute.
  // Auto-scales, but never below `floor`, so idle noise does not look like a spike.
  import uPlot from 'uplot'
  import 'uplot/dist/uPlot.min.css'
  import { untrack } from 'svelte'
  import { XS } from '../live.svelte'
  import { theme } from '../theme.svelte'
  import { formatBps } from '../format'

  interface Series {
    label: string
    data: (number | null)[]
    /** CSS custom property, e.g. "--accent". */
    color: string
  }
  interface Props {
    title: string
    a: Series
    b: Series
    /** Change to redraw (data is mutated in place). */
    tick: number
    height?: number
    /** Minimum top of the y scale, bytes/s. */
    floor?: number
    /** Shown instead of the chart when there is no data at all. */
    empty?: string
  }
  let { title, a, b, tick, height = 120, floor = 64 * 1024, empty = 'No data yet' }: Props = $props()

  let el: HTMLDivElement
  let plot: uPlot | undefined

  const last = (d: (number | null)[]) => {
    for (let i = d.length - 1; i >= 0; i--) if (d[i] !== null) return d[i] as number
    return null
  }
  let aNow = $derived((void tick, last(a.data)))
  let bNow = $derived((void tick, last(b.data)))
  let hasData = $derived(aNow !== null || bNow !== null)

  function css(name: string) {
    return getComputedStyle(el).getPropertyValue(name).trim()
  }

  $effect(() => {
    void theme.resolved
    if (!el) return
    const [ca, cb, grid, text] = [css(a.color), css(b.color), css('--border'), css('--text-3')]
    const opts: uPlot.Options = {
      width: el.clientWidth || 300,
      height,
      padding: [8, 4, 0, 0],
      legend: { show: false },
      cursor: { show: false },
      select: { show: false, left: 0, top: 0, width: 0, height: 0 },
      scales: {
        x: { time: false },
        y: { range: (_u, _min, max) => [0, Math.max(max * 1.15, floor)] },
      },
      axes: [
        { show: false },
        {
          stroke: text,
          font: '10.5px ' + css('--font-mono'),
          size: 62,
          gap: 4,
          ticks: { show: false },
          grid: { stroke: grid, width: 1 },
          splits: (u) => {
            const top = u.scales.y.max ?? floor
            return [0, top / 2, top]
          },
          values: (_u, splits) => splits.map((v) => (v === 0 ? '0' : formatBps(v))),
        },
      ],
      series: [
        {},
        { stroke: ca, width: 1.5, points: { show: false }, fill: ca + '22' },
        { stroke: cb, width: 1.5, points: { show: false }, dash: [4, 3] },
      ],
    }
    plot = new uPlot(opts, untrack(() => [XS, a.data, b.data]) as uPlot.AlignedData, el)
    const ro = new ResizeObserver(() => plot?.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot?.destroy()
      plot = undefined
    }
  })

  $effect(() => {
    void tick
    plot?.setData([XS, a.data, b.data] as uPlot.AlignedData)
  })
</script>

<div class="io">
  <header>
    <span class="title">{title}</span>
    <span class="legend">
      <span class="key" style:--c="var({a.color})"><i></i>{a.label} <b class="mono">{aNow === null ? '—' : formatBps(aNow)}</b></span>
      <span class="key dashed" style:--c="var({b.color})"><i></i>{b.label} <b class="mono">{bNow === null ? '—' : formatBps(bNow)}</b></span>
    </span>
  </header>
  <div class="wrap">
    <div class="plot" bind:this={el} style:height="{height}px" aria-hidden="true"></div>
    {#if !hasData}<div class="empty">{empty}</div>{/if}
  </div>
</div>

<style>
  .io {
    min-width: 0;
  }
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 6px;
  }
  .title {
    font-size: 13px;
    font-weight: 600;
  }
  .legend {
    display: flex;
    gap: 14px;
    flex-wrap: wrap;
  }
  .key {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
    font-size: 12px;
  }
  .key i {
    width: 12px;
    height: 0;
    border-top: 2px solid var(--c);
  }
  .key.dashed i {
    border-top-style: dashed;
  }
  .key b {
    color: var(--text);
    font-weight: 500;
    font-size: 12px;
  }
  .plot {
    width: 100%;
    overflow: hidden;
  }
  .wrap {
    position: relative;
  }
  .empty {
    position: absolute;
    inset: 0 0 0 66px;
    display: grid;
    background: var(--surface);
    place-items: center;
    border: 1px dashed var(--border);
    border-radius: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
</style>
