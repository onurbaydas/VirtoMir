<script lang="ts">
  // The host's CPU layout as a map to pin by (P5): NUMA nodes, cores with their hardware
  // threads, each thread's load, and the running machines' vCPUs where they run right now.
  // Pick a machine to see where it is allowed to run (its pinning).
  import Network from '@lucide/svelte/icons/network'
  import { ent, type Topology } from '../enterprise'
  import { errorText } from '../api'
  import { formatKiB } from '../format'

  let t = $state<Topology | null>(null)
  let error = $state('')
  let picked = $state('')

  async function load() {
    try {
      t = await ent.topology()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
    const id = setInterval(() => void load(), 5_000)
    return () => clearInterval(id)
  })

  const PALETTE = ['var(--cobalt)', 'var(--cadmium)', 'var(--viridian)', 'var(--ochre)']
  let colour = $derived(new Map((t?.machines ?? []).map((m, i) => [m.uuid, PALETTE[i % PALETTE.length]])))

  /** vCPUs running on each host CPU: machine uuid per vCPU. */
  let onCPU = $derived.by(() => {
    const m = new Map<number, { uuid: string; name: string; vcpu: number }[]>()
    for (const d of t?.machines ?? [])
      d.vcpus.forEach((v, i) => {
        if (v.cpu >= 0) m.set(v.cpu, [...(m.get(v.cpu) ?? []), { uuid: d.uuid, name: d.name, vcpu: i }])
      })
    return m
  })

  let allowed = $derived.by(() => {
    const d = t?.machines.find((m) => m.uuid === picked)
    if (!d) return null
    const s = new Set<number>()
    for (const v of d.vcpus) for (const c of v.pinnedCpus ?? []) s.add(c)
    return s
  })

  /** Cores of a node: threads grouped by (socket, core). */
  function cores(node: number) {
    const m = new Map<string, Topology['cpus']>()
    for (const c of t?.cpus ?? []) {
      if (c.node !== node) continue
      const k = `${c.socket}/${c.core}/${c.siblings[0]}`
      m.set(k, [...(m.get(k) ?? []), c])
    }
    return [...m.values()]
  }

  // Contextual warnings: a machine whose vCPUs run on more than one NUMA node pays for
  // remote memory on every access.
  let warnings = $derived.by(() => {
    if (!t || t.nodes.length < 2) return []
    const nodeOf = new Map(t.cpus.map((c) => [c.id, c.node]))
    return t.machines
      .map((d) => ({ d, nodes: new Set(d.vcpus.filter((v) => v.cpu >= 0).map((v) => nodeOf.get(v.cpu))) }))
      .filter((x) => x.nodes.size > 1)
      .map((x) => `${x.d.name} runs on ${x.nodes.size} NUMA nodes: pin its vCPUs to one node (Settings → Performance tuning) to keep memory local.`)
  })

  const loadLevel = (p?: number) => (p === undefined ? '' : p >= 85 ? 'hot' : p >= 50 ? 'warm' : '')
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if t}
  <h2 class="sub"><Network size={16} /> CPU and memory layout</h2>
  {#each warnings as w (w)}
    <p class="warn">{w}</p>
  {/each}
  {#if t.machines.length}
    <div class="legend" role="radiogroup" aria-label="Show the pinning of">
      {#each t.machines as m (m.uuid)}
        <button role="radio" aria-checked={picked === m.uuid} class:on={picked === m.uuid} onclick={() => (picked = picked === m.uuid ? '' : m.uuid)}>
          <i style:background={colour.get(m.uuid)}></i>{m.name}
          <span class="mono">{m.vcpus.length} vCPU{m.vcpus.length === 1 ? '' : 's'}</span>
        </button>
      {/each}
    </div>
    {#if picked && allowed && !allowed.size}
      <p class="hint">Not pinned: its vCPUs may run on any host CPU.</p>
    {/if}
  {/if}
  <div class="nodes">
    {#each t.nodes as n (n.id)}
      <section class="ui-panel node">
        <header>
          <strong>Node {n.id}</strong>
          <span class="mono">{n.cpus.length} CPUs</span>
          {#if n.memTotalKiB}
            <span class="mono mem">{formatKiB(n.memFreeKiB)} free of {formatKiB(n.memTotalKiB)}</span>
            <span class="bar"><span style:width="{(100 * (n.memTotalKiB - n.memFreeKiB)) / n.memTotalKiB}%"></span></span>
          {/if}
        </header>
        <div class="cores">
          {#each cores(n.id) as threads (threads[0].id)}
            <div class="core" title="Socket {threads[0].socket}, core {threads[0].core}">
              {#each threads as c (c.id)}
                {@const here = onCPU.get(c.id) ?? []}
                <div
                  class="cpu {loadLevel(c.loadPercent)}"
                  class:off={!c.online}
                  class:allowed={allowed?.has(c.id)}
                  class:dim={allowed && allowed.size > 0 && !allowed.has(c.id)}
                  title="CPU {c.id}{c.loadPercent !== undefined ? ` · ${Math.round(c.loadPercent)}% busy` : ''}{here.length ? ` · ${here.map((h) => `${h.name} vCPU ${h.vcpu}`).join(', ')}` : ''}"
                >
                  <span class="fill" style:height="{c.loadPercent ?? 0}%"></span>
                  <span class="id mono">{c.id}</span>
                  <span class="vms">
                    {#each here as h (h.uuid + h.vcpu)}<i style:background={colour.get(h.uuid)} class:picked={h.uuid === picked}></i>{/each}
                  </span>
                </div>
              {/each}
            </div>
          {/each}
        </div>
      </section>
    {/each}
  </div>
{/if}

<style>
  .sub {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 18px 0 10px;
    font-size: 15px;
  }
  .warn {
    margin: 0 0 8px;
    padding: 8px 12px;
    border: 2px solid var(--warn);
    font-size: 13px;
  }
  .hint {
    margin: 0 0 8px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 10px;
  }
  .legend button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    font-size: 12.5px;
    cursor: pointer;
  }
  .legend button.on {
    background: var(--invert-bg);
    color: var(--invert-fg);
  }
  .legend i {
    width: 10px;
    height: 10px;
  }
  .legend .mono {
    color: inherit;
    opacity: 0.7;
    font-size: 11px;
  }
  .nodes {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 10px;
  }
  .node header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--border);
    font-size: 12.5px;
  }
  .node header .mono {
    color: var(--text-3);
  }
  .mem {
    margin-left: auto;
  }
  .bar {
    width: 100%;
    height: 4px;
    background: var(--surface-2);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--text);
  }
  .cores {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 12px 14px;
  }
  .core {
    display: flex;
    gap: 2px;
    padding: 2px;
    border: 1px solid var(--border);
  }
  .cpu {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    width: 34px;
    height: 40px;
    padding: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .fill {
    position: absolute;
    inset: auto 0 0 0;
    background: color-mix(in srgb, var(--ok) 35%, transparent);
  }
  .cpu.warm .fill {
    background: color-mix(in srgb, var(--warn) 45%, transparent);
  }
  .cpu.hot .fill {
    background: color-mix(in srgb, var(--danger) 50%, transparent);
  }
  .cpu.off {
    opacity: 0.35;
  }
  .cpu.allowed {
    outline: 2px solid var(--text);
    outline-offset: -2px;
  }
  .cpu.dim {
    opacity: 0.45;
  }
  .id {
    position: relative;
    font-size: 10.5px;
    color: var(--text-2);
  }
  .vms {
    position: relative;
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
  }
  .vms i {
    width: 7px;
    height: 7px;
  }
  .vms i.picked {
    outline: 1px solid var(--text);
  }
</style>
