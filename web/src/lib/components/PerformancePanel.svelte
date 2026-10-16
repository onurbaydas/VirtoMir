<script lang="ts">
  // Live performance of one machine: disk and network throughput (1 min), per-vCPU load
  // and per-device rates. Data comes from the 1 s snapshot stream.
  import Activity from '@lucide/svelte/icons/activity'
  import type { Device, Domain } from '../api'
  import { isActive, live } from '../live.svelte'
  import { formatBps, formatKiB, formatPercent, formatRate } from '../format'
  import IOChart from './IOChart.svelte'
  import HistoryChart from './HistoryChart.svelte'
  import RangeTabs, { type Range } from './RangeTabs.svelte'

  let { d, devices }: { d: Domain; devices: Device[] } = $props()

  let active = $derived(isActive(d.state))
  let range = $state<Range>('live')
  const vm = (m: string) => `vm.${d.uuid}.${m}`
  let io = $derived(live.domainIO(d.uuid))

  // Friendly names: "vda" → "Disk vda", "vnet2" → the adapter that uses it.
  function diskTitle(name: string) {
    return devices.find((x) => (x.kind === 'disk' || x.kind === 'cdrom') && x.title.endsWith(' ' + name))?.title ?? name
  }
  function netTitle(name: string) {
    const nic = devices.find((x) => x.kind === 'interface' && x.props.some((p) => p.tech === 'target@dev' && p.value === name))
    return nic ? `${nic.title} · ${nic.summary ?? name}` : name
  }

  const latency = (ms?: number) => (ms === undefined ? '—' : ms < 1 ? `${ms.toFixed(2)} ms` : `${ms.toFixed(1)} ms`)
</script>

<section class="perf">
  <header class="head">
    <h2><Activity size={15} /> Performance</h2>
    <span class="right">
      {#if range === 'live' && active}<span class="hint">updated every second</span>{/if}
      <RangeTabs bind:value={range} />
    </span>
  </header>

  {#if range !== 'live'}
    <div class="charts">
      <div class="full">
      <HistoryChart
        title="CPU and memory"
        unit="percent"
        {range}
        lines={[
          { key: vm('cpu'), label: 'CPU', color: '--accent' },
          { key: vm('mem'), label: 'Guest memory', color: '--accent-2', dashed: true },
        ]}
      />
      </div>
      <HistoryChart
        title="Disk"
        unit="bps"
        {range}
        lines={[
          { key: vm('disk.read'), label: 'Read', color: '--accent' },
          { key: vm('disk.write'), label: 'Write', color: '--accent-2', dashed: true },
        ]}
      />
      <HistoryChart
        title="Network"
        unit="bps"
        {range}
        lines={[
          { key: vm('net.rx'), label: 'Receive', color: '--accent' },
          { key: vm('net.tx'), label: 'Send', color: '--accent-2', dashed: true },
        ]}
      />
    </div>
  {:else if !active}
    <p class="off">Live data appears while the machine is running. Pick a range above to see its history.</p>
  {:else}
    <div class="charts">
      <IOChart
        title="Disk"
        a={{ label: 'Read', data: io.diskRead, color: '--accent' }}
        b={{ label: 'Write', data: io.diskWrite, color: '--accent-2' }}
        tick={live.tick}
        empty={d.disks === undefined ? 'Collecting…' : 'No disks'}
      />
      <IOChart
        title="Network"
        a={{ label: 'Receive', data: io.netRx, color: '--accent' }}
        b={{ label: 'Send', data: io.netTx, color: '--accent-2' }}
        tick={live.tick}
        empty={d.nets === undefined ? 'Collecting…' : 'No network adapters'}
      />
    </div>

    {#if d.vcpuPercent?.length}
      <div class="block">
        <h3>Per vCPU</h3>
        <div class="vcpus">
          {#each d.vcpuPercent as p, i (i)}
            <div class="vcpu" title="vCPU {i}: {formatPercent(p)} %">
              <div class="bar"><span style:height="{Math.max(p, 1.5)}%" class:hot={p >= 90}></span></div>
              <span class="pct mono">{Math.round(p)}</span>
              <span class="idx mono">{i}</span>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if d.disks?.length || d.nets?.length}
      <div class="block">
        <h3>Devices</h3>
        <div class="table" role="table" aria-label="Per-device rates">
          {#each d.disks ?? [] as x (x.name)}
            <div class="row" role="row">
              <span class="dev" role="cell">{diskTitle(x.name)}</span>
              <span class="cell" role="cell">
                <small>read</small><b class="mono">{formatBps(x.readBps)}</b>
                <small class="mono">{formatRate(x.readIops)} IOPS · {latency(x.readLatencyMs)}</small>
              </span>
              <span class="cell" role="cell">
                <small>write</small><b class="mono">{formatBps(x.writeBps)}</b>
                <small class="mono">{formatRate(x.writeIops)} IOPS · {latency(x.writeLatencyMs)}</small>
              </span>
            </div>
          {/each}
          {#each d.nets ?? [] as x (x.name)}
            <div class="row" role="row">
              <span class="dev" role="cell">{netTitle(x.name)}</span>
              <span class="cell" role="cell">
                <small>receive</small><b class="mono">{formatBps(x.rxBps)}</b>
                <small class="mono">{formatRate(x.rxPps)} pkt/s</small>
              </span>
              <span class="cell" role="cell">
                <small>send</small><b class="mono">{formatBps(x.txBps)}</b>
                <small class="mono">
                  {formatRate(x.txPps)} pkt/s
                  {#if x.problemsPs}<span class="bad" title="Dropped or errored packets per second">· {formatRate(x.problemsPs)} dropped/s</span>{/if}
                </small>
              </span>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if d.hostRssKiB || d.guestMemTotalKiB}
      <div class="mem-note">
        {#if d.guestMemTotalKiB}
          Guest reports <b>{formatKiB(d.guestMemUsedKiB ?? 0)}</b> in use of {formatKiB(d.guestMemTotalKiB)}.
        {/if}
        {#if d.hostRssKiB}
          The VM process uses <b>{formatKiB(d.hostRssKiB)}</b> of host memory.
        {/if}
      </div>
    {/if}
  {/if}
</section>

<style>
  .perf {
    padding: 16px 18px;
    margin-bottom: 16px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin: -16px -18px 14px;
    padding: 6px 10px 6px 18px;
    border-bottom: var(--frame) solid var(--border-strong);
    background: var(--metal-head);
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 14px;
    font-weight: 700;
  }
  h2 :global(svg) {
    color: var(--accent);
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .full {
    grid-column: 1 / -1;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .off {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 24px;
  }
  .block {
    margin-top: 16px;
    padding-top: 14px;
    border-top: 1px solid var(--border);
  }
  h3 {
    margin: 0 0 10px;
    color: var(--text-2);
    font-size: 12px;
    font-weight: 600;
  }
  .vcpus {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .vcpu {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 3px;
    width: 34px;
  }
  .bar {
    position: relative;
    width: 14px;
    height: 56px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    background: var(--accent);
    transition: height var(--dur) var(--ease-out);
  }
  .bar span.hot {
    background: var(--warn);
  }
  .pct {
    font-size: 11.5px;
    color: var(--text);
  }
  .idx {
    font-size: 10.5px;
    color: var(--text-3);
  }
  .table {
    display: grid;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(160px, 1.2fr) 1fr 1fr;
    gap: 16px;
    align-items: center;
    padding: 8px 0;
    border-top: 1px solid var(--border);
  }
  .row:first-child {
    border-top: 0;
  }
  .dev {
    font-size: 13px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cell {
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: 8px;
    align-items: baseline;
  }
  .cell small {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .cell small:last-child {
    grid-column: 1 / -1;
  }
  .cell b {
    font-size: 13px;
    font-weight: 500;
  }
  .bad {
    color: var(--danger);
  }
  .mem-note {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    font-size: 12.5px;
  }
  .mem-note b {
    color: var(--text);
    font-weight: 500;
  }
  @media (max-width: 900px) {
    .charts {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 600px) {
    .row {
      grid-template-columns: 1fr 1fr;
    }
    .dev {
      grid-column: 1 / -1;
    }
  }
</style>
