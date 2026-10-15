<script lang="ts">
  import Server from '@lucide/svelte/icons/server'
  import Zap from '@lucide/svelte/icons/zap'
  import Wind from '@lucide/svelte/icons/wind'
  import Thermometer from '@lucide/svelte/icons/thermometer'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import Wrench from '@lucide/svelte/icons/wrench'
  import MaintenanceSimulatorModal from './MaintenanceSimulatorModal.svelte'

  interface Props {
    hostname: string
    model?: string
  }
  let { hostname, model = 'DELL POWEREDGE R750 2U HCI NODE' }: Props = $props()

  let maintOpen = $state(false)
</script>

<div class="rack-chassis-card">
  <!-- Chassis Enclosure Faceplate -->
  <div class="chassis-faceplate">
    <div class="ears left">
      <span class="screw"></span>
      <span class="screw"></span>
    </div>

    <div class="bezel-content">
      <div class="chassis-info">
        <div class="indicator-group">
          <span class="led ok" title="System Power OK"></span>
          <span class="led ok" title="ECC Memory OK"></span>
          <span class="led activity" title="Drive Array I/O Activity"></span>
        </div>
        <div class="model-block mono">
          <span class="brand">VIRTO-CHASSIS // 2U-ENC-01</span>
          <span class="name">{hostname.toUpperCase()} · {model}</span>
        </div>
      </div>

      <!-- Drive Bay Slots Mini Representation -->
      <div class="drive-bays" title="Hot-Swap NVMe/SAS Gen4 Array">
        {#each Array.from({ length: 12 }) as _, i}
          <div class="drive-slot" class:active={i < 8}>
            <span class="slot-led"></span>
          </div>
        {/each}
      </div>

      <!-- Action Controls -->
      <div class="chassis-actions">
        <a
          class="oob-btn mono"
          href="https://192.168.100.24"
          target="_blank"
          rel="noopener"
          title="Open Out-of-Band Management (iDRAC9 Enterprise)"
        >
          <ExternalLink size={12} />
          <span>iDRAC9 [192.168.100.24]</span>
        </a>

        <button class="maint-btn mono" onclick={() => (maintOpen = true)} title="Drain node & simulate evacuation">
          <Wrench size={12} />
          <span>MAINTENANCE MODE</span>
        </button>
      </div>
    </div>

    <div class="ears right">
      <span class="screw"></span>
      <span class="screw"></span>
    </div>
  </div>

  <!-- Telemetry Sensors Deck (PSU, Fans, Thermal) -->
  <div class="sensors-deck mono">
    <!-- Redundant PSU 1 & 2 -->
    <div class="sensor-block">
      <div class="sensor-title">
        <Zap size={13} />
        <span>REDUNDANT POWER (PSU 1 & 2)</span>
      </div>
      <div class="sensor-readout">
        <span class="val ok">PSU 1: 1200W [OK · 94% EFF]</span>
        <span class="sep">|</span>
        <span class="val ok">PSU 2: 1200W [OK · STANDBY]</span>
      </div>
    </div>

    <!-- Fan Arrays -->
    <div class="sensor-block">
      <div class="sensor-title">
        <Wind size={13} />
        <span>CHASSIS COOLING (FANS 1-6)</span>
      </div>
      <div class="sensor-readout">
        <span class="val ok">ARRAY 1-6: 7,200 RPM [OPTIMAL TACH]</span>
      </div>
    </div>

    <!-- Thermal Sensors -->
    <div class="sensor-block">
      <div class="sensor-title">
        <Thermometer size={13} />
        <span>THERMAL GRADIENT</span>
      </div>
      <div class="sensor-readout">
        <span class="val">INLET: 21°C</span>
        <span class="sep">→</span>
        <span class="val">EXHAUST: 38°C</span>
        <span class="delta">(Δ +17°C)</span>
      </div>
    </div>
  </div>
</div>

<MaintenanceSimulatorModal
  open={maintOpen}
  nodeName={hostname}
  onclose={() => (maintOpen = false)}
/>

<style>
  .rack-chassis-card {
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 var(--ink);
    margin-bottom: 20px;
    display: flex;
    flex-direction: column;
  }
  .chassis-faceplate {
    display: flex;
    background: var(--bg-surface, #1b1c20);
    border-bottom: 1px solid var(--border-strong);
    min-height: 52px;
  }
  .ears {
    width: 22px;
    background: #25272c;
    border-right: 1px solid #000;
    display: flex;
    flex-direction: column;
    justify-content: space-around;
    align-items: center;
    padding: 6px 0;
  }
  .ears.right {
    border-right: none;
    border-left: 1px solid #000;
  }
  .screw {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #3f424a;
    border: 1px solid #1a1b1e;
  }
  .bezel-content {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 16px;
    gap: 16px;
    flex-wrap: wrap;
  }
  .chassis-info {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .indicator-group {
    display: flex;
    gap: 4px;
  }
  .led {
    width: 6px;
    height: 6px;
    background: #333;
  }
  .led.ok {
    background: var(--ok);
    box-shadow: 0 0 5px var(--ok);
  }
  .led.activity {
    background: var(--accent);
    box-shadow: 0 0 5px var(--accent);
  }
  .model-block {
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .brand {
    font-size: 10px;
    color: var(--accent);
    font-weight: 700;
    letter-spacing: 0.08em;
  }
  .name {
    font-size: 12px;
    color: var(--text);
    font-weight: 700;
  }
  .drive-bays {
    display: flex;
    gap: 3px;
    padding: 4px 6px;
    background: #111215;
    border: 1px solid #2d2f35;
  }
  .drive-slot {
    width: 8px;
    height: 22px;
    background: #1d1e23;
    border: 1px solid #33363e;
    position: relative;
  }
  .drive-slot.active .slot-led {
    position: absolute;
    bottom: 2px;
    left: 2px;
    width: 2px;
    height: 2px;
    background: var(--ok);
  }
  .chassis-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .oob-btn,
  .maint-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 10px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    color: var(--text);
    font-size: 11px;
    text-decoration: none;
    cursor: pointer;
    box-shadow: var(--bevel);
  }
  .oob-btn:hover,
  .maint-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .maint-btn {
    background: color-mix(in srgb, var(--warn) 10%, var(--bg));
    border-color: var(--warn);
    color: var(--warn);
  }
  .maint-btn:hover {
    background: var(--warn);
    color: #000;
  }

  /* Sensors Deck */
  .sensors-deck {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    padding: 10px 16px;
    background: var(--surface);
    font-size: 11px;
  }
  .sensor-block {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 6px 10px;
    background: var(--bg);
    border: 1px solid var(--border);
  }
  .sensor-title {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
    font-weight: 700;
    font-size: 10px;
  }
  .sensor-readout {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
  }
  .sensor-readout .ok {
    color: var(--ok);
    font-weight: 700;
  }
  .sensor-readout .sep {
    color: var(--text-3);
  }
  .sensor-readout .delta {
    color: var(--accent);
    font-weight: 700;
  }
  @media (max-width: 900px) {
    .sensors-deck {
      grid-template-columns: 1fr;
    }
  }
</style>
