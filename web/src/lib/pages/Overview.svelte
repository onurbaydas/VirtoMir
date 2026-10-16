<script lang="ts">
  import { session } from '../session.svelte'
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import Boxes from '@lucide/svelte/icons/boxes'
  import { isActive, live } from '../live.svelte'
  import { formatKiB, formatMHz, formatPercent, plural } from '../format'
  import StatCard from '../components/StatCard.svelte'
  import Sparkline from '../components/Sparkline.svelte'
  import IOChart from '../components/IOChart.svelte'
  import HistoryChart from '../components/HistoryChart.svelte'
  import RangeTabs, { type Range } from '../components/RangeTabs.svelte'
  import type { HistoryRange } from '../api'
  import Plus from '@lucide/svelte/icons/plus'
  import { router } from '../router.svelte'
  import HostChassisCard from '../components/HostChassisCard.svelte'
  import NoisyNeighborsPanel from '../components/NoisyNeighborsPanel.svelte'
  import CapacitySimulator from '../components/CapacitySimulator.svelte'

  let histRange = $state<Range>('24h')
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
  <div class="head-right">
    {#if live.streamOpen && snap?.connected}
      <span class="live" title="Updated every second"><span class="live-dot"></span>Live</span>
    {/if}
    {#if session.canCreate}<a class="ui-btn primary" href="/machines/new" onclick={router.link}><Plus size={15} /> New machine</a>{/if}
  </div>
</header>

<HostChassisCard hostname={host?.hostname ?? ''} />

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

{#if snap?.host.io || live.hostIO.diskRead.some((v) => v !== null)}
  <section class="ui-panel hostio">
    <IOChart
      title="Host disks"
      a={{ label: 'Read', data: live.hostIO.diskRead, color: '--accent' }}
      b={{ label: 'Write', data: live.hostIO.diskWrite, color: '--accent-2' }}
      tick={live.tick}
      height={90}
      floor={1024 * 1024}
    />
    <IOChart
      title="Host network"
      a={{ label: 'Receive', data: live.hostIO.netRx, color: '--accent' }}
      b={{ label: 'Send', data: live.hostIO.netTx, color: '--accent-2' }}
      tick={live.tick}
      height={90}
      floor={128 * 1024}
    />
  </section>
{/if}

<div class="workloads-banner ui-panel">
  <div class="wb-left">
    <Boxes size={22} />
    <div>
      <div class="wb-title mono">GUEST WORKLOADS // {domains.length} VIRTUAL MACHINES</div>
      <div class="wb-sub">
        {running.length} Running · {paused} Paused · {domains.length - running.length - paused} Stopped · {plural(vcpusInUse, 'vCPU')} in use
      </div>
    </div>
  </div>
  <a class="ui-btn primary mono" href="/machines" onclick={router.link}>
    <Boxes size={14} />
    <span>MANAGE VIRTUAL MACHINES ({domains.length}) →</span>
  </a>
</div>

<NoisyNeighborsPanel />

<CapacitySimulator />

<section class="ui-panel history">
  <header class="h-head">
    <h2>Host history</h2>
    <RangeTabs bind:value={histRange} live={false} />
  </header>
  <div class="h-grid">
    <HistoryChart
      title="CPU and memory"
      unit="percent"
      range={histRange as HistoryRange}
      lines={[
        { key: 'host.cpu', label: 'CPU', color: '--accent' },
        { key: 'host.mem', label: 'Memory', color: '--accent-2', dashed: true },
      ]}
    />
    <HistoryChart
      title="Disks"
      unit="bps"
      range={histRange as HistoryRange}
      lines={[
        { key: 'host.disk.read', label: 'Read', color: '--accent' },
        { key: 'host.disk.write', label: 'Write', color: '--accent-2', dashed: true },
      ]}
    />
    <HistoryChart
      title="Network"
      unit="bps"
      range={histRange as HistoryRange}
      lines={[
        { key: 'host.net.rx', label: 'Receive', color: '--accent' },
        { key: 'host.net.tx', label: 'Send', color: '--accent-2', dashed: true },
      ]}
    />
  </div>
</section>

<style>
  .workloads-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 16px 20px;
    margin-bottom: 20px;
    background: var(--surface-1);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 0 var(--border-strong);
  }
  .wb-left {
    display: flex;
    align-items: center;
    gap: 14px;
    color: var(--accent);
  }
  .wb-title {
    font-size: 13px;
    font-weight: 700;
    letter-spacing: 0.05em;
    color: var(--text-strong);
  }
  .wb-sub {
    font-size: 12px;
    color: var(--text-dim);
    margin-top: 2px;
  }
  .head-right {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .history {
    margin-top: 20px;
    padding: 14px 18px 18px;
  }
  .h-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
  }
  .h-head h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
  }
  .h-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 24px;
  }
  @media (max-width: 1100px) {
    .h-grid {
      grid-template-columns: 1fr;
    }
  }
  .hostio {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 24px;
    padding: 14px 18px;
    margin-bottom: 20px;
  }
  @media (max-width: 900px) {
    .hostio {
      grid-template-columns: 1fr;
    }
  }
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
    border-radius: 0;
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
