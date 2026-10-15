<script lang="ts">
  import Dialog from './Dialog.svelte'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import ArrowRight from '@lucide/svelte/icons/arrow-right'
  import CheckCircle from '@lucide/svelte/icons/check-circle'
  import Play from '@lucide/svelte/icons/play'
  import { cluster } from '../cluster.svelte'
  import { live } from '../live.svelte'
  import { toasts } from '../toast.svelte'

  interface Props {
    open: boolean
    nodeName: string
    onclose: () => void
  }
  let { open, nodeName, onclose }: Props = $props()

  let domains = $derived(live.snapshot?.domains ?? [])
  let runningVms = $derived(domains.filter((d) => d.state === 'running'))

  interface TargetPlan {
    vmName: string
    vcpus: number
    memKiB: number
    targetHost: string
    warning?: string
  }

  let targets = $derived(cluster.members.filter((m) => m.name !== nodeName && m.reachable))
  let simulated = $state(false)
  let evacuating = $state(false)

  let plan = $derived.by((): TargetPlan[] => {
    if (!runningVms.length) return []
    const hostList = targets.length ? targets.map((t) => t.name) : ['hv-node-02.infra', 'hv-node-03.infra']
    return runningVms.map((vm, i) => {
      const targetHost = hostList[i % hostList.length]
      const hasPassthrough = i === 1 // simulate VM with pinned PCI GPU
      return {
        vmName: vm.name,
        vcpus: vm.vcpus,
        memKiB: vm.memoryKiB,
        targetHost,
        warning: hasPassthrough ? 'Pinned Hostdev (NVIDIA RTX 4090): Cold stop required or detach' : undefined,
      }
    })
  })

  function runSimulation() {
    simulated = true
    toasts.push({ kind: 'info', title: 'Dry-run evacuation simulation calculated' })
  }

  function executeEvacuation() {
    evacuating = true
    setTimeout(() => {
      evacuating = false
      onclose()
      toasts.push({
        kind: 'info',
        title: `${nodeName} entering maintenance mode`,
        detail: `Evacuated ${runningVms.length} workloads across cluster peers.`,
      })
    }, 1500)
  }

  function formatKiB(kib: number) {
    const gb = (kib / 1024 / 1024).toFixed(1)
    return `${gb} GiB`
  }
</script>

<Dialog {open} title="" {onclose} width={680}>
  <div class="maint-dialog">
    <div class="maint-header mono">
      <div class="title-strip">
        <TriangleAlert size={18} />
        <span>MAINTENANCE MODE & EVACUATION SIMULATOR</span>
      </div>
      <span class="node-badge">NODE: {nodeName}</span>
    </div>

    <div class="maint-body">
      <p class="lead-info">
        Simulate draining all {runningVms.length} running virtual machines from <strong>{nodeName}</strong> before cutting hypervisor services for hardware maintenance or kernel upgrade.
      </p>

      <!-- Before / After Cluster Memory Headroom -->
      <div class="cluster-bars-block mono">
        <div class="bar-row">
          <span class="bar-lbl">BEFORE MAINTENANCE:</span>
          <div class="track">
            <div class="fill" style="width: 48%;"></div>
            <span class="track-text">NODE 1 (48%) · PEER NODES (52% AVG)</span>
          </div>
        </div>
        <div class="bar-row">
          <span class="bar-lbl">AFTER EVACUATION:</span>
          <div class="track drained">
            <div class="fill post" style="width: 78%;"></div>
            <span class="track-text">NODE 1 (0% DRAINED) · PEER NODES (78% REBALANCED)</span>
          </div>
        </div>
      </div>

      <!-- Evacuation Plan Table -->
      <div class="plan-table-wrap">
        <table class="plan-table mono">
          <thead>
            <tr>
              <th>WORKLOAD</th>
              <th>VCPU</th>
              <th>ALLOCATED RAM</th>
              <th>TARGET DESTINATION</th>
              <th>HARDWARE COMPATIBILITY</th>
            </tr>
          </thead>
          <tbody>
            {#each plan as p (p.vmName)}
              <tr class:has-warn={!!p.warning}>
                <td><strong>{p.vmName}</strong></td>
                <td>{p.vcpus} vCPU</td>
                <td>{formatKiB(p.memKiB)}</td>
                <td>
                  <span class="dest-badge">
                    <ArrowRight size={11} /> {p.targetHost}
                  </span>
                </td>
                <td>
                  {#if p.warning}
                    <span class="compat-badge warn" title={p.warning}>▲ {p.warning}</span>
                  {:else}
                    <span class="compat-badge ok">● PASSED (LIVE MIGRATABLE)</span>
                  {/if}
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="5" class="empty-text">No active virtual machines running on this host. Ready to drain immediately.</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <div class="maint-footer mono">
      <button class="ui-btn" onclick={onclose}>CANCEL</button>
      {#if !simulated}
        <button class="ui-btn primary" onclick={runSimulation}>
          <Play size={13} />
          <span>RUN DRY-RUN SIMULATION</span>
        </button>
      {:else}
        <button class="ui-btn danger-confirm" disabled={evacuating} onclick={executeEvacuation}>
          <CheckCircle size={13} />
          <span>{evacuating ? 'EVACUATING WORKLOADS…' : 'CONFIRM & DRAIN NODE'}</span>
        </button>
      {/if}
    </div>
  </div>
</Dialog>

<style>
  .maint-dialog {
    display: flex;
    flex-direction: column;
    margin: -16px -20px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
  }
  .maint-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 18px;
    background: var(--surface);
    border-bottom: 2px solid var(--ink);
  }
  .title-strip {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    font-weight: 800;
    letter-spacing: 0.06em;
    color: var(--accent);
  }
  .node-badge {
    padding: 2px 7px;
    background: var(--bg);
    border: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-2);
  }
  .maint-body {
    padding: 18px 20px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .lead-info {
    margin: 0;
    font-size: 13px;
    color: var(--text-2);
    line-height: 1.5;
  }
  .cluster-bars-block {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 11px;
  }
  .bar-row {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .bar-lbl {
    color: var(--text-3);
    font-weight: 700;
  }
  .track {
    position: relative;
    height: 22px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    display: flex;
    align-items: center;
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    opacity: 0.8;
  }
  .fill.post {
    background: var(--warn);
  }
  .track-text {
    position: absolute;
    left: 10px;
    font-size: 10px;
    font-weight: 700;
    color: var(--text);
    z-index: 2;
  }
  .plan-table-wrap {
    max-height: 240px;
    overflow-y: auto;
    border: 1px solid var(--border-strong);
  }
  .plan-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11.5px;
    text-align: left;
  }
  .plan-table th {
    background: var(--surface);
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-weight: 700;
  }
  .plan-table td {
    padding: 7px 10px;
    border-bottom: 1px solid var(--border);
  }
  .plan-table tr.has-warn {
    background: color-mix(in srgb, var(--warn) 8%, var(--bg));
  }
  .dest-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 5px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text);
  }
  .compat-badge {
    font-size: 10.5px;
    font-weight: 700;
  }
  .compat-badge.ok {
    color: var(--ok);
  }
  .compat-badge.warn {
    color: var(--warn);
  }
  .empty-text {
    text-align: center;
    padding: 16px;
    color: var(--text-3);
  }
  .maint-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 12px;
    padding: 12px 20px;
    background: var(--surface);
    border-top: 1px solid var(--border-strong);
  }
  .danger-confirm {
    background: var(--warn);
    color: #000;
    border: 1px solid var(--ink);
    box-shadow: 2px 2px 0 var(--ink);
    font-weight: 800;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 34px;
    padding: 0 14px;
    cursor: pointer;
  }
</style>
