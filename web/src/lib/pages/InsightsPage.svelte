<script lang="ts">
  import { t } from '../i18n.svelte'
  // Insights (ADR-0066): when this host and its pools fill up, and which machines are too big
  // or too small, all from the host's own 30-day metric history. Nothing here is guessed:
  // without 14 days of history a forecast says so.
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import { api, errorText } from '../api'
  import { ent, type Forecast, type Insights } from '../enterprise'
  import { formatBytes } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let data = $state<Insights | null>(null)
  let error = $state('')
  let applying = $state('')

  async function load() {
    try {
      data = await ent.insights()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  function runway(f: Forecast): string {
    if (f.insufficientData) return t('Not enough history yet: forecasts need 14 days.')
    if (f.daysUntilFull === 0) return t('Full now.')
    if (f.daysUntilFull < 0) return t('Not growing towards full.')
    return t('Full in about {days} days', { days: Math.round(f.daysUntilFull) })
  }
  const confidence = (f: Forecast) => (f.insufficientData ? '' : t('{pct} % confidence', { pct: Math.round(f.confidence * 100) }))
  const urgent = (f: Forecast) => !f.insufficientData && f.daysUntilFull >= 0 && f.daysUntilFull < 30

  let suggestions = $derived((data?.machines ?? []).filter((m) => m.oversized || m.undersized))

  // Applies a suggestion through the normal settings path (validated, and live where possible).
  async function apply(m: Insights['machines'][number]) {
    applying = m.uuid
    try {
      const s = await api.settings(m.uuid)
      const { bootDevices: _b, hotplugMemoryMiB: _h, ...set } = s
      Object.assign(set, {
        maxVcpus: m.suggestedVcpus,
        vcpus: m.suggestedVcpus,
        sockets: 1,
        cores: m.suggestedVcpus,
        threads: 1,
        memoryMiB: m.suggestedMemMiB,
        currentMemoryMiB: m.suggestedMemMiB,
        pinning: [],
      })
      const res = await api.saveSettings(m.uuid, set)
      toasts.push({
        kind: 'info',
        title: t('{name} resized', { name: m.name }),
        detail: res.appliesOnNextBoot ? t('It is running: the new size applies from its next start.') : undefined,
      })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not resized'), detail: errorText(e) })
    } finally {
      applying = ''
    }
  }
</script>

<header class="page-head">
  <div>
    <h1>{t('Insights')}</h1>
    <p class="lead">{t("Forecasts and suggestions from this host's own metric history (up to 30 days). Other hosts of a cluster show theirs when you switch to them.")}</p>
  </div>
</header>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !data}
  <p class="muted">{t('Loading…')}</p>
{:else}
  <div class="cards">
    {#each [{ f: data.host.cpu, label: t('Host CPU'), icon: Cpu }, { f: data.host.mem, label: t('Host memory'), icon: MemoryStick }] as c (c.label)}
      <section class="ui-panel card" class:urgent={urgent(c.f)}>
        <h2><c.icon size={16} /> {c.label}</h2>
        <p class="big">{runway(c.f)}</p>
        <p class="muted">{confidence(c.f)}{c.f.seasonal ? ` · ${t('daily pattern taken into account')}` : ''}</p>
      </section>
    {/each}
  </div>

  <h2 class="sub">{t('Storage pools')}</h2>
  <section class="ui-panel">
    {#if data.pools.length === 0}
      <p class="muted pad">{t('No active pools.')}</p>
    {:else}
      <table class="t">
        <thead><tr><th>{t('Pool')}</th><th>{t('Used')}</th><th>{t('Forecast')}</th></tr></thead>
        <tbody>
          {#each data.pools as p (p.name)}
            <tr class:urgent={urgent(p.forecast)}>
              <td class="mono">{p.name}</td>
              <td>{formatBytes(p.allocationBytes)} / {formatBytes(p.capacityBytes)}</td>
              <td>{runway(p.forecast)} <span class="muted">{confidence(p.forecast)}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <h2 class="sub">{t('Right-sizing')}</h2>
  <section class="ui-panel">
    {#if suggestions.length === 0}
      <p class="muted pad">{t('Every machine looks sized right for what it used (or there is not enough history yet).')}</p>
    {:else}
      <table class="t">
        <thead><tr><th>{t('Machine')}</th><th>{t('vCPUs')}</th><th>{t('Memory')}</th><th>{t('Why')}</th><th></th></tr></thead>
        <tbody>
          {#each suggestions as m (m.uuid)}
            <tr>
              <td><a href={machineHref(m.uuid)} onclick={router.link}>{m.name}</a></td>
              <td class="num">{m.currentVcpus} → {m.suggestedVcpus}</td>
              <td class="num">{formatBytes(m.currentMemMiB * 2 ** 20)} → {formatBytes(m.suggestedMemMiB * 2 ** 20)}</td>
              <td class="why">{m.reasons.join(' ')}</td>
              <td>
                <button class="ui-btn sm" disabled={!session.can('admin') || applying !== ''} title={session.why('admin')} onclick={() => apply(m)}>
                  {applying === m.uuid ? t('Applying…') : t('Apply')}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>
{/if}

<style>
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 12px; }
  .card { padding: 16px; }
  .card h2 { display: flex; gap: 8px; align-items: center; margin: 0 0 8px; font-size: 14.5px; }
  .card.urgent, tr.urgent td { border-color: var(--danger); }
  .big { margin: 0 0 4px; font-size: 16px; font-weight: 600; }
  .muted { margin: 0; color: var(--text-3); font-size: 12.5px; }
  .pad { padding: 14px 16px; }
  .sub { font-size: 14px; margin: 22px 0 8px; }
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 8px 10px; border-bottom: 1px solid var(--border); }
  .t td { padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
  .why { color: var(--text-2); max-width: 46ch; }
</style>
