<script lang="ts">
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import Boxes from '@lucide/svelte/icons/boxes'
  import { isActive, live } from '../live.svelte'
  import { formatKiB, formatMHz, formatPercent, plural } from '../format'
  import StatCard from '../components/StatCard.svelte'
  import Sparkline from '../components/Sparkline.svelte'
  import MachineList from '../components/MachineList.svelte'

  let snap = $derived(live.snapshot)
  let host = $derived(live.host)
  let loading = $derived(!snap || !snap.connected || !host)

  let memUsed = $derived(snap?.host.memUsedKiB ?? 0)
  let memTotal = $derived(snap?.host.memTotalKiB ?? 0)
  let memPct = $derived(memTotal ? (memUsed / memTotal) * 100 : 0)

  let domains = $derived(snap?.domains ?? [])
  let running = $derived(domains.filter((d) => isActive(d.state)))
  let paused = $derived(domains.filter((d) => d.state === 'paused').length)
  let vcpusInUse = $derived(running.reduce((n, d) => n + d.vcpus, 0))
  let memAllocated = $derived(running.reduce((n, d) => n + d.memoryKiB, 0))

  let topology = $derived(
    host
      ? `${plural(host.cpus, 'thread')} · ${plural(host.sockets, 'socket')} × ${plural(host.coresPerSocket, 'core')} · ${formatMHz(host.cpuMHz)}`
      : '',
  )
</script>

<svelte:head><title>{host?.hostname ? `${host.hostname} · VirtoMir` : 'VirtoMir'}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{host?.hostname ?? 'Overview'}</h1>
    {#if host}
      <div class="chips">
        <span class="chip mono" title="Hypervisor">{host.hypervisor} {host.hypervisorVersion}</span>
        <span class="chip mono" title="libvirt">libvirt {host.libvirtVersion}</span>
        <span class="chip mono" title="CPU architecture">{host.cpuModel}</span>
        {#if host.numaNodes > 1}<span class="chip mono">{plural(host.numaNodes, 'NUMA node')}</span>{/if}
      </div>
    {/if}
  </div>
  {#if live.streamOpen && snap?.connected}
    <span class="live" title="Updated every second"><span class="live-dot"></span>Live</span>
  {/if}
</header>

<section class="stats">
  <StatCard label="Host CPU" icon={Cpu} value={formatPercent(snap?.host.cpuPercent ?? 0)} unit="%" sub={topology} {loading}>
    {#snippet chart()}<Sparkline data={live.hostCpu} tick={live.tick} height={72} />{/snippet}
  </StatCard>

  <StatCard
    label="Host memory"
    icon={MemoryStick}
    value={formatKiB(memUsed).split(' ')[0]}
    unit="{formatKiB(memUsed).split(' ')[1]} of {formatKiB(memTotal)}"
    sub="{formatPercent(memPct)} % in use · {formatKiB(memAllocated)} assigned to running machines"
    {loading}
  >
    {#snippet chart()}<Sparkline data={live.hostMem} tick={live.tick} height={72} color="--accent-2" />{/snippet}
  </StatCard>

  <StatCard
    label="Machines"
    icon={Boxes}
    value={String(running.length)}
    unit="of {domains.length} running"
    sub="{plural(vcpusInUse, 'vCPU')} in use{paused ? ` · ${paused} paused` : ''}"
    loading={!snap}
  >
    {#snippet chart()}
      <div class="fleet" aria-hidden="true">
        {#each domains as d (d.uuid)}
          <span
            class="cell"
            class:on={isActive(d.state)}
            class:paused={d.state === 'paused'}
            class:crashed={d.state === 'crashed'}
            title="{d.name}: {d.state}"
          ></span>
        {/each}
      </div>
    {/snippet}
  </StatCard>
</section>

<MachineList />

<style>
  .stats {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 16px;
    margin-bottom: 20px;
  }
  .fleet {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
    padding: 0 18px 18px;
  }
  .cell {
    width: 14px;
    height: 14px;
    border-radius: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    transition: background var(--dur);
  }
  .cell.on {
    background: var(--ok);
    border-color: transparent;
  }
  .cell.paused {
    background: var(--warn);
    border-color: transparent;
  }
  .cell.crashed {
    background: var(--danger);
    border-color: transparent;
  }
  @media (max-width: 1100px) {
    .stats {
      grid-template-columns: 1fr 1fr;
    }
  }
  @media (max-width: 700px) {
    .stats {
      grid-template-columns: 1fr;
    }
  }
</style>
