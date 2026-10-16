<script lang="ts">
  import { t } from '../i18n.svelte'
  // Reports (ADR-0032): capacity, inventory and backup compliance over every cluster member,
  // made now or on a schedule. Every number in them was measured; nothing is estimated.
  import FileBarChart from '@lucide/svelte/icons/file-chart-column'
  import Server from '@lucide/svelte/icons/server'
  import ListChecks from '@lucide/svelte/icons/list-checks'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Download from '@lucide/svelte/icons/download'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import Trash from '@lucide/svelte/icons/trash'
  import Plus from '@lucide/svelte/icons/plus'
  import { errorText } from '../api'
  import { ent, type ReportFormat, type ReportKind, type ReportMeta, type ReportSchedule } from '../enterprise'
  import { formatBytes } from '../format'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'

  const KINDS: { kind: ReportKind; title: string; text: string; icon: typeof Server }[] = [
    { kind: 'capacity', title: t('Capacity'), text: t('CPUs, vCPUs and memory per host (allocated and on running machines), storage pools with used and free space.'), icon: Server },
    { kind: 'inventory', title: t('Machine inventory'), text: t('Every machine with its host, state, size, disks, addresses and owner.'), icon: ListChecks },
    { kind: 'backup_sla', title: t('Backup compliance'), text: "Each machine's newest backup against its backup schedule: ok, late, overdue or never.", icon: ShieldCheck },
  ]

  let list = $state<ReportMeta[]>([])
  let schedules = $state<ReportSchedule[]>([])
  let error = $state('')
  let format = $state<ReportFormat>('html')
  let busy = $state<ReportKind | ''>('')

  async function load() {
    try {
      ;[list, schedules] = await Promise.all([ent.reports(), ent.reportSchedules()])
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  async function generate(kind: ReportKind) {
    busy = kind
    try {
      const m = await ent.generateReport(kind, format)
      toasts.push({ kind: 'info', title: m.title, detail: m.summary })
      await load()
      if (m.format === 'html') window.open(ent.reportURL(m.id), '_blank', 'noopener')
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Report failed'), detail: errorText(e) })
    } finally {
      busy = ''
    }
  }

  async function remove(id: string) {
    await ent.deleteReport(id)
    await load()
  }

  // ---- Schedules ------------------------------------------------------------------------
  let editing = $state<ReportSchedule | null>(null)
  function newSchedule() {
    editing = { name: t('Weekly capacity'), kind: 'capacity', format: 'html', enabled: true, every: 'weekly', at: '06:00', keepDays: 90, notify: false }
  }
  async function saveSchedule() {
    if (!editing) return
    try {
      await ent.saveReportSchedule(editing)
      editing = null
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    }
  }
  async function removeSchedule(id: string) {
    await ent.deleteReportSchedule(id)
    await load()
  }
  const kindTitle = (k: ReportKind) => KINDS.find((x) => x.kind === k)?.title ?? k
  let admin = $derived(session.can('admin'))
</script>

<svelte:head><title>{t('Reports · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Reports')}</h1>
    <p class="lead">{t('Covers every host of the cluster that answers. HTML reports print well (Ctrl+P → Save as PDF); CSV opens in a spreadsheet.')}</p>
  </div>
  <label class="ui-field fmt">
    <span class="label">{t('Format')}</span>
    <select class="ui-input" bind:value={format}>
      <option value="html">{t('HTML (view, print to PDF)')}</option>
      <option value="csv">{t('CSV (spreadsheet)')}</option>
      <option value="json">{t('JSON (scripts)')}</option>
    </select>
  </label>
</header>

{#if error}<div class="ui-error-box">{error}</div>{/if}

<div class="kinds">
  {#each KINDS as k (k.kind)}
    <section class="ui-panel kind">
      <h2><k.icon size={18} /> {k.title}</h2>
      <p>{k.text}</p>
      <button class="ui-btn primary" disabled={!admin || !!busy} title={session.why('admin')} onclick={() => generate(k.kind)}>
        <FileBarChart size={14} /> {busy === k.kind ? 'Collecting from every host…' : t('Make report')}
      </button>
    </section>
  {/each}
</div>

<h2 class="sub">{t('Schedules')}</h2>
<section class="ui-panel">
  {#if schedules.length === 0}
    <p class="muted pad">{t('No scheduled reports.')}</p>
  {:else}
    <table class="t">
      <thead><tr><th>{t('Name')}</th><th>{t('Report')}</th><th>{t('When')}</th><th>{t('Keeps')}</th><th>{t('Last run')}</th><th>{t('Next')}</th><th></th></tr></thead>
      <tbody>
        {#each schedules as s (s.id)}
          <tr>
            <td><b>{s.name}</b>{#if !s.enabled} <span class="muted">{t('(off)')}</span>{/if}{#if s.notify} <span class="chip">{t('notifies')}</span>{/if}</td>
            <td>{kindTitle(s.kind)} · {s.format.toUpperCase()}</td>
            <td>{s.every} at {s.at}</td>
            <td>{s.keepDays ? `${s.keepDays} days` : 'forever'}</td>
            <td class:bad={!!s.lastError}>{s.lastRun ? new Date(s.lastRun).toLocaleString() : 'never'}{#if s.lastError} — {s.lastError}{/if}</td>
            <td>{s.nextRun ? new Date(s.nextRun).toLocaleString() : '—'}</td>
            <td class="acts">{#if admin}
              <button class="ui-btn sm ghost" onclick={() => (editing = { ...s })}>{t('Edit')}</button>
              <ConfirmButton label={t('Delete')} confirm={t('Delete schedule?')} icon={Trash} run={() => removeSchedule(s.id!)} />
            {/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
  {#if admin}<div class="pad"><button class="ui-btn sm" onclick={newSchedule}><Plus size={13} /> {t('New schedule')}</button></div>{/if}
</section>

<h2 class="sub">{t('Made so far')}</h2>
<section class="ui-panel">
  {#if list.length === 0}
    <p class="muted pad">{t('No reports yet.')}</p>
  {:else}
    <table class="t">
      <thead><tr><th>{t('Report')}</th><th>{t('Summary')}</th><th>{t('By')}</th><th>{t('Size')}</th><th></th></tr></thead>
      <tbody>
        {#each list as r (r.id)}
          <tr>
            <td><b>{r.title}</b><div class="muted mono small">{r.format.toUpperCase()} · {new Date(r.generatedAt).toLocaleString()}</div></td>
            <td class="summary">{r.summary}</td>
            <td>{r.by}</td>
            <td class="mono">{formatBytes(r.sizeBytes)}</td>
            <td class="acts">
              <a class="ui-btn sm" href={ent.reportURL(r.id)} target="_blank" rel="noopener"><ExternalLink size={13} /> {t('Open')}</a>
              <a class="ui-btn sm ghost" href={ent.reportURL(r.id, true)} download><Download size={13} /></a>
              {#if admin}<ConfirmButton label="" confirm={t('Delete?')} icon={Trash} run={() => remove(r.id)} />{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>

<Dialog open={!!editing} title={editing?.id ? 'Edit schedule' : t('New report schedule')} onclose={() => (editing = null)} width={520}>
  {#if editing}
    <div class="form">
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={editing.name} /></label>
      <div class="two">
        <label class="ui-field"><span class="label">{t('Report')}</span>
          <select class="ui-input" bind:value={editing.kind}>{#each KINDS as k (k.kind)}<option value={k.kind}>{k.title}</option>{/each}</select></label>
        <label class="ui-field"><span class="label">{t('Format')}</span>
          <select class="ui-input" bind:value={editing.format}><option value="html">{t('HTML')}</option><option value="csv">{t('CSV')}</option><option value="json">{t('JSON')}</option></select></label>
        <label class="ui-field"><span class="label">{t('Every')}</span>
          <select class="ui-input" bind:value={editing.every}><option value="daily">{t('day')}</option><option value="weekly">{t('Monday')}</option><option value="monthly">{t('1st of the month')}</option></select></label>
        <label class="ui-field"><span class="label">{t('At')}</span><input class="ui-input" type="time" bind:value={editing.at} /></label>
        <label class="ui-field"><span class="label">{t('Keep its reports (days, 0 = forever)')}</span><input class="ui-input" type="number" min="0" bind:value={editing.keepDays} /></label>
      </div>
      <label class="ui-switch"><input type="checkbox" bind:checked={editing.enabled} /> {t('Enabled')}</label>
      <label class="ui-switch"><input type="checkbox" bind:checked={editing.notify} /> {t('Send the summary through the alert channels (Alerts → Channels)')}</label>
      <p class="hint">{t('Schedules run on this host; the report covers the whole cluster, so set them up on one host only.')}</p>
    </div>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (editing = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" onclick={saveSchedule}>{t('Save')}</button>
  {/snippet}
</Dialog>

<style>
  .lead { color: var(--text-2); margin: 4px 0 0; }
  .fmt { min-width: 220px; }
  .kinds { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 12px; }
  .kind { padding: 16px; display: flex; flex-direction: column; gap: 8px; }
  .kind h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .kind p { margin: 0; color: var(--text-2); font-size: 13px; flex: 1; }
  .sub { font-size: 14px; margin: 22px 0 8px; }
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 8px 10px; border-bottom: 1px solid var(--border); }
  .t td { padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
  .summary { max-width: 420px; color: var(--text-2); }
  .acts { white-space: nowrap; display: flex; gap: 4px; }
  .pad { padding: 12px 14px; margin: 0; }
  .muted { color: var(--text-2); }
  .small { font-size: 11px; }
  .bad { color: var(--danger); }
  .chip { border: 1px solid currentColor; padding: 0 5px; font-size: 10px; }
  .form { display: flex; flex-direction: column; gap: 10px; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .hint { color: var(--text-2); font-size: 12px; margin: 0; }
  @media (max-width: 640px) { .two { grid-template-columns: 1fr; } }
</style>
