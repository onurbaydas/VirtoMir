<script lang="ts">
  // Metric history from virtomird (GET /api/v1/history) with a time axis and a hover readout.
  // Refreshes once per step so the newest bucket keeps filling in.
  import uPlot from 'uplot'
  import 'uplot/dist/uPlot.min.css'
  import { api, errorText, type HistoryRange, type HistoryResult } from '../api'
  import { theme } from '../theme.svelte'
  import { formatBps, formatPercent } from '../format'

  interface Line {
    key: string
    label: string
    /** CSS custom property, e.g. "--accent". */
    color: string
    dashed?: boolean
    /** Cluster member to read this line from (default: the host being shown). */
    host?: string
  }
  interface Props {
    title: string
    lines: Line[]
    range: HistoryRange
    unit: 'percent' | 'bps'
    height?: number
  }
  let { title, lines, range, unit, height = 150 }: Props = $props()

  let el = $state<HTMLDivElement>()
  let data = $state.raw<HistoryResult | null>(null)
  let error = $state('')
  let hover = $state<{ t: number; vals: (number | null)[] } | null>(null)

  const fmt = (v: number | null | undefined) =>
    v === null || v === undefined ? '—' : unit === 'percent' ? `${formatPercent(v)} %` : formatBps(v)

  /** Series key inside data: lines from other cluster members are kept apart. */
  const id = (l: Line) => (l.host ? `${l.host}|${l.key}` : l.key)

  async function load() {
    try {
      // One request per host; their buckets are aligned to the first answer's time axis.
      const hosts = [...new Set(lines.map((l) => l.host))]
      const parts = await Promise.all(
        hosts.map(async (h) => ({ h, r: await api.history(lines.filter((l) => l.host === h).map((l) => l.key), range, h) })),
      )
      const base = parts[0].r
      const len = Object.values(base.series)[0]?.length ?? 0
      const series: Record<string, (number | null)[]> = {}
      for (const { h, r } of parts) {
        const shift = Math.round((r.start - base.start) / base.step)
        for (const [k, vals] of Object.entries(r.series)) {
          const out = Array<number | null>(len).fill(null)
          vals.forEach((v, i) => {
            if (i + shift >= 0 && i + shift < len) out[i + shift] = v
          })
          series[h ? `${h}|${k}` : k] = out
        }
      }
      data = { start: base.start, step: base.step, series }
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void range
    void lines.map(id).join()
    void load()
    const every = { '1h': 30_000, '6h': 30_000, '24h': 60_000, '7d': 300_000, '30d': 900_000 }[range]
    const t = setInterval(load, every)
    return () => clearInterval(t)
  })

  let stats = $derived.by(() =>
    lines.map((l) => {
      const vals = (data?.series[id(l)] ?? []).filter((v): v is number => v !== null)
      return vals.length
        ? { avg: vals.reduce((a, b) => a + b, 0) / vals.length, max: Math.max(...vals), any: true }
        : { avg: 0, max: 0, any: false }
    }),
  )
  let empty = $derived(!!data && stats.every((s) => !s.any))

  function css(name: string) {
    return el ? getComputedStyle(el).getPropertyValue(name).trim() : ''
  }

  $effect(() => {
    void theme.resolved
    if (!el || !data) return
    const d = data
    const xs = Array.from({ length: d.series[id(lines[0])]?.length ?? 0 }, (_, i) => d.start + i * d.step)
    const ys = lines.map((l) => d.series[id(l)] ?? xs.map(() => null))
    const [grid, text] = [css('--border'), css('--text-3')]
    const font = '10.5px ' + css('--font-mono')
    const opts: uPlot.Options = {
      width: el.clientWidth || 400,
      height,
      padding: [8, 8, 0, 0],
      legend: { show: false },
      cursor: { points: { show: false }, drag: { x: false, y: false } },
      select: { show: false, left: 0, top: 0, width: 0, height: 0 },
      scales: {
        x: { time: true },
        y: unit === 'percent' ? { range: [0, 100] } : { range: (_u, _min, max) => [0, Math.max(max * 1.15, 64 * 1024)] },
      },
      axes: [
        {
          stroke: text,
          font,
          grid: { show: false },
          ticks: { stroke: grid, size: 4 },
          space: 80,
          // One short line per tick: times within a day, dates for longer ranges.
          values: (_u, splits) =>
            splits.map((t) =>
              new Date(t * 1000).toLocaleString(
                undefined,
                range === '7d' || range === '30d' ? { month: 'short', day: 'numeric' } : { hour: '2-digit', minute: '2-digit' },
              ),
            ),
        },
        {
          stroke: text,
          font,
          size: 64,
          gap: 4,
          ticks: { show: false },
          grid: { stroke: grid, width: 1 },
          splits: (u) => {
            const top = u.scales.y.max ?? 100
            return [0, top / 2, top]
          },
          values: (_u, splits) => splits.map((v) => (v === 0 ? '0' : unit === 'percent' ? `${Math.round(v)}%` : formatBps(v))),
        },
      ],
      series: [
        {},
        ...lines.map((l, i) => {
          const c = css(l.color)
          return {
            stroke: c,
            width: 1.5,
            points: { show: false },
            dash: l.dashed ? [4, 3] : undefined,
            fill: i === 0 ? c + '22' : undefined,
            spanGaps: false,
          }
        }),
      ],
      hooks: {
        setCursor: [
          (u) => {
            const i = u.cursor.idx
            hover = i === null || i === undefined ? null : { t: xs[i], vals: ys.map((s) => s[i] ?? null) }
          },
        ],
      },
    }
    const plot = new uPlot(opts, [xs, ...ys] as uPlot.AlignedData, el)
    const ro = new ResizeObserver(() => el && plot.setSize({ width: el.clientWidth, height }))
    ro.observe(el)
    return () => {
      ro.disconnect()
      plot.destroy()
    }
  })

  const when = (t: number) =>
    new Date(t * 1000).toLocaleString(undefined, range === '7d' || range === '30d' ? { dateStyle: 'short', timeStyle: 'short' } : { timeStyle: 'short' })
</script>

<div class="hist">
  <header>
    <span class="title">{title}</span>
    <span class="legend">
      {#each lines as l, i (id(l))}
        <span class="key" class:dashed={l.dashed} style:--c="var({l.color})">
          <i></i>{l.label}
          <b class="mono">{hover ? fmt(hover.vals[i]) : stats[i]?.any ? `avg ${fmt(stats[i].avg)} · max ${fmt(stats[i].max)}` : '—'}</b>
        </span>
      {/each}
      {#if hover}<span class="when mono">{when(hover.t)}</span>{/if}
    </span>
  </header>
  {#if error}
    <div class="msg err" style:height="{height}px">{error}</div>
  {:else}
    <div class="wrap">
      <div class="plot" bind:this={el} style:height="{height}px"></div>
      {#if !data}<div class="msg">Loading…</div>{:else if empty}<div class="msg">No data for this period yet — history starts when VirtoMir sees the machine running.</div>{/if}
    </div>
  {/if}
</div>

<style>
  .hist {
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
    align-items: baseline;
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
    border-top: 2px solid var(--c);
  }
  .key.dashed i {
    border-top-style: dashed;
  }
  .key b {
    color: var(--text);
    font-weight: 500;
  }
  .when {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .wrap {
    position: relative;
  }
  .plot {
    width: 100%;
    overflow: hidden;
  }
  .msg {
    position: absolute;
    inset: 0 0 18px 68px;
    display: grid;
    place-items: center;
    padding: 0 16px;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text-3);
    font-size: 12.5px;
    text-align: center;
  }
  .msg.err {
    position: static;
    color: var(--danger);
  }
</style>
