<script lang="ts">
  // The host's GPUs with their load, as their drivers report it (nvidia-smi for NVIDIA cards
  // on the NVIDIA driver, sysfs for amdgpu). A card bound to vfio-pci belongs to a machine
  // and reports nothing to the host.
  import Cpu from '@lucide/svelte/icons/cpu'
  import { ent, type GPU } from '../enterprise'
  import type { HostPCI } from '../api'

  let { pci = [] }: { pci?: HostPCI[] } = $props()

  let gpus = $state<GPU[]>([])
  let failed = $state(false)
  async function load() {
    try {
      gpus = await ent.gpus()
    } catch {
      failed = true
    }
  }
  $effect(() => {
    void load()
    const t = setInterval(() => void load(), 5000)
    return () => clearInterval(t)
  })

  const pciOf = (addr: string) => pci.find((p) => p.address === addr)
  const pct = (a?: number, b?: number) => (a !== undefined && b ? Math.min(100, (a / b) * 100) : 0)
  // Emulated adapters of a nested host are not worth a card.
  let real = $derived(gpus.filter((g) => !['Virtio', 'QEMU', 'VMware', 'Red Hat', 'Microsoft', 'Cirrus', 'VirtualBox'].includes(g.vendor)))
</script>

{#if !failed && real.length}
  <h2 class="sub"><Cpu size={16} /> GPUs</h2>
  <div class="gpus">
    {#each real as g (g.address)}
      {@const p = pciOf(g.address)}
      <section class="ui-panel gpu">
        <header>
          <b>{g.name || p?.product || `${g.vendor} ${g.deviceId}`}</b>
          <span class="muted mono">{g.address} · {g.driver ?? 'no driver'}{g.mig ? ` · MIG ${g.mig}` : ''}</span>
        </header>
        {#if p?.usedBy.length}
          <p class="used">Passed through to {p.usedBy.join(', ')}</p>
        {:else if g.driver === 'vfio-pci'}
          <p class="muted">Bound to vfio-pci (ready for passthrough); the host cannot read its load.</p>
        {/if}
        {#if g.telemetry}
          <div class="m">
            <span class="lbl">Load</span><span class="bar"><i style="width: {g.utilPercent ?? 0}%"></i></span><span class="mono">{g.utilPercent?.toFixed(0) ?? '—'} %</span>
            <span class="lbl">Memory</span><span class="bar"><i style="width: {pct(g.memUsedMiB, g.memTotalMiB)}%"></i></span>
            <span class="mono">{g.memUsedMiB !== undefined ? (g.memUsedMiB / 1024).toFixed(1) : '—'} / {g.memTotalMiB ? (g.memTotalMiB / 1024).toFixed(0) : '—'} GiB</span>
            {#if g.powerW !== undefined}<span class="lbl">Power</span><span class="bar"><i style="width: {pct(g.powerW, g.powerLimitW)}%"></i></span><span class="mono">{g.powerW.toFixed(0)}{g.powerLimitW ? ` / ${g.powerLimitW.toFixed(0)}` : ''} W</span>{/if}
            {#if g.tempC !== undefined}<span class="lbl">Temperature</span><span></span><span class="mono" class:hot={g.tempC >= 85}>{g.tempC.toFixed(0)} °C</span>{/if}
          </div>
          <p class="muted small">From {g.telemetry}.{g.mig === 'enabled' ? ' MIG instances are made with nvidia-smi on the host; they then show up as vGPU slices below.' : ''}</p>
        {:else if !p?.usedBy.length && g.driver !== 'vfio-pci'}
          <p class="muted small">No load figures: {g.vendor === 'NVIDIA' ? 'install the NVIDIA driver (nvidia-smi)' : 'its driver does not report them'}.</p>
        {/if}
      </section>
    {/each}
  </div>
{/if}

<style>
  .sub { display: flex; gap: 8px; align-items: center; font-size: 15px; margin: 18px 0 8px; }
  .gpus { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 10px; }
  .gpu { padding: 12px 14px; display: flex; flex-direction: column; gap: 6px; }
  header { display: flex; flex-direction: column; }
  .m { display: grid; grid-template-columns: auto 1fr auto; gap: 4px 10px; align-items: center; font-size: 12px; }
  .lbl { font: 600 11px var(--font-mono); text-transform: uppercase; color: var(--text-2); }
  .bar { height: 6px; background: var(--bg-elev); }
  .bar i { display: block; height: 100%; background: var(--accent); }
  .used { color: var(--accent-2); margin: 0; font-size: 13px; }
  .hot { color: var(--danger); }
  .muted { color: var(--text-2); margin: 0; font-size: 12px; }
  .small { font-size: 11px; }
</style>
