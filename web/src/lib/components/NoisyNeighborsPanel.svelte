<script lang="ts">
  import AlertTriangle from '@lucide/svelte/icons/triangle-alert'
  import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left'
  import CheckCircle from '@lucide/svelte/icons/check-circle'
  import Activity from '@lucide/svelte/icons/activity'
  import { live } from '../live.svelte'
  import { toasts } from '../toast.svelte'

  interface Anomaly {
    id: string
    vmName: string
    host: string
    cpuSteal: number
    ioWait: number
    memFaults: number
    severity: 'CRITICAL' | 'WARNING' | 'OPTIMAL'
    issue: string
  }

  let anomalies = $state<Anomaly[]>([
    {
      id: 'a1',
      vmName: 'prod-db-postgres-01',
      host: 'hv-node-01',
      cpuSteal: 14.8,
      ioWait: 22.4,
      memFaults: 890,
      severity: 'CRITICAL',
      issue: 'High IOPS thrashing on shared NVMe pool + CPU core contention',
    },
    {
      id: 'a2',
      vmName: 'analytics-spark-worker-04',
      host: 'hv-node-01',
      cpuSteal: 7.2,
      ioWait: 9.1,
      memFaults: 140,
      severity: 'WARNING',
      issue: 'Memory balloon paging frequency exceeding threshold',
    },
    {
      id: 'a3',
      vmName: 'k8s-ingress-gateway',
      host: 'hv-node-02',
      cpuSteal: 0.8,
      ioWait: 1.2,
      memFaults: 12,
      severity: 'OPTIMAL',
      issue: 'Workload execution within scheduled SLA envelope',
    },
  ])

  function migrateWorkload(a: Anomaly) {
    toasts.push({
      kind: 'info',
      title: `Workload rebalance triggered: ${a.vmName}`,
      detail: `Initiating live migration to least-loaded peer node...`,
    })
    anomalies = anomalies.filter((x) => x.id !== a.id)
  }
</script>

<div class="noisy-neighbors-panel ui-panel">
  <div class="panel-header mono">
    <div class="title-wrap">
      <Activity size={15} />
      <span>NOISY NEIGHBORS & WORKLOAD ANOMALY DETECTOR</span>
    </div>
    <span class="badge-status">REAL-TIME TELEMETRY SCANNER ACTIVE</span>
  </div>

  <div class="table-wrap">
    <table class="anomaly-table mono">
      <thead>
        <tr>
          <th>WORKLOAD</th>
          <th>HOST</th>
          <th>CPU STEAL</th>
          <th>I/O WAIT</th>
          <th>MEM FAULTS/S</th>
          <th>IMPACT SEVERITY</th>
          <th>DIAGNOSTIC ROOT CAUSE</th>
          <th>ACTION</th>
        </tr>
      </thead>
      <tbody>
        {#each anomalies as a (a.id)}
          <tr class={a.severity.toLowerCase()}>
            <td><strong>{a.vmName}</strong></td>
            <td>{a.host}</td>
            <td>
              <span class="val" class:alert={a.cpuSteal > 5}>{a.cpuSteal.toFixed(1)}%</span>
            </td>
            <td>
              <span class="val" class:alert={a.ioWait > 8}>{a.ioWait.toFixed(1)}%</span>
            </td>
            <td>{a.memFaults} /s</td>
            <td>
              <span class="sev-badge {a.severity.toLowerCase()}">
                {#if a.severity === 'CRITICAL'}
                  ▲ CRITICAL
                {:else if a.severity === 'WARNING'}
                  ▲ WARNING
                {:else}
                  ● OPTIMAL
                {/if}
              </span>
            </td>
            <td class="desc-cell">{a.issue}</td>
            <td>
              {#if a.severity !== 'OPTIMAL'}
                <button class="rebalance-btn" onclick={() => migrateWorkload(a)}>
                  <ArrowRightLeft size={11} />
                  <span>REBALANCE NOW</span>
                </button>
              {:else}
                <span class="ok-pill"><CheckCircle size={11} /> BALANCED</span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<style>
  .noisy-neighbors-panel {
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
  .badge-status {
    font-size: 10px;
    color: var(--ok);
    font-weight: 700;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .anomaly-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11px;
    text-align: left;
  }
  .anomaly-table th {
    background: var(--surface);
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-weight: 700;
  }
  .anomaly-table td {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  .anomaly-table tr.critical {
    background: color-mix(in srgb, var(--danger) 8%, var(--surface));
  }
  .anomaly-table tr.warning {
    background: color-mix(in srgb, var(--warn) 6%, var(--surface));
  }
  .val.alert {
    color: var(--danger);
    font-weight: 700;
  }
  .sev-badge {
    display: inline-block;
    padding: 2px 6px;
    font-size: 10px;
    font-weight: 700;
  }
  .sev-badge.critical {
    background: color-mix(in srgb, var(--danger) 18%, var(--surface));
    color: var(--danger);
    border: 1px solid var(--danger);
  }
  .sev-badge.warning {
    background: color-mix(in srgb, var(--warn) 18%, var(--surface));
    color: var(--warn);
    border: 1px solid var(--warn);
  }
  .sev-badge.optimal {
    background: color-mix(in srgb, var(--ok) 18%, var(--surface));
    color: var(--ok);
    border: 1px solid var(--ok);
  }
  .desc-cell {
    color: var(--text-2);
    font-size: 10.5px;
  }
  .rebalance-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding: 0 8px;
    background: var(--metal-accent);
    color: var(--accent-contrast);
    border: 1px solid var(--border-strong);
    font-family: inherit;
    font-size: 10px;
    font-weight: 700;
    cursor: pointer;
    box-shadow: 1px 1px 0 var(--ink);
  }
  .ok-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--ok);
    font-size: 10px;
    font-weight: 700;
  }
</style>
