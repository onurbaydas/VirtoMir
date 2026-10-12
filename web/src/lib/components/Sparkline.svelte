<script lang="ts">
  // Minimal live area chart (uPlot). Fixed 0..max scale so charts are comparable at a glance.
  import uPlot from 'uplot'
  import 'uplot/dist/uPlot.min.css'
  import { untrack } from 'svelte'
  import { XS } from '../live.svelte'
  import { theme } from '../theme.svelte'

  interface Props {
    data: (number | null)[]
    /** Change to redraw (data is mutated in place). */
    tick: number
    /** CSS custom property name, e.g. "--accent". */
    color?: string
    height?: number
    max?: number
  }
  let { data, tick, color = '--accent', height = 32, max = 100 }: Props = $props()

  let el: HTMLDivElement
  let plot: uPlot | undefined

  function rgba(hex: string, alpha: number): string {
    const h = hex.replace('#', '')
    const n = parseInt(h.length === 3 ? [...h].map((c) => c + c).join('') : h, 16)
    return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`
  }

  $effect(() => {
    void theme.resolved // re-create with the new palette when the theme changes
    const stroke = getComputedStyle(el).getPropertyValue(color).trim() || '#3ad1b6'
    const opts: uPlot.Options = {
      width: el.clientWidth || 120,
      height,
      padding: [3, 0, 2, 0], // keeps an idle (0 %) line visible
      legend: { show: false },
      cursor: { show: false },
      select: { show: false, left: 0, top: 0, width: 0, height: 0 },
      axes: [{ show: false }, { show: false }],
      scales: { x: { time: false }, y: { range: [0, max] } },
      series: [
        {},
        {
          stroke,
          width: 1.5,
          points: { show: false },
          fill: (u: uPlot) => {
            const g = u.ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height)
            g.addColorStop(0, rgba(stroke, 0.32))
            g.addColorStop(1, rgba(stroke, 0))
            return g
          },
        },
      ],
    }
    plot = new uPlot(opts, untrack(() => [XS, data]) as uPlot.AlignedData, el)
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
    plot?.setData([XS, data] as uPlot.AlignedData)
  })
</script>

<div class="spark" bind:this={el} style:height="{height}px" aria-hidden="true"></div>

<style>
  .spark {
    width: 100%;
    overflow: hidden;
  }
</style>
