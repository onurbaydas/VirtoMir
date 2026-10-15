<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../api'
  import { toasts } from '../toast.svelte'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Zap from '@lucide/svelte/icons/zap'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Play from '@lucide/svelte/icons/play'
  import Settings from '@lucide/svelte/icons/settings'
  import Server from '@lucide/svelte/icons/server'
  import Dialog from './Dialog.svelte'

  interface QuorumState {
    quorate: boolean
    totalNodes: number
    activeNodes: number
    requiredQuorum: number
    witnessEnabled: boolean
    witnessIp?: string
    witnessReachable: boolean
    lastEvaluated: string
  }

  interface NodeHeartbeat {
    nodeId: string
    nodeName: string
    address: string
    lastSeen: string
    status: 'online' | 'degraded' | 'dead'
    fenced: boolean
    fenceTime?: string
  }

  interface FenceAgent {
    nodeId: string
    driver: string
    endpoint: string
    status: string
  }

  interface VMHAPolicy {
    uuid: string
    name: string
    enabled: boolean
    priority: 'critical' | 'high' | 'normal' | 'low'
    restartDelaySec: number
    maxRetries: number
    targetNode?: string
  }

  interface HAStatus {
    quorum: QuorumState
    heartbeats: NodeHeartbeat[]
    config: {
      autoFailover: boolean
      heartbeatIntervalSec: number
      failoverTimeoutSec: number
      fencingEnabled: boolean
      witnessIp?: string
      fenceAgents: Record<string, FenceAgent>
    }
    policies: VMHAPolicy[]
  }

  interface PlanStep {
    order: number
    vmUuid: string
    vmName: string
    priority: string
    delaySec: number
    targetNode: string
    action: string
  }

  interface FailoverSimulation {
    deadNodeId: string
    deadNodeName?: string
    survivingNodes: string[]
    hasQuorum: boolean
    canSafelyFailover: boolean
    failoverPlan: PlanStep[]
  }

  let status = $state<HAStatus | null>(null)
  let loading = $state(true)
  let simModalOpen = $state(false)
  let simDeadNode = $state('')
  let simulation = $state<FailoverSimulation | null>(null)
  let simLoading = $state(false)

  async function loadStatus() {
    loading = true
    try {
      status = await api.get<HAStatus>('/api/v1/ha/status')
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to load HA status', detail: e.message })
    } finally {
      loading = false
    }
  }

  onMount(() => {
    loadStatus()
    const timer = setInterval(loadStatus, 5000)
    return () => clearInterval(timer)
  })

  async function toggleAutoFailover() {
    if (!status) return
    const next = !status.config.autoFailover
    try {
      await api.put('/api/v1/ha/config', {
        ...status.config,
        autoFailover: next,
      })
      status.config.autoFailover = next
      toasts.push({
        kind: next ? 'info' : 'warn',
        title: next ? 'AUTO-FAILOVER ARMED' : 'AUTO-FAILOVER DISABLED',
        detail: next ? 'Quorum-driven automatic VM restart active.' : 'Manual operator failover required.',
      })
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Config update failed', detail: e.message })
    }
  }

  async function triggerFence(nodeId: string) {
    if (!confirm(`EMERGENCY STONITH: Are you sure you want to trigger power-cut fence on node ${nodeId}?`)) return
    try {
      const res = await api.post<any>(`/api/v1/ha/fence/${nodeId}`, { action: 'poweroff' })
      toasts.push({ kind: 'warn', title: 'FENCE COMMAND SENT', detail: res.message })
      await loadStatus()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Fence failed', detail: e.message })
    }
  }

  async function toggleVMPolicy(policy: VMHAPolicy) {
    const updated = { ...policy, enabled: !policy.enabled }
    try {
      await api.put(`/api/v1/ha/vms/${policy.uuid}`, updated)
      policy.enabled = updated.enabled
      toasts.push({
        kind: 'info',
        title: `HA Protection ${updated.enabled ? 'Enabled' : 'Disabled'}`,
        detail: policy.name,
      })
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to update HA policy', detail: e.message })
    }
  }

  async function runSimulation() {
    if (!simDeadNode) return
    simLoading = true
    try {
      simulation = await api.post<FailoverSimulation>('/api/v1/ha/simulate-failover', { deadNodeId: simDeadNode })
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Simulation failed', detail: e.message })
    } finally {
      simLoading = false
    }
  }
</script>

<div class="ha-deck">
  <!-- Quorum & Engine Status Header -->
  <div class="ha-top-grid">
    <div class="ui-panel quorum-card" class:quorate={status?.quorum.quorate} class:lost={status && !status.quorum.quorate}>
      <div class="q-icon">
        {#if status?.quorum.quorate}
          <ShieldCheck size={32} />
        {:else}
          <ShieldAlert size={32} />
        {/if}
      </div>
      <div class="q-info">
        <div class="q-title mono">
          {status?.quorum.quorate ? 'CLUSTER QUORATE' : 'QUORUM COMPROMISED'}
        </div>
        <div class="q-detail">
          Active nodes: <strong>{status?.quorum.activeNodes ?? 1} / {status?.quorum.totalNodes ?? 1}</strong>
          (Required Quorum: {status?.quorum.requiredQuorum ?? 1})
        </div>
        {#if status?.quorum.witnessEnabled}
          <div class="witness-tag mono">
            WITNESS ({status.quorum.witnessIp}): {status.quorum.witnessReachable ? 'REACHABLE' : 'UNREACHABLE'}
          </div>
        {/if}
      </div>
      <div class="q-actions">
        <button
          class="ui-btn mono"
          class:danger={status?.config.autoFailover}
          class:primary={!status?.config.autoFailover}
          onclick={toggleAutoFailover}
        >
          <Zap size={14} />
          {status?.config.autoFailover ? 'DISARM AUTO-FAILOVER' : 'ARM AUTO-FAILOVER'}
        </button>
        <button class="ui-btn mono" onclick={() => { simModalOpen = true; if (status?.heartbeats.length) simDeadNode = status.heartbeats[0].nodeId; runSimulation() }}>
          <Play size={14} /> SIMULATE FAILURE
        </button>
      </div>
    </div>
  </div>

  <!-- Cluster Node Heartbeats & Fencing Agents -->
  <section class="ui-panel">
    <div class="panel-head">
      <div class="ph-title mono">NODE HEARTBEATS & STONITH FENCE AGENTS</div>
      <button class="ui-btn icon-only" onclick={loadStatus} title="Refresh pulse"><RefreshCw size={14} /></button>
    </div>
    <div class="table-wrap">
      <table class="data-table mono">
        <thead>
          <tr>
            <th>NODE</th>
            <th>STATUS</th>
            <th>ADDRESS</th>
            <th>LAST SEEN</th>
            <th>FENCE DRIVER</th>
            <th>STONITH STATUS</th>
            <th>ACTIONS</th>
          </tr>
        </thead>
        <tbody>
          {#if !status?.heartbeats.length}
            <tr><td colspan="7" class="empty">No remote cluster heartbeats recorded yet.</td></tr>
          {:else}
            {#each status.heartbeats as hb (hb.nodeId)}
              {@const agent = status.config.fenceAgents[hb.nodeId]}
              <tr>
                <td><strong>{hb.nodeName}</strong><br><small class="text-dim">{hb.nodeId}</small></td>
                <td><span class="status-dot {hb.status}"></span> {hb.status.toUpperCase()}</td>
                <td>{hb.address}</td>
                <td>{new Date(hb.lastSeen).toLocaleTimeString()}</td>
                <td>{agent?.driver ?? 'manual (admin ack)'}</td>
                <td>
                  {#if hb.fenced}
                    <span class="badge danger">FENCED (POWER-OFF)</span>
                  {:else}
                    <span class="badge ok">STANDBY</span>
                  {/if}
                </td>
                <td>
                  <button class="ui-btn danger small mono" onclick={() => triggerFence(hb.nodeId)} title="Trigger STONITH Power-Cut">
                    <Zap size={12} /> FENCE
                  </button>
                </td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </section>

  <!-- Protected VMs & Failover Priority Matrix -->
  <section class="ui-panel">
    <div class="panel-head">
      <div class="ph-title mono">VM HIGH AVAILABILITY PROTECTION MATRIX</div>
      <span class="count-badge mono">{status?.policies.filter(p => p.enabled).length ?? 0} PROTECTED</span>
    </div>
    <div class="table-wrap">
      <table class="data-table mono">
        <thead>
          <tr>
            <th>PROTECTION</th>
            <th>VM WORKLOAD</th>
            <th>PRIORITY</th>
            <th>RESTART ORDER</th>
            <th>RESTART DELAY</th>
            <th>MAX RETRIES</th>
            <th>PREFERRED NODE</th>
          </tr>
        </thead>
        <tbody>
          {#if !status?.policies.length}
            <tr><td colspan="7" class="empty">No VM HA policies defined. Protect machines in VM Settings -> HA.</td></tr>
          {:else}
            {#each status.policies as pol (pol.uuid)}
              <tr class:row-disabled={!pol.enabled}>
                <td>
                  <button class="toggle-btn" class:on={pol.enabled} onclick={() => toggleVMPolicy(pol)}>
                    {pol.enabled ? 'ENABLED' : 'DISABLED'}
                  </button>
                </td>
                <td><strong>{pol.name}</strong><br><small class="text-dim">{pol.uuid.slice(0, 8)}</small></td>
                <td><span class="priority-badge {pol.priority}">{pol.priority.toUpperCase()}</span></td>
                <td>{pol.priority === 'critical' ? '#1 FIRST' : pol.priority === 'high' ? '#2 SECOND' : '#3 NORMAL'}</td>
                <td>+{pol.restartDelaySec}s</td>
                <td>{pol.maxRetries} tries</td>
                <td>{pol.targetNode || 'ANY SURVIVING'}</td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </section>
</div>

<!-- Simulation Modal -->
<Dialog title="HA FAILOVER WHAT-IF SIMULATOR" bind:open={simModalOpen}>
  <div class="sim-modal">
    <p class="sim-intro">
      Select a cluster node to simulate an unexpected hardware crash. VirtoMir will evaluate quorum health, STONITH fencing, and calculate the exact VM failover restart sequence.
    </p>

    <div class="sim-controls">
      <label class="mono">
        SIMULATE FAILURE OF NODE:
        <select bind:value={simDeadNode} onchange={runSimulation}>
          {#if status?.heartbeats}
            {#each status.heartbeats as hb}
              <option value={hb.nodeId}>{hb.nodeName} ({hb.nodeId})</option>
            {/each}
          {/if}
          <option value="node-mock-fail">Simulate Local Host (HV-01)</option>
        </select>
      </label>
      <button class="ui-btn primary mono" onclick={runSimulation} disabled={simLoading}>
        <RefreshCw size={14} class={simLoading ? 'spin' : ''} /> RE-RUN SIMULATION
      </button>
    </div>

    {#if simulation}
      <div class="sim-summary ui-panel" class:ok={simulation.canSafelyFailover} class:danger={!simulation.canSafelyFailover}>
        <div class="sim-stat">
          <span class="lbl mono">QUORUM STATE:</span>
          <strong>{simulation.hasQuorum ? 'QUORATE (SAFE)' : 'SPLIT-BRAIN RISK (NO QUORUM)'}</strong>
        </div>
        <div class="sim-stat">
          <span class="lbl mono">FAILOVER ELIGIBILITY:</span>
          <strong>{simulation.canSafelyFailover ? 'PERMITTED' : 'BLOCKED (PREVENTS CORRUPTION)'}</strong>
        </div>
        <div class="sim-stat">
          <span class="lbl mono">SURVIVING TARGETS:</span>
          <strong>{simulation.survivingNodes.join(', ') || 'NONE'}</strong>
        </div>
      </div>

      <div class="plan-head mono">CALCULATED FAILOVER EXECUTION SEQUENCE</div>
      <table class="data-table mono sim-table">
        <thead>
          <tr>
            <th>ORDER</th>
            <th>VM NAME</th>
            <th>PRIORITY</th>
            <th>DELAY</th>
            <th>TARGET NODE</th>
            <th>FAILOVER ACTION</th>
          </tr>
        </thead>
        <tbody>
          {#if !simulation.failoverPlan.length}
            <tr><td colspan="6" class="empty">No HA-enabled workloads affected on this node.</td></tr>
          {:else}
            {#each simulation.failoverPlan as step}
              <tr>
                <td>#{step.order}</td>
                <td><strong>{step.vmName}</strong></td>
                <td><span class="priority-badge {step.priority}">{step.priority.toUpperCase()}</span></td>
                <td>+{step.delaySec}s</td>
                <td><strong>{step.targetNode}</strong></td>
                <td><span class="action-tag">{step.action}</span></td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    {/if}
  </div>
</Dialog>

<style>
  .ha-deck {
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  .ha-top-grid {
    display: grid;
    grid-template-columns: 1fr;
    gap: 16px;
  }
  .quorum-card {
    display: flex;
    align-items: center;
    gap: 20px;
    padding: 20px 24px;
    background: var(--surface-1);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 0 var(--border-strong);
  }
  .quorum-card.quorate {
    border-left: 6px solid var(--ok);
  }
  .quorum-card.lost {
    border-left: 6px solid var(--danger);
  }
  .q-icon {
    color: var(--ok);
  }
  .quorum-card.lost .q-icon {
    color: var(--danger);
  }
  .q-info {
    flex: 1;
  }
  .q-title {
    font-size: 16px;
    font-weight: 700;
    letter-spacing: 0.05em;
  }
  .q-detail {
    font-size: 13px;
    color: var(--text-dim);
    margin-top: 2px;
  }
  .witness-tag {
    font-size: 11px;
    color: var(--accent);
    margin-top: 4px;
  }
  .q-actions {
    display: flex;
    gap: 10px;
  }
  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
  }
  .ph-title {
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.05em;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .data-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .data-table th {
    text-align: left;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    color: var(--text-dim);
    font-weight: 600;
  }
  .data-table td {
    padding: 10px 16px;
    border-bottom: 1px solid var(--border-soft);
  }
  .empty {
    text-align: center;
    color: var(--text-dim);
    padding: 24px !important;
  }
  .status-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    margin-right: 6px;
  }
  .status-dot.online { background: var(--ok); }
  .status-dot.degraded { background: var(--warn); }
  .status-dot.dead { background: var(--danger); }
  .badge {
    padding: 2px 6px;
    font-size: 10px;
    border: 1px solid currentColor;
  }
  .badge.ok { color: var(--ok); }
  .badge.danger { color: var(--danger); }
  .priority-badge {
    padding: 2px 6px;
    font-size: 10px;
    font-weight: 700;
  }
  .priority-badge.critical { background: var(--danger); color: #fff; }
  .priority-badge.high { background: var(--accent); color: #fff; }
  .priority-badge.normal { background: var(--surface-3); color: var(--text); }
  .priority-badge.low { background: var(--surface-2); color: var(--text-dim); }
  .toggle-btn {
    padding: 3px 8px;
    font-family: inherit;
    font-size: 10px;
    font-weight: 700;
    background: var(--surface-2);
    border: 1px solid var(--border);
    cursor: pointer;
  }
  .toggle-btn.on {
    background: var(--ok);
    color: #000;
    border-color: var(--ok);
  }
  .row-disabled td {
    opacity: 0.6;
  }
  .sim-intro {
    font-size: 13px;
    color: var(--text-dim);
    margin-bottom: 16px;
  }
  .sim-controls {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    margin-bottom: 16px;
  }
  .sim-controls select {
    padding: 6px 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
  }
  .sim-summary {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 12px;
    padding: 14px;
    margin-bottom: 16px;
    border-left: 4px solid var(--border);
  }
  .sim-summary.ok { border-left-color: var(--ok); }
  .sim-summary.danger { border-left-color: var(--danger); }
  .sim-stat .lbl {
    font-size: 10px;
    color: var(--text-dim);
    display: block;
  }
  .plan-head {
    font-size: 12px;
    font-weight: 700;
    margin-bottom: 8px;
  }
  .action-tag {
    color: var(--ok);
  }
  .spin {
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
