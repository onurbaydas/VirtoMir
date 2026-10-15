<script lang="ts">
  import Shield from '@lucide/svelte/icons/shield'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import ArrowDownLeft from '@lucide/svelte/icons/arrow-down-left'
  import ArrowUpRight from '@lucide/svelte/icons/arrow-up-right'
  import Dialog from './Dialog.svelte'
  import { toasts } from '../toast.svelte'

  interface Props {
    uuid: string
    name: string
    active: boolean
  }
  let { uuid, name, active }: Props = $props()

  interface Rule {
    id: string
    direction: 'IN' | 'OUT'
    proto: 'TCP' | 'UDP' | 'ICMP' | 'ANY'
    source: string
    dest: string
    port: string
    action: 'ACCEPT' | 'DROP' | 'REJECT'
    log: boolean
    enabled: boolean
    packets: number
    bytes: number
  }

  let rules = $state<Rule[]>([
    {
      id: 'r1',
      direction: 'IN',
      proto: 'TCP',
      source: '192.168.1.0/24',
      dest: '10.0.0.15',
      port: '22',
      action: 'ACCEPT',
      log: true,
      enabled: true,
      packets: 14208,
      bytes: 1840290,
    },
    {
      id: 'r2',
      direction: 'IN',
      proto: 'TCP',
      source: '0.0.0.0/0',
      dest: '10.0.0.15',
      port: '80, 443',
      action: 'ACCEPT',
      log: false,
      enabled: true,
      packets: 849201,
      bytes: 129482100,
    },
    {
      id: 'r3',
      direction: 'IN',
      proto: 'ICMP',
      source: '192.168.1.0/24',
      dest: '10.0.0.15',
      port: 'echo-request',
      action: 'ACCEPT',
      log: false,
      enabled: true,
      packets: 412,
      bytes: 34608,
    },
    {
      id: 'r4',
      direction: 'IN',
      proto: 'ANY',
      source: '0.0.0.0/0',
      dest: '10.0.0.15',
      port: 'ANY',
      action: 'DROP',
      log: true,
      enabled: true,
      packets: 39401,
      bytes: 3120490,
    },
    {
      id: 'r5',
      direction: 'OUT',
      proto: 'ANY',
      source: '10.0.0.15',
      dest: '0.0.0.0/0',
      port: 'ANY',
      action: 'ACCEPT',
      log: false,
      enabled: true,
      packets: 984021,
      bytes: 847291040,
    },
  ])

  let addOpen = $state(false)
  let dir = $state<'IN' | 'OUT'>('IN')
  let proto = $state<'TCP' | 'UDP' | 'ICMP' | 'ANY'>('TCP')
  let source = $state('0.0.0.0/0')
  let dest = $state('GUEST_IP')
  let port = $state('80')
  let action = $state<'ACCEPT' | 'DROP' | 'REJECT'>('ACCEPT')
  let log = $state(false)

  function applyPreset(preset: 'ssh' | 'web' | 'block-in') {
    if (preset === 'ssh') {
      dir = 'IN'
      proto = 'TCP'
      source = '192.168.1.0/24'
      port = '22'
      action = 'ACCEPT'
      log = true
    } else if (preset === 'web') {
      dir = 'IN'
      proto = 'TCP'
      source = '0.0.0.0/0'
      port = '80, 443'
      action = 'ACCEPT'
      log = false
    } else if (preset === 'block-in') {
      dir = 'IN'
      proto = 'ANY'
      source = '0.0.0.0/0'
      port = 'ANY'
      action = 'DROP'
      log = true
    }
  }

  function addRule() {
    rules = [
      ...rules,
      {
        id: `r-${Date.now()}`,
        direction: dir,
        proto,
        source: source.trim() || '0.0.0.0/0',
        dest: dest.trim() || 'GUEST_IP',
        port: port.trim() || 'ANY',
        action,
        log,
        enabled: true,
        packets: 0,
        bytes: 0,
      },
    ]
    addOpen = false
    toasts.push({ kind: 'info', title: 'Firewall rule applied to nftables filter' })
  }

  function deleteRule(id: string) {
    rules = rules.filter((r) => r.id !== id)
    toasts.push({ kind: 'info', title: 'Firewall rule removed' })
  }

  function toggleRule(id: string) {
    rules = rules.map((r) => (r.id === id ? { ...r, enabled: !r.enabled } : r))
  }

  function formatBytes(b: number): string {
    if (b < 1024) return `${b} B`
    if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
    if (b < 1024 * 1024 * 1024) return `${(b / (1024 * 1024)).toFixed(1)} MB`
    return `${(b / (1024 * 1024 * 1024)).toFixed(2)} GB`
  }
</script>

<div class="firewall-panel">
  <!-- Top Action & Overview Bar -->
  <div class="fw-topbar">
    <div class="left-info">
      <div class="engine-pill mono">
        <span class="dot">●</span>
        <span>ENGINE: NFTABLES HOOKED</span>
      </div>
      <span class="summary-text mono">
        {rules.filter((r) => r.enabled).length} ACTIVE RULES · {rules.length} TOTAL
      </span>
    </div>

    <div class="right-actions">
      <button class="ui-btn primary" onclick={() => (addOpen = true)}>
        <Plus size={14} />
        <span>+ ADD FIREWALL RULE</span>
      </button>
    </div>
  </div>

  <!-- Rules Table -->
  <div class="table-frame">
    <table class="fw-table mono">
      <thead>
        <tr>
          <th style="width: 50px;">STATE</th>
          <th style="width: 70px;">DIR</th>
          <th style="width: 80px;">PROTO</th>
          <th>SOURCE</th>
          <th>DESTINATION</th>
          <th>PORT</th>
          <th style="width: 90px;">ACTION</th>
          <th style="width: 70px;">LOG</th>
          <th style="width: 140px;">MATCH TELEMETRY</th>
          <th style="width: 50px;"></th>
        </tr>
      </thead>
      <tbody>
        {#each rules as r (r.id)}
          <tr class:disabled={!r.enabled}>
            <td>
              <input
                type="checkbox"
                class="rule-check"
                checked={r.enabled}
                onchange={() => toggleRule(r.id)}
                title="Enable / Disable Rule"
              />
            </td>
            <td>
              <span class="dir-badge" class:in={r.direction === 'IN'} class:out={r.direction === 'OUT'}>
                {#if r.direction === 'IN'}
                  <ArrowDownLeft size={11} /> IN
                {:else}
                  <ArrowUpRight size={11} /> OUT
                {/if}
              </span>
            </td>
            <td>
              <span class="proto-badge">{r.proto}</span>
            </td>
            <td><span class="ip-text">{r.source}</span></td>
            <td><span class="ip-text">{r.dest}</span></td>
            <td><span class="port-text">{r.port}</span></td>
            <td>
              <span class="act-badge {r.action.toLowerCase()}">
                {r.action}
              </span>
            </td>
            <td>
              <span class="log-pill" class:on={r.log}>{r.log ? 'ON' : 'OFF'}</span>
            </td>
            <td class="telemetry-cell">
              <span class="pkts">{r.packets.toLocaleString()} pkts</span>
              <span class="bytes">{formatBytes(r.bytes)}</span>
            </td>
            <td>
              <button class="trash-btn" onclick={() => deleteRule(r.id)} title="Delete Rule">
                <Trash size={13} />
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<!-- Add Rule Dialog -->
<Dialog open={addOpen} title="Add Firewall Rule for {name}" onclose={() => (addOpen = false)} width={560}>
  <div class="rule-dialog form">
    <div class="preset-strip mono">
      <span class="preset-label">QUICK TEMPLATES:</span>
      <button class="preset-btn" onclick={() => applyPreset('ssh')}>SSH Management Only</button>
      <button class="preset-btn" onclick={() => applyPreset('web')}>Open Web (80/443)</button>
      <button class="preset-btn" onclick={() => applyPreset('block-in')}>Drop All Inbound</button>
    </div>

    <div class="fields-grid mono">
      <label class="ui-field">
        <span class="label">Direction</span>
        <select class="ui-input" bind:value={dir}>
          <option value="IN">INBOUND (Traffic to Guest)</option>
          <option value="OUT">OUTBOUND (Traffic from Guest)</option>
        </select>
      </label>

      <label class="ui-field">
        <span class="label">Protocol</span>
        <select class="ui-input" bind:value={proto}>
          <option value="TCP">TCP</option>
          <option value="UDP">UDP</option>
          <option value="ICMP">ICMP</option>
          <option value="ANY">ANY PROTOCOL</option>
        </select>
      </label>

      <label class="ui-field">
        <span class="label">Source IP / CIDR</span>
        <input class="ui-input" type="text" bind:value={source} placeholder="0.0.0.0/0" />
      </label>

      <label class="ui-field">
        <span class="label">Destination IP / CIDR</span>
        <input class="ui-input" type="text" bind:value={dest} placeholder="GUEST_IP" />
      </label>

      <label class="ui-field">
        <span class="label">Port / Range</span>
        <input class="ui-input" type="text" bind:value={port} placeholder="80, 443 or 8000-8080" />
      </label>

      <label class="ui-field">
        <span class="label">Action</span>
        <select class="ui-input" bind:value={action}>
          <option value="ACCEPT">ACCEPT (Allow packet)</option>
          <option value="DROP">DROP (Silently discard)</option>
          <option value="REJECT">REJECT (Send TCP RST / ICMP error)</option>
        </select>
      </label>
    </div>

    <label class="log-switch mono">
      <input type="checkbox" bind:checked={log} />
      <span>Enable kernel packet match logging (dmesg / systemd-journald)</span>
    </label>
  </div>

  {#snippet footer()}
    <button class="ui-btn" onclick={() => (addOpen = false)}>Cancel</button>
    <button class="ui-btn primary mono" onclick={addRule}>INSERT RULE</button>
  {/snippet}
</Dialog>

<style>
  .firewall-panel {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .fw-topbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 14px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 var(--ink);
  }
  .left-info {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .engine-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    font-weight: 700;
    color: var(--ok);
    background: color-mix(in srgb, var(--ok) 12%, var(--surface));
    padding: 3px 8px;
    border: 1px solid color-mix(in srgb, var(--ok) 40%, transparent);
  }
  .engine-pill .dot {
    font-size: 9px;
  }
  .summary-text {
    font-size: 11.5px;
    color: var(--text-2);
  }
  .table-frame {
    border: 1px solid var(--border-strong);
    background: var(--bg);
    overflow-x: auto;
  }
  .fw-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11.5px;
    text-align: left;
  }
  .fw-table th {
    background: var(--surface);
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-weight: 700;
    letter-spacing: 0.05em;
  }
  .fw-table td {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  .fw-table tr:hover {
    background: var(--surface);
  }
  .fw-table tr.disabled {
    opacity: 0.45;
  }
  .rule-check {
    width: 15px;
    height: 15px;
    accent-color: var(--accent);
    cursor: pointer;
  }
  .dir-badge {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: 10px;
    font-weight: 700;
    padding: 2px 5px;
    border: 1px solid var(--border);
  }
  .dir-badge.in {
    background: color-mix(in srgb, var(--accent) 15%, var(--surface));
    color: var(--accent);
    border-color: var(--accent);
  }
  .dir-badge.out {
    background: var(--surface);
    color: var(--text-2);
  }
  .proto-badge {
    display: inline-block;
    padding: 1px 5px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-weight: 700;
    font-size: 10.5px;
  }
  .ip-text,
  .port-text {
    color: var(--text);
  }
  .act-badge {
    display: inline-block;
    padding: 2px 6px;
    font-size: 10.5px;
    font-weight: 700;
  }
  .act-badge.accept {
    background: color-mix(in srgb, var(--ok) 18%, var(--surface));
    color: var(--ok);
    border: 1px solid var(--ok);
  }
  .act-badge.drop {
    background: color-mix(in srgb, var(--danger) 18%, var(--surface));
    color: var(--danger);
    border: 1px solid var(--danger);
  }
  .act-badge.reject {
    background: color-mix(in srgb, var(--warn) 18%, var(--surface));
    color: var(--warn);
    border: 1px solid var(--warn);
  }
  .log-pill {
    font-size: 10px;
    padding: 1px 5px;
    border: 1px solid var(--border);
    color: var(--text-3);
  }
  .log-pill.on {
    color: var(--accent);
    border-color: var(--accent);
  }
  .telemetry-cell {
    display: flex;
    flex-direction: column;
    gap: 1px;
    font-size: 10.5px;
  }
  .telemetry-cell .bytes {
    color: var(--text-3);
  }
  .trash-btn {
    background: none;
    border: none;
    color: var(--text-3);
    cursor: pointer;
    padding: 4px;
  }
  .trash-btn:hover {
    color: var(--danger);
  }

  /* Preset strip */
  .preset-strip {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 11px;
    margin-bottom: 12px;
  }
  .preset-label {
    color: var(--text-3);
    font-weight: 700;
  }
  .preset-btn {
    background: var(--bg);
    border: 1px solid var(--border);
    padding: 3px 8px;
    color: var(--text);
    font-size: 10.5px;
    cursor: pointer;
  }
  .preset-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .fields-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .log-switch {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    font-size: 11.5px;
    color: var(--text-2);
    cursor: pointer;
  }
</style>
