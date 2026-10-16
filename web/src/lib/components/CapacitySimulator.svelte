<script lang="ts">
  // "Would N more machines fit?" from real numbers: every reachable member's CPUs and memory,
  // what its running machines are given, and the free space of the storage pools. The N+1
  // check asks whether everything still fits when the biggest host is down.
  import Calculator from '@lucide/svelte/icons/calculator'
  import { api, onHost, type HostInfo, type Pool } from '../api'
  import { cluster } from '../cluster.svelte'
  import { live } from '../live.svelte'

  interface HostCap { name: string; cpus: number; memKiB: number; allocKiB: number; vcpus: number }
  let hosts = $state<HostCap[]>([])
  let storageFree = $state(0)
  let loaded = $state(false)

  let newVms = $state(4)
  let vcpuEach = $state(2)
  let ramEachGiB = $state(4)
  let diskEachGiB = $state(40)

  async function load() {
    const members = cluster.clustered ? cluster.members.filter((m) => m.reachable) : [{ id: '', name: live.host?.hostname ?? 'this host' }]
    const seen = new Set<string>()
    let free = 0
    const out = await Promise.all(
      members.map(async (m): Promise<HostCap | null> => {
        try {
          const [h, s, pools] = await Promise.all([
            m.id ? api.get<HostInfo>(onHost(m.id, '/api/v1/host')) : api.host(),
            m.id ? api.summary(m.id) : Promise.resolve(live.snapshot!),
            m.id ? api.poolsOn(m.id) : api.pools(),
          ])
          for (const p of pools as Pool[]) {
            if (!p.active) continue
            const shared = ['netfs', 'rbd', 'iscsi', 'gluster'].includes(p.type)
            const key = shared ? `${p.type}:${p.name}:${p.capacityBytes}` : `${m.id}:${p.name}`
            if (!seen.has(key)) {
              seen.add(key)
              free += p.availableBytes
            }
          }
          const on = (s?.domains ?? []).filter((d) => d.state === 'running' || d.state === 'paused')
          return { name: m.name, cpus: h.cpus, memKiB: h.memoryKiB, allocKiB: on.reduce((n, d) => n + d.maxMemoryKiB, 0), vcpus: on.reduce((n, d) => n + d.vcpus, 0) }
        } catch {
          return null
        }
      }),
    )
    hosts = out.filter((x): x is HostCap => !!x)
    storageFree = free
    loaded = true
  }
  $effect(() => {
    void cluster.members.length
    void load()
  })

  const GiB = 1024 * 1024
  let totCpus = $derived(hosts.reduce((n, h) => n + h.cpus, 0))
  let totMem = $derived(hosts.reduce((n, h) => n + h.memKiB, 0))
  let alloc = $derived(hosts.reduce((n, h) => n + h.allocKiB, 0))
  let vcpus = $derived(hosts.reduce((n, h) => n + h.vcpus, 0))
  let addMem = $derived(newVms * ramEachGiB * GiB)
  let addCpu = $derived(newVms * vcpuEach)
  let addDisk = $derived(newVms * diskEachGiB * 2 ** 30)
  let memPct = $derived(totMem ? ((alloc + addMem) / totMem) * 100 : 0)
  let ratio = $derived(totCpus ? (vcpus + addCpu) / totCpus : 0)
  let biggest = $derived(hosts.reduce((m, h) => Math.max(m, h.memKiB), 0))
  let nPlusOne = $derived(hosts.length > 1 && alloc + addMem <= totMem - biggest)
</script>

<section class="ui-panel cap">
  <header><h2><Calculator size={16} /> Would more machines fit?</h2>
    <span class="muted">{hosts.length} host{hosts.length === 1 ? '' : 's'} · from live numbers</span></header>
  <div class="inputs">
    <label class="ui-field"><span class="label">Machines</span><input class="ui-input" type="number" min="1" bind:value={newVms} /></label>
    <label class="ui-field"><span class="label">vCPUs each</span><input class="ui-input" type="number" min="1" bind:value={vcpuEach} /></label>
    <label class="ui-field"><span class="label">Memory each (GiB)</span><input class="ui-input" type="number" min="1" bind:value={ramEachGiB} /></label>
    <label class="ui-field"><span class="label">Disk each (GiB)</span><input class="ui-input" type="number" min="0" bind:value={diskEachGiB} /></label>
  </div>
  {#if loaded}
    <div class="out">
      <div class:bad={memPct > 100} class:warn={memPct > 85 && memPct <= 100}>
        <span class="lbl">Memory given to running machines</span>
        <b class="mono">{((alloc + addMem) / GiB).toFixed(0)} / {(totMem / GiB).toFixed(0)} GiB ({memPct.toFixed(0)} %)</b>
        <span class="muted">now {(alloc / GiB).toFixed(0)} GiB</span>
      </div>
      <div class:warn={ratio > 4}>
        <span class="lbl">vCPU : CPU</span>
        <b class="mono">{vcpus + addCpu} : {totCpus} ({ratio.toFixed(1)}:1)</b>
        <span class="muted">above 4:1 most workloads feel it</span>
      </div>
      <div class:bad={addDisk > storageFree}>
        <span class="lbl">Disk space</span>
        <b class="mono">{(addDisk / 2 ** 30).toFixed(0)} of {(storageFree / 2 ** 30).toFixed(0)} GiB free</b>
        <span class="muted">thin disks only take what they use</span>
      </div>
      {#if hosts.length > 1}
        <div class:bad={!nPlusOne} class:ok={nPlusOne}>
          <span class="lbl">One host down (N+1)</span>
          <b>{nPlusOne ? 'Everything still fits' : 'Would NOT fit without the biggest host'}</b>
          <span class="muted">{((totMem - biggest) / GiB).toFixed(0)} GiB left on the others</span>
        </div>
      {/if}
    </div>
  {:else}
    <p class="muted">Asking the hosts…</p>
  {/if}
</section>

<style>
  .cap { padding: 14px 16px; margin: 14px 0; }
  header { display: flex; justify-content: space-between; gap: 12px; align-items: baseline; margin-bottom: 10px; }
  h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .inputs { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 10px; }
  .out { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; margin-top: 12px; }
  .out > div { display: flex; flex-direction: column; gap: 2px; border-left: 3px solid var(--border); padding-left: 10px; }
  .out .ok { border-left-color: var(--ok); }
  .out .warn { border-left-color: var(--warn); }
  .out .bad { border-left-color: var(--danger); color: var(--danger); }
  .lbl { font: 600 11px var(--font-mono); text-transform: uppercase; color: var(--text-2); }
  .muted { color: var(--text-2); font-size: 12px; }
</style>
