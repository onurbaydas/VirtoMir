<script lang="ts">
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import Boxes from '@lucide/svelte/icons/boxes'
  import { onMount } from 'svelte'
  import { isActive, live } from './lib/live.svelte'
  import { formatKiB, formatMHz, formatPercent, plural } from './lib/format'
  import Sidebar from './lib/components/Sidebar.svelte'
  import StatCard from './lib/components/StatCard.svelte'
  import Sparkline from './lib/components/Sparkline.svelte'
  import MachineList from './lib/components/MachineList.svelte'
  import ConnectionBanner from './lib/components/ConnectionBanner.svelte'
  import Toasts from './lib/components/Toasts.svelte'

  let version = $state('')
  onMount(() => {
    live.start()
    fetch('/api/v1/version')
      .then((r) => r.json())
      .then((v) => (version = v.version))
      .catch(() => {})
  })

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

<div class="app">
  <Sidebar {version} />

  <main>
    <ConnectionBanner />

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
  </main>
</div>

<Toasts />

<style>
  .app {
    display: flex;
    min-height: 100vh;
  }
  main {
    flex: 1;
    min-width: 0;
    max-width: 1440px;
    padding: 28px 32px 48px;
  }
  .page-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 22px;
  }
  h1 {
    margin: 0 0 8px;
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.02em;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    padding: 3px 9px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
    color: var(--text-2);
    font-size: 11.5px;
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 5px 11px;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 12px;
    font-weight: 600;
  }
  .live-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
    animation: blink 2s ease-in-out infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0.35;
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
    main {
      padding: 20px 16px 40px;
    }
    .stats {
      grid-template-columns: 1fr;
    }
  }
</style>
