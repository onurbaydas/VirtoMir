<script lang="ts">
  import Network from '@lucide/svelte/icons/network'
  import Cable from '@lucide/svelte/icons/cable'
  import Layers from '@lucide/svelte/icons/layers'
  import Monitor from '@lucide/svelte/icons/monitor'
  import Tag from '@lucide/svelte/icons/tag'

  interface Props {
    netName?: string
  }
  let { netName = 'default' }: Props = $props()
</script>

<div class="sdn-topology ui-panel">
  <div class="sdn-header mono">
    <div class="title-wrap">
      <Cable size={15} />
      <span>ORTHOGONAL SDN TOPOLOGY CIRCUIT (90° WIRING DIAGRAM)</span>
    </div>
    <span class="l2-status">L2 / L3 PACKET FABRIC ACTIVE</span>
  </div>

  <div class="topology-diagram mono">
    <!-- Tier 1: Physical Uplinks -->
    <div class="tier-col">
      <div class="tier-label">01 // PHYSICAL NICS</div>
      <div class="node-box active">
        <div class="box-head">
          <span class="led ok"></span>
          <span>eno1 (10G SFP+)</span>
        </div>
        <span class="meta">MAC: 00:1E:67:D2:A4:01</span>
        <span class="speed">10 Gbps Full-Duplex</span>
      </div>

      <div class="node-box active">
        <div class="box-head">
          <span class="led ok"></span>
          <span>eno2 (10G SFP+)</span>
        </div>
        <span class="meta">MAC: 00:1E:67:D2:A4:02</span>
        <span class="speed">10 Gbps Full-Duplex</span>
      </div>
    </div>

    <!-- Orthogonal Bus 1: Bonding -->
    <div class="ortho-bus">
      <svg class="bus-svg" viewBox="0 0 40 180" preserveAspectRatio="none">
        <path d="M 0 45 L 20 45 L 20 90 L 40 90" class="wire" />
        <path d="M 0 135 L 20 135 L 20 90 L 40 90" class="wire" />
      </svg>
    </div>

    <!-- Tier 2: Bond Layer (LACP 802.3ad) -->
    <div class="tier-col">
      <div class="tier-label">02 // LACP AGGREGATE</div>
      <div class="node-box active bond">
        <div class="box-head">
          <span class="led ok"></span>
          <span>bond0 [LACP 802.3ad]</span>
        </div>
        <span class="meta">Mode: 4 (802.3ad) · Hash: L2+L3</span>
        <span class="speed">20 Gbps Aggregate Trunk</span>
      </div>
    </div>

    <!-- Orthogonal Bus 2: Bridge -->
    <div class="ortho-bus">
      <svg class="bus-svg" viewBox="0 0 40 180" preserveAspectRatio="none">
        <path d="M 0 90 L 40 90" class="wire" />
      </svg>
    </div>

    <!-- Tier 3: Linux Bridge / OVS -->
    <div class="tier-col">
      <div class="tier-label">03 // VIRTUAL SWITCH</div>
      <div class="node-box active bridge">
        <div class="box-head">
          <span class="led ok"></span>
          <span>vmbr0 / virbr0</span>
        </div>
        <span class="meta">STP: ON · Forward Delay: 0</span>
        <span class="speed">Subnet: 192.168.100.0/24</span>
      </div>
    </div>

    <!-- Orthogonal Bus 3: VLAN Demux -->
    <div class="ortho-bus">
      <svg class="bus-svg" viewBox="0 0 40 180" preserveAspectRatio="none">
        <path d="M 0 90 L 20 90 L 20 45 L 40 45" class="wire" />
        <path d="M 0 90 L 20 90 L 20 135 L 40 135" class="wire" />
      </svg>
    </div>

    <!-- Tier 4: VLAN / VXLAN VPAs -->
    <div class="tier-col">
      <div class="tier-label">04 // ISOLATION TAG</div>
      <div class="node-box vlan">
        <div class="box-head">
          <Tag size={12} />
          <span>VLAN 100 (PROD)</span>
        </div>
        <span class="meta">CIDR: 192.168.100.0/24</span>
      </div>

      <div class="node-box vlan">
        <div class="box-head">
          <Tag size={12} />
          <span>VLAN 200 (MGMT)</span>
        </div>
        <span class="meta">CIDR: 10.10.200.0/24</span>
      </div>
    </div>

    <!-- Orthogonal Bus 4: Virtual NICs -->
    <div class="ortho-bus">
      <svg class="bus-svg" viewBox="0 0 40 180" preserveAspectRatio="none">
        <path d="M 0 45 L 40 45" class="wire" />
        <path d="M 0 135 L 40 135" class="wire" />
      </svg>
    </div>

    <!-- Tier 5: VM Endpoints -->
    <div class="tier-col">
      <div class="tier-label">05 // GUEST VNICS</div>
      <div class="node-box guest">
        <div class="box-head">
          <Monitor size={12} />
          <span>vnet0 → prod-db-01</span>
        </div>
        <span class="meta">IP: 192.168.100.15</span>
      </div>

      <div class="node-box guest">
        <div class="box-head">
          <Monitor size={12} />
          <span>vnet1 → k8s-node-02</span>
        </div>
        <span class="meta">IP: 10.10.200.42</span>
      </div>
    </div>
  </div>
</div>

<style>
  .sdn-topology {
    margin-bottom: 24px;
    padding: 0;
    overflow: hidden;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    box-shadow: 2px 2px 0 var(--ink);
  }
  .sdn-header {
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
  .l2-status {
    font-size: 10px;
    color: var(--ok);
    font-weight: 700;
  }
  .topology-diagram {
    display: flex;
    align-items: center;
    padding: 20px 16px;
    overflow-x: auto;
    background: var(--bg-surface, var(--bg));
    gap: 0;
  }
  .tier-col {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-width: 170px;
    flex-shrink: 0;
  }
  .tier-label {
    font-size: 9.5px;
    font-weight: 700;
    color: var(--text-3);
    letter-spacing: 0.08em;
    padding-bottom: 4px;
    border-bottom: 1px solid var(--border);
  }
  .node-box {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel);
    font-size: 11px;
  }
  .node-box.active {
    border-color: var(--border-strong);
  }
  .box-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-weight: 700;
    color: var(--text);
  }
  .led {
    width: 6px;
    height: 6px;
    background: #444;
  }
  .led.ok {
    background: var(--ok);
    box-shadow: 0 0 4px var(--ok);
  }
  .meta {
    font-size: 9.5px;
    color: var(--text-3);
  }
  .speed {
    font-size: 9.5px;
    color: var(--accent);
    font-weight: 700;
  }
  .node-box.bond {
    border-color: var(--accent);
  }
  .node-box.bridge {
    border-color: #4a9eff;
  }
  .node-box.vlan {
    border-left: 3px solid var(--warn);
  }
  .node-box.guest {
    border-left: 3px solid var(--ok);
  }
  .ortho-bus {
    width: 40px;
    height: 180px;
    flex-shrink: 0;
  }
  .bus-svg {
    width: 100%;
    height: 100%;
    display: block;
  }
  .wire {
    fill: none;
    stroke: var(--border-strong);
    stroke-width: 1.5;
  }
</style>
