<script lang="ts">
  import { t } from '../i18n.svelte'
  // The machine VirtoMir runs on, as its firmware and sensors report it (DMI, hwmon). Only
  // what the host reports is shown: no sensors, no sensor rows.
  import Server from '@lucide/svelte/icons/server'
  import Thermometer from '@lucide/svelte/icons/thermometer'
  import Fan from '@lucide/svelte/icons/fan'
  import Zap from '@lucide/svelte/icons/zap'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import Wrench from '@lucide/svelte/icons/wrench'
  import { ent, type Chassis, type Sensor } from '../enterprise'
  import { cluster } from '../cluster.svelte'
  import { session } from '../session.svelte'
  import EvacuateDialog from './EvacuateDialog.svelte'

  let { hostname }: { hostname: string } = $props()

  let c = $state<Chassis | null>(null)
  let unavailable = $state(false)
  let evacuating = $state(false)
  let showAll = $state(false)

  async function load() {
    try {
      c = await ent.chassis()
    } catch {
      unavailable = true
    }
  }
  $effect(() => {
    void load()
    const t = setInterval(() => void load(), 10_000) // sensors change slowly
    return () => clearInterval(t)
  })

  const by = (kind: Sensor['kind']) => (c?.sensors ?? []).filter((s) => s.kind === kind)
  let temps = $derived(by('temperature'))
  let fans = $derived(by('fan'))
  let power = $derived(by('power'))
  let hottest = $derived(temps.reduce<Sensor | null>((a, s) => (!a || s.value > a.value ? s : a), null))
  const level = (s: Sensor) => (s.crit && s.value >= s.crit - 5 ? 'danger' : s.max && s.value >= s.max - 5 ? 'warn' : s.value >= 85 ? 'warn' : '')
  const uptime = (sec?: number) => {
    if (!sec) return ''
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
    return d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`
  }
  let model = $derived([c?.vendor, c?.product].filter(Boolean).join(' ') || t('Unknown hardware'))
</script>

{#if !unavailable}
  <section class="ui-panel chassis">
    <div class="id">
      <Server size={22} />
      <div class="txt">
        <strong>{hostname}</strong>
        <span class="muted">{model}{c?.chassisType ? ` · ${c.chassisType}` : ''}{c?.virtual ? ' · virtual machine (nested)' : ''}</span>
        <span class="muted mono small">
          {#if c?.serial}S/N {c.serial} · {/if}{#if c?.biosVersion}BIOS {c.biosVersion}{c.biosDate ? ` (${c.biosDate})` : ''} · {/if}{c?.os ?? ''}{c?.kernel ? ` · kernel ${c.kernel}` : ''}{c?.uptimeSec ? ` · up ${uptime(c.uptimeSec)}` : ''}
        </span>
      </div>
    </div>
    <div class="readings">
      {#if hottest}
        <span class="r {level(hottest)}" title="{hottest.chip}: {hottest.label}"><Thermometer size={14} /> {hottest.value.toFixed(0)} °C <span class="muted">max of {temps.length}</span></span>
      {/if}
      {#if fans.length}<span class="r"><Fan size={14} /> {fans.map((f) => f.value.toFixed(0)).join(' / ')} RPM</span>{/if}
      {#if power.length}<span class="r"><Zap size={14} /> {power.reduce((n, p) => n + p.value, 0).toFixed(0)} W</span>{/if}
      {#if c && !c.sensors.length}<span class="muted small">{t('No sensors reported (install lm-sensors drivers, or this is a VM)')}</span>{/if}
    </div>
    <div class="acts">
      {#if c?.bmc}<a class="ui-btn sm" href={c.bmc.url} target="_blank" rel="noopener noreferrer" title={t('Management controller (from the HA fence agent)')}><ExternalLink size={13} /> {t('BMC')}</a>{/if}
      {#if c?.sensors.length}<button class="ui-btn sm ghost" onclick={() => (showAll = !showAll)}>{showAll ? 'Hide sensors' : t('All sensors')}</button>{/if}
      {#if cluster.clustered && session.can('admin')}<button class="ui-btn sm" onclick={() => (evacuating = true)}><Wrench size={13} /> {t('Maintenance…')}</button>{/if}
    </div>
    {#if showAll && c}
      <div class="sensors mono">
        {#each c.sensors as s, i (i)}
          <span class={level(s)}>{s.chip} · {s.label}: <b>{s.kind === 'voltage' ? s.value.toFixed(2) : s.value.toFixed(s.kind === 'power' ? 1 : 0)} {s.unit}</b>{s.crit ? ` (crit ${s.crit})` : s.max ? ` (max ${s.max})` : ''}</span>
        {/each}
      </div>
    {/if}
  </section>
{/if}

<EvacuateDialog bind:open={evacuating} />

<style>
  .chassis { display: grid; grid-template-columns: 1fr auto auto; gap: 12px 20px; align-items: center; padding: 12px 16px; margin-bottom: 14px; }
  .id { display: flex; gap: 12px; align-items: center; min-width: 0; }
  .txt { display: flex; flex-direction: column; min-width: 0; }
  .txt span { overflow: hidden; text-overflow: ellipsis; }
  .readings { display: flex; gap: 14px; flex-wrap: wrap; }
  .r { display: inline-flex; gap: 5px; align-items: center; font: 600 13px var(--font-mono); }
  .r.warn, .sensors .warn { color: var(--warn); }
  .r.danger, .sensors .danger { color: var(--danger); }
  .acts { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
  .sensors { grid-column: 1 / -1; display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 2px 16px; font-size: 12px; border-top: 1px solid var(--border); padding-top: 8px; }
  .muted { color: var(--text-2); }
  .small { font-size: 11px; }
  @media (max-width: 900px) { .chassis { grid-template-columns: 1fr; } .acts { justify-content: flex-start; } }
</style>
