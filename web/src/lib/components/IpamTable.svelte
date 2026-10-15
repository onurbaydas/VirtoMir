<script lang="ts">
  import Table from '@lucide/svelte/icons/table'
  import Check from '@lucide/svelte/icons/check'
  import Plus from '@lucide/svelte/icons/plus'
  import Pin from '@lucide/svelte/icons/pin'
  import { toasts } from '../toast.svelte'

  interface Props {
    subnet?: string
  }
  let { subnet = '192.168.100.0/24' }: Props = $props()

  interface IpamEntry {
    ip: string
    type: 'gateway' | 'static' | 'dhcp' | 'free'
    hostname?: string
    mac?: string
    status: string
  }

  let entries = $state<IpamEntry[]>([
    { ip: '192.168.100.1', type: 'gateway', hostname: 'gateway.virtomir.lan', mac: '52:54:00:1A:00:01', status: 'DEFAULT GATEWAY / HOST' },
    { ip: '192.168.100.2', type: 'static', hostname: 'dns-resolver-01', mac: '52:54:00:1A:00:02', status: 'PINNED STATIC DNS' },
    { ip: '192.168.100.15', type: 'dhcp', hostname: 'prod-db-postgres-01', mac: '52:54:00:4C:91:15', status: 'ACTIVE LEASE (14h REMAINING)' },
    { ip: '192.168.100.42', type: 'dhcp', hostname: 'k8s-node-02', mac: '52:54:00:8B:23:42', status: 'ACTIVE LEASE (22h REMAINING)' },
    { ip: '192.168.100.100', type: 'free', status: 'DHCP POOL RANGE START' },
    { ip: '192.168.100.101', type: 'free', status: 'UNASSIGNED / AVAILABLE' },
    { ip: '192.168.100.102', type: 'free', status: 'UNASSIGNED / AVAILABLE' },
    { ip: '192.168.100.254', type: 'free', status: 'DHCP POOL RANGE END' },
  ])

  function reserveIp(e: IpamEntry) {
    toasts.push({
      kind: 'info',
      title: `IP ${e.ip} reserved`,
      detail: `MAC binding pinned to DHCP static host configuration.`,
    })
  }
</script>

<div class="ipam-table-panel ui-panel">
  <div class="panel-header mono">
    <div class="title-wrap">
      <Table size={15} />
      <span>SUBNET IPAM ALLOCATION & LEASE MATRIX // {subnet}</span>
    </div>
    <div class="legend">
      <span class="legend-item"><span class="chip gw">■</span> GATEWAY</span>
      <span class="legend-item"><span class="chip static">■</span> STATIC PINNED</span>
      <span class="legend-item"><span class="chip dhcp">■</span> DHCP ACTIVE</span>
      <span class="legend-item"><span class="chip free">■</span> FREE</span>
    </div>
  </div>

  <div class="table-wrap">
    <table class="ipam-table mono">
      <thead>
        <tr>
          <th>IPV4 ADDRESS</th>
          <th>ALLOCATION TYPE</th>
          <th>ATTACHED HOSTNAME</th>
          <th>MAC ADDRESS</th>
          <th>LEASE ENVELOPE / STATUS</th>
          <th>ACTION</th>
        </tr>
      </thead>
      <tbody>
        {#each entries as e (e.ip)}
          <tr class={e.type}>
            <td><strong class="ip-addr">{e.ip}</strong></td>
            <td>
              <span class="type-pill {e.type}">{e.type.toUpperCase()}</span>
            </td>
            <td>{e.hostname ?? '—'}</td>
            <td>{e.mac ?? '—'}</td>
            <td class="status-cell">{e.status}</td>
            <td>
              {#if e.type === 'dhcp'}
                <button class="pin-btn" onclick={() => reserveIp(e)} title="Pin as permanent static DHCP reservation">
                  <Pin size={11} />
                  <span>PIN STATIC</span>
                </button>
              {:else if e.type === 'free'}
                <button class="claim-btn" onclick={() => reserveIp(e)} title="Claim IP">
                  <Plus size={11} />
                  <span>CLAIM</span>
                </button>
              {:else}
                <span class="locked-text">RESERVED</span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<style>
  .ipam-table-panel {
    margin-bottom: 24px;
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
    flex-wrap: wrap;
    gap: 8px;
  }
  .title-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11.5px;
    font-weight: 700;
    color: var(--text);
  }
  .legend {
    display: flex;
    gap: 12px;
    font-size: 10px;
    color: var(--text-2);
  }
  .legend-item {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .chip.gw {
    color: var(--accent);
  }
  .chip.static {
    color: #4a9eff;
  }
  .chip.dhcp {
    color: var(--ok);
  }
  .chip.free {
    color: var(--text-3);
  }
  .table-wrap {
    overflow-x: auto;
  }
  .ipam-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11px;
    text-align: left;
  }
  .ipam-table th {
    background: var(--surface);
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-weight: 700;
  }
  .ipam-table td {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  .ip-addr {
    color: var(--text);
  }
  .type-pill {
    display: inline-block;
    padding: 2px 6px;
    font-size: 9.5px;
    font-weight: 700;
  }
  .type-pill.gateway {
    background: color-mix(in srgb, var(--accent) 15%, var(--surface));
    color: var(--accent);
    border: 1px solid var(--accent);
  }
  .type-pill.static {
    background: color-mix(in srgb, #4a9eff 15%, var(--surface));
    color: #4a9eff;
    border: 1px solid #4a9eff;
  }
  .type-pill.dhcp {
    background: color-mix(in srgb, var(--ok) 15%, var(--surface));
    color: var(--ok);
    border: 1px solid var(--ok);
  }
  .type-pill.free {
    background: var(--bg);
    color: var(--text-3);
    border: 1px solid var(--border);
  }
  .status-cell {
    color: var(--text-2);
    font-size: 10.5px;
  }
  .pin-btn,
  .claim-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding: 0 8px;
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    font-family: inherit;
    font-size: 10px;
    font-weight: 700;
    cursor: pointer;
  }
  .pin-btn:hover,
  .claim-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .locked-text {
    font-size: 9.5px;
    color: var(--text-3);
  }
</style>
