<script lang="ts">
  import Cpu from '@lucide/svelte/icons/cpu'
  import Zap from '@lucide/svelte/icons/zap'
  import Thermometer from '@lucide/svelte/icons/thermometer'
  import Layers from '@lucide/svelte/icons/layers'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import { toasts } from '../toast.svelte'

  interface MigSlice {
    id: string
    profile: string
    vramGiB: number
    assignedVm?: string
  }

  interface GpuDevice {
    id: string
    model: string
    pciAddr: string
    vramTotalGiB: number
    vramUsedGiB: number
    tempC: number
    powerW: number
    powerLimitW: number
    migEnabled: boolean
    slices: MigSlice[]
  }

  let gpus = $state<GpuDevice[]>([
    {
      id: 'gpu-0',
      model: 'NVIDIA H100 SXM5 80GB (Hopper GH100)',
      pciAddr: '0000:41:00.0',
      vramTotalGiB: 80,
      vramUsedGiB: 60,
      tempC: 48,
      powerW: 340,
      powerLimitW: 700,
      migEnabled: true,
      slices: [
        { id: 'mig-1', profile: '3g.40gb', vramGiB: 40, assignedVm: 'llm-inference-prod' },
        { id: 'mig-2', profile: '2g.20gb', vramGiB: 20, assignedVm: 'speech-whisper-02' },
        { id: 'mig-3', profile: '1g.10gb', vramGiB: 10 },
        { id: 'mig-4', profile: '1g.10gb', vramGiB: 10 },
      ],
    },
    {
      id: 'gpu-1',
      model: 'NVIDIA A100-PCIE-80GB (Ampere GA100)',
      pciAddr: '0000:81:00.0',
      vramTotalGiB: 80,
      vramUsedGiB: 40,
      tempC: 42,
      powerW: 185,
      powerLimitW: 300,
      migEnabled: true,
      slices: [
        { id: 'mig-5', profile: '4g.40gb', vramGiB: 40, assignedVm: 'vision-yolo-cluster' },
        { id: 'mig-6', profile: '2g.20gb', vramGiB: 20 },
        { id: 'mig-7', profile: '2g.20gb', vramGiB: 20 },
      ],
    },
    {
      id: 'gpu-2',
      model: 'NVIDIA GeForce RTX 4090 24GB (Ada Lovelace)',
      pciAddr: '0000:c1:00.0',
      vramTotalGiB: 24,
      vramUsedGiB: 18.5,
      tempC: 56,
      powerW: 280,
      powerLimitW: 450,
      migEnabled: false,
      slices: [],
    },
  ])

  function carveSlice(gpu: GpuDevice, profile: string, vram: number) {
    const newSlice: MigSlice = {
      id: `mig-${Date.now()}`,
      profile,
      vramGiB: vram,
    }
    gpu.slices.push(newSlice)
    toasts.push({
      kind: 'info',
      title: 'MIG compute slice carved',
      detail: `${profile} (${vram} GiB VRAM) created on ${gpu.model}.`,
    })
  }

  function deleteSlice(gpu: GpuDevice, sliceId: string) {
    gpu.slices = gpu.slices.filter((s) => s.id !== sliceId)
    toasts.push({
      kind: 'info',
      title: 'MIG slice released',
      detail: 'GPU compute capacity returned to pool.',
    })
  }
</script>

<div class="gpu-pool-panel ui-panel">
  <div class="panel-header mono">
    <div class="title-wrap">
      <Cpu size={16} />
      <span>AI ACCELERATION CLUSTER & GPU SLICER (NVIDIA MIG / PASSTHROUGH)</span>
    </div>
    <span class="active-tag">NVLINK 4.0 (900 GB/s) BIDIRECTIONAL FABRIC</span>
  </div>

  <div class="panel-body">
    <!-- GPU Cards Deck -->
    <div class="gpu-list">
      {#each gpus as g (g.id)}
        <div class="gpu-card">
          <!-- Card Header & Telemetry -->
          <div class="gpu-head">
            <div class="head-left mono">
              <span class="gpu-pci">{g.pciAddr}</span>
              <strong class="gpu-name">{g.model}</strong>
            </div>

            <div class="head-sensors mono">
              <div class="sensor-pill">
                <Thermometer size={12} />
                <span>{g.tempC}°C</span>
              </div>
              <div class="sensor-pill">
                <Zap size={12} />
                <span>{g.powerW}W / {g.powerLimitW}W</span>
              </div>
              <div class="sensor-pill vram">
                <span>VRAM: {g.vramUsedGiB} / {g.vramTotalGiB} GiB</span>
              </div>
            </div>
          </div>

          <!-- VRAM Progress Gauge -->
          <div class="vram-bar">
            <div class="fill" style="width: {(g.vramUsedGiB / g.vramTotalGiB) * 100}%;"></div>
          </div>

          <!-- Visual MIG Slicer Block -->
          {#if g.migEnabled}
            <div class="mig-section mono">
              <div class="mig-bar-label">
                <span>MULTI-INSTANCE GPU (MIG) HARDWARE PARTITIONING:</span>
                <span class="mig-hint">Click available slice to bind to a virtual machine</span>
              </div>

              <div class="mig-slices-track">
                {#each g.slices as s (s.id)}
                  <div
                    class="slice-block"
                    class:assigned={!!s.assignedVm}
                    style="flex: {s.vramGiB};"
                  >
                    <div class="slice-header">
                      <span class="slice-prof">{s.profile}</span>
                      <span class="slice-vram">{s.vramGiB}G</span>
                    </div>
                    <span class="slice-dest">{s.assignedVm ? `→ ${s.assignedVm}` : '[AVAILABLE]'}</span>
                    {#if !s.assignedVm}
                      <button class="slice-del" onclick={() => deleteSlice(g, s.id)} title="Release slice">✕</button>
                    {/if}
                  </div>
                {/each}
              </div>

              <!-- Carve Slice Quick Actions -->
              <div class="carve-actions">
                <span class="act-label">CARVE NEW INSTANCE:</span>
                <button class="carve-btn" onclick={() => carveSlice(g, '1g.10gb', 10)}>+ 1g.10gb</button>
                <button class="carve-btn" onclick={() => carveSlice(g, '2g.20gb', 20)}>+ 2g.20gb</button>
                <button class="carve-btn" onclick={() => carveSlice(g, '3g.40gb', 40)}>+ 3g.40gb</button>
              </div>
            </div>
          {:else}
            <div class="passthrough-notice mono">
              <span>DIRECT DEDICATED PCI PASSTHROUGH MODE (VFIO-PCI DRIVER)</span>
            </div>
          {/if}
        </div>
      {/each}
    </div>

    <!-- NVLink Interconnect Bandwidth Matrix -->
    <div class="nvlink-box mono">
      <div class="nvlink-head">NVLINK BIDIRECTIONAL BANDWIDTH TOPOLOGY MATRIX (GB/s):</div>
      <div class="matrix-grid">
        <div class="matrix-cell header">PORT</div>
        <div class="matrix-cell header">GPU 0 (H100)</div>
        <div class="matrix-cell header">GPU 1 (A100)</div>
        <div class="matrix-cell header">GPU 2 (4090)</div>

        <div class="matrix-cell row-header">GPU 0</div>
        <div class="matrix-cell self">—</div>
        <div class="matrix-cell high">900 GB/s</div>
        <div class="matrix-cell pcie">PCIe Gen5</div>

        <div class="matrix-cell row-header">GPU 1</div>
        <div class="matrix-cell high">900 GB/s</div>
        <div class="matrix-cell self">—</div>
        <div class="matrix-cell pcie">PCIe Gen4</div>

        <div class="matrix-cell row-header">GPU 2</div>
        <div class="matrix-cell pcie">PCIe Gen5</div>
        <div class="matrix-cell pcie">PCIe Gen4</div>
        <div class="matrix-cell self">—</div>
      </div>
    </div>
  </div>
</div>

<style>
  .gpu-pool-panel {
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
  .active-tag {
    font-size: 10px;
    color: var(--ok);
    font-weight: 700;
  }
  .panel-body {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .gpu-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .gpu-card {
    background: var(--bg);
    border: 1px solid var(--border-strong);
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .gpu-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
  }
  .head-left {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
  }
  .gpu-pci {
    padding: 2px 6px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 10px;
    color: var(--text-3);
  }
  .head-sensors {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11px;
  }
  .sensor-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 7px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-2);
  }
  .sensor-pill.vram {
    color: var(--accent);
    font-weight: 700;
  }
  .vram-bar {
    height: 6px;
    background: var(--surface);
    border: 1px solid var(--border);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
  }
  .mig-section {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
  }
  .mig-bar-label {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: var(--text-3);
    font-weight: 700;
  }
  .mig-hint {
    color: var(--text-3);
    font-weight: normal;
  }
  .mig-slices-track {
    display: flex;
    gap: 4px;
    height: 44px;
  }
  .slice-block {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    padding: 4px 6px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    color: var(--text);
  }
  .slice-block.assigned {
    background: color-mix(in srgb, var(--accent) 15%, var(--bg));
    border-color: var(--accent);
  }
  .slice-header {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    font-weight: 700;
  }
  .slice-prof {
    color: var(--text);
  }
  .slice-vram {
    color: var(--accent);
  }
  .slice-dest {
    font-size: 9.5px;
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .slice-del {
    position: absolute;
    top: 2px;
    right: 2px;
    background: none;
    border: none;
    color: var(--text-3);
    font-size: 10px;
    cursor: pointer;
  }
  .slice-del:hover {
    color: var(--danger);
  }
  .carve-actions {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 4px;
    font-size: 10px;
  }
  .act-label {
    color: var(--text-3);
    font-weight: 700;
  }
  .carve-btn {
    background: var(--bg);
    border: 1px solid var(--border);
    padding: 2px 6px;
    color: var(--text);
    font-family: inherit;
    font-size: 9.5px;
    cursor: pointer;
  }
  .carve-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .passthrough-notice {
    font-size: 10.5px;
    color: var(--text-3);
    padding: 6px;
    background: var(--surface);
    border: 1px solid var(--border);
  }
  .nvlink-box {
    border: 1px solid var(--border);
    background: var(--bg);
    padding: 10px 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .nvlink-head {
    font-size: 10.5px;
    color: var(--text-3);
    font-weight: 700;
  }
  .matrix-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    border: 1px solid var(--border-strong);
    font-size: 11px;
    text-align: center;
  }
  .matrix-cell {
    padding: 6px 10px;
    border: 1px solid var(--border);
    background: var(--surface);
  }
  .matrix-cell.header,
  .matrix-cell.row-header {
    background: var(--bg);
    font-weight: 700;
    color: var(--text-3);
  }
  .matrix-cell.high {
    background: color-mix(in srgb, var(--ok) 18%, var(--surface));
    color: var(--ok);
    font-weight: 700;
  }
  .matrix-cell.pcie {
    color: var(--text-2);
  }
  .matrix-cell.self {
    color: var(--text-3);
  }
</style>
