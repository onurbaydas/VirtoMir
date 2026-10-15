<script lang="ts">
  import Calculator from '@lucide/svelte/icons/calculator'
  import CheckCircle from '@lucide/svelte/icons/check-circle'
  import AlertTriangle from '@lucide/svelte/icons/triangle-alert'
  import TrendingUp from '@lucide/svelte/icons/trending-up'
  import { live } from '../live.svelte'

  let newVms = $state(8)
  let vcpuEach = $state(4)
  let ramEachGiB = $state(16)
  let diskEachGiB = $state(100)

  // Cluster Host Hardware Specs (Total Capacity & In Use)
  let totalCores = $derived((live.host?.cpus ?? 64) * 3) // 3-node cluster assumption
  let totalRamGiB = $derived(Math.round(((live.snapshot?.host.memTotalKiB ?? 256 * 1024 * 1024) * 3) / 1024 / 1024))
  let totalStorageTiB = 48

  let curCoresUsed = $derived((live.snapshot?.domains ?? []).reduce((n, d) => n + d.vcpus, 0))
  let curRamUsedGiB = $derived(Math.round((live.snapshot?.host.memUsedKiB ?? 64 * 1024 * 1024) / 1024 / 1024 * 3))
  let curStorageUsedTiB = 18.4

  // Simulated additions
  let simCores = $derived(newVms * vcpuEach)
  let simRamGiB = $derived(newVms * ramEachGiB)
  let simStorageTiB = $derived((newVms * diskEachGiB) / 1024)

  // Post-simulation totals & percentages
  let postCores = $derived(curCoresUsed + simCores)
  let postCoresPct = $derived(Math.min(100, Math.round((postCores / (totalCores * 3)) * 100))) // 3:1 vCPU overcommit ratio
  let postRamGiB = $derived(curRamUsedGiB + simRamGiB)
  let postRamPct = $derived(Math.min(100, Math.round((postRamGiB / totalRamGiB) * 100)))
  let postStorageTiB = $derived(curStorageUsedTiB + simStorageTiB)
  let postStoragePct = $derived(Math.min(100, Math.round((postStorageTiB / totalStorageTiB) * 100)))

  // N+1 Failover safety: If 1 node fails (loss of 1/3 capacity), can remaining nodes hold post workloads?
  let nPlusOneCapacityRam = $derived(totalRamGiB * (2 / 3))
  let nPlusOnePass = $derived(postRamGiB < nPlusOneCapacityRam)

  // Saturation days calculation
  let daysToFull = $derived(Math.max(12, Math.floor((100 - Math.max(postRamPct, postStoragePct)) * 4.2)))
</script>

<div class="capacity-simulator ui-panel">
  <div class="panel-header mono">
    <div class="title-wrap">
      <Calculator size={15} />
      <span>CAPACITY FORECASTING & "WHAT-IF" SIMULATOR</span>
    </div>
    <span class="forecast-badge">N+1 HIGH AVAILABILITY PREDICTOR</span>
  </div>

  <div class="simulator-body">
    <!-- Controls Matrix -->
    <div class="inputs-grid mono">
      <label class="sim-field">
        <span class="lbl">PLANNED WORKLOADS:</span>
        <div class="input-unit">
          <input type="number" min="1" max="100" bind:value={newVms} class="sim-input" />
          <span class="unit">VMs</span>
        </div>
      </label>

      <label class="sim-field">
        <span class="lbl">VCPU PER VM:</span>
        <div class="input-unit">
          <input type="number" min="1" max="64" bind:value={vcpuEach} class="sim-input" />
          <span class="unit">Cores</span>
        </div>
      </label>

      <label class="sim-field">
        <span class="lbl">RAM PER VM:</span>
        <div class="input-unit">
          <input type="number" min="1" max="512" bind:value={ramEachGiB} class="sim-input" />
          <span class="unit">GiB</span>
        </div>
      </label>

      <label class="sim-field">
        <span class="lbl">PROVISIONED DISK:</span>
        <div class="input-unit">
          <input type="number" min="10" max="4000" bind:value={diskEachGiB} class="sim-input" />
          <span class="unit">GiB</span>
        </div>
      </label>
    </div>

    <!-- Projected Impact & Headroom Gauges -->
    <div class="projections-deck mono">
      <!-- vCPU Projection -->
      <div class="gauge-card">
        <div class="gauge-header">
          <span class="g-title">VCPU COMMITMENT</span>
          <span class="g-val">{postCores} / {totalCores * 3} ({postCoresPct}%)</span>
        </div>
        <div class="gauge-track">
          <div class="gauge-fill" style="width: {postCoresPct}%;"></div>
        </div>
        <span class="g-sub">+{simCores} vCPUs planned · 3:1 Overcommit Envelope</span>
      </div>

      <!-- RAM Projection -->
      <div class="gauge-card">
        <div class="gauge-header">
          <span class="g-title">PHYSICAL MEMORY</span>
          <span class="g-val">{postRamGiB} / {totalRamGiB} GiB ({postRamPct}%)</span>
        </div>
        <div class="gauge-track">
          <div class="gauge-fill" class:warn={postRamPct > 75} style="width: {postRamPct}%;"></div>
        </div>
        <span class="g-sub">+{simRamGiB} GiB planned · {totalRamGiB - postRamGiB} GiB headroom</span>
      </div>

      <!-- Storage Projection -->
      <div class="gauge-card">
        <div class="gauge-header">
          <span class="g-title">STORAGE POOL FABRIC</span>
          <span class="g-val">{postStorageTiB.toFixed(1)} / {totalStorageTiB} TiB ({postStoragePct}%)</span>
        </div>
        <div class="gauge-track">
          <div class="gauge-fill" style="width: {postStoragePct}%;"></div>
        </div>
        <span class="g-sub">+{simStorageTiB.toFixed(1)} TiB provisioned footprint</span>
      </div>
    </div>

    <!-- Summary Prognosis & N+1 Status -->
    <div class="prognosis-strip mono">
      <div class="prognosis-item">
        <TrendingUp size={14} />
        <span>GROWTH RUNWAY: <strong>~{daysToFull} DAYS</strong> TO SATURATION</span>
      </div>

      <div class="prognosis-item" class:fail={!nPlusOnePass}>
        {#if nPlusOnePass}
          <CheckCircle size={14} class="ok-icon" />
          <span>N+1 HA CHECK: <strong>PASS (RESILIENT TO WORST-CASE NODE LOSS)</strong></span>
        {:else}
          <AlertTriangle size={14} class="warn-icon" />
          <span>N+1 HA CHECK: <strong>FAIL (REMAINING NODES WILL THRESHOLD ON FAILURE)</strong></span>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .capacity-simulator {
    margin-bottom: 20px;
    padding: 0;
    overflow: hidden;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    box-shadow: 2px 2px 0 var(--ink);
  }
  .panel-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 14px;
    background: var(--bg);
    border-bottom: 1px solid var(--border-strong);
  }
  .title-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11.5px;
    font-weight: 700;
    color: var(--text);
  }
  .forecast-badge {
    font-size: 10px;
    color: var(--accent);
    font-weight: 700;
  }
  .simulator-body {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .inputs-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
  }
  .sim-field {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .lbl {
    font-size: 10.5px;
    color: var(--text-3);
    font-weight: 700;
  }
  .input-unit {
    display: flex;
    align-items: center;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
  }
  .sim-input {
    width: 100%;
    height: 32px;
    padding: 0 8px;
    border: none;
    background: transparent;
    color: var(--text);
    font-family: inherit;
    font-size: 12px;
    font-weight: 700;
  }
  .sim-input:focus {
    outline: none;
  }
  .unit {
    padding: 0 8px;
    font-size: 10.5px;
    color: var(--text-3);
  }
  .projections-deck {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }
  .gauge-card {
    padding: 10px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .gauge-header {
    display: flex;
    justify-content: space-between;
    font-size: 10.5px;
  }
  .g-title {
    color: var(--text-3);
    font-weight: 700;
  }
  .g-val {
    color: var(--text);
    font-weight: 700;
  }
  .gauge-track {
    height: 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    overflow: hidden;
  }
  .gauge-fill {
    height: 100%;
    background: var(--accent);
  }
  .gauge-fill.warn {
    background: var(--warn);
  }
  .g-sub {
    font-size: 9.5px;
    color: var(--text-3);
  }
  .prognosis-strip {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 14px;
    background: var(--bg);
    border: 1px solid var(--border);
    font-size: 11px;
    flex-wrap: wrap;
    gap: 8px;
  }
  .prognosis-item {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
  }
  .prognosis-item strong {
    color: var(--text);
  }
  :global(.ok-icon) {
    color: var(--ok);
  }
  :global(.warn-icon) {
    color: var(--warn);
  }
  .prognosis-item.fail strong {
    color: var(--warn);
  }
  @media (max-width: 900px) {
    .inputs-grid,
    .projections-deck {
      grid-template-columns: 1fr;
    }
  }
</style>
