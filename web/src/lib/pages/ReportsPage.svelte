<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../api'
  import { toasts } from '../toast.svelte'
  import FileText from '@lucide/svelte/icons/file-text'
  import Download from '@lucide/svelte/icons/download'
  import Eye from '@lucide/svelte/icons/eye'
  import Trash2 from '@lucide/svelte/icons/trash-2'
  import Play from '@lucide/svelte/icons/play'
  import Calendar from '@lucide/svelte/icons/calendar'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Printer from '@lucide/svelte/icons/printer'
  import Dialog from '../components/Dialog.svelte'

  interface ReportMetadata {
    id: string
    type: 'capacity' | 'inventory' | 'backup_sla' | 'audit_activity'
    title: string
    format: 'html' | 'csv' | 'json'
    generatedAt: string
    timeRange: string
    sizeBytes: number
    summary: string
  }

  interface Schedule {
    id: string
    name: string
    type: string
    format: string
    cadence: string
    enabled: boolean
    deliveryWebhook?: string
    deliveryEmail?: string
    keepDays: number
    lastRun?: string
  }

  let reports = $state<ReportMetadata[]>([])
  let schedules = $state<Schedule[]>([])
  let loading = $state(true)
  let generating = $state(false)

  let selectedFormat = $state<'html' | 'csv' | 'json'>('html')
  let previewModalOpen = $state(false)
  let previewTitle = $state('')
  let previewContent = $state('')
  let previewFormat = $state('')

  async function loadData() {
    loading = true
    try {
      const [r, s] = await Promise.all([
        api.get<ReportMetadata[]>('/api/v1/reports'),
        api.get<Schedule[]>('/api/v1/reports/schedules'),
      ])
      reports = r
      schedules = s
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to load reports', detail: e.message })
    } finally {
      loading = false
    }
  }

  onMount(loadData)

  async function generate(type: 'capacity' | 'inventory' | 'backup_sla') {
    generating = true
    try {
      const meta = await api.post<ReportMetadata>('/api/v1/reports/generate', {
        type,
        format: selectedFormat,
      })
      toasts.push({ kind: 'info', title: 'Report Generated', detail: meta.title })
      await loadData()
      openPreview(meta.id)
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Generation failed', detail: e.message })
    } finally {
      generating = false
    }
  }

  async function openPreview(id: string) {
    try {
      const res = await api.get<{ metadata: ReportMetadata; content: string }>(`/api/v1/reports/${id}`)
      previewTitle = res.metadata.title
      previewFormat = res.metadata.format
      previewContent = res.content
      previewModalOpen = true
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to fetch report preview', detail: e.message })
    }
  }

  async function deleteReport(id: string) {
    if (!confirm('Are you sure you want to delete this report?')) return
    try {
      await api.delete(`/api/v1/reports/${id}`)
      toasts.push({ kind: 'info', title: 'Report Deleted' })
      await loadData()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Delete failed', detail: e.message })
    }
  }

  function printPreview() {
    const w = window.open('', '_blank')
    if (!w) return
    w.document.write(previewContent)
    w.document.close()
    w.focus()
    setTimeout(() => w.print(), 250)
  }
</script>

<svelte:head><title>Reports & Compliance · VirtoMir</title></svelte:head>

<div class="reports-page">
  <header class="page-head">
    <div>
      <div class="title-row">
        <FileText size={22} />
        <h1>Audits & Compliance Reports</h1>
      </div>
      <div class="sub mono">
        Automated capacity forecasting, guest workload inventories, and disaster recovery SLA audits.
      </div>
    </div>
    <div class="head-right">
      <div class="fmt-selector mono">
        <span class="fmt-label">FORMAT:</span>
        <button class="fmt-btn" class:active={selectedFormat === 'html'} onclick={() => selectedFormat = 'html'}>HTML</button>
        <button class="fmt-btn" class:active={selectedFormat === 'csv'} onclick={() => selectedFormat = 'csv'}>CSV</button>
        <button class="fmt-btn" class:active={selectedFormat === 'json'} onclick={() => selectedFormat = 'json'}>JSON</button>
      </div>
    </div>
  </header>

  <!-- Quick Generator Action Deck -->
  <div class="generators-grid">
    <div class="ui-panel gen-card">
      <div class="gc-head">
        <span class="gc-icon mono">01</span>
        <div>
          <div class="gc-title mono">CLUSTER CAPACITY & SATURATION RUNWAY</div>
          <div class="gc-desc">Linear projection of compute, RAM and storage exhaustion based on active 30-day growth.</div>
        </div>
      </div>
      <button class="ui-btn primary mono" onclick={() => generate('capacity')} disabled={generating}>
        <Play size={14} /> GENERATE AUDIT REPORT
      </button>
    </div>

    <div class="ui-panel gen-card">
      <div class="gc-head">
        <span class="gc-icon mono">02</span>
        <div>
          <div class="gc-title mono">FLEET WORKLOAD INVENTORY MATRIX</div>
          <div class="gc-desc">Complete ledger of all guest machines, allocated vCPUs/RAM, virtual disks, MACs and IP bindings.</div>
        </div>
      </div>
      <button class="ui-btn primary mono" onclick={() => generate('inventory')} disabled={generating}>
        <Play size={14} /> GENERATE INVENTORY
      </button>
    </div>

    <div class="ui-panel gen-card">
      <div class="gc-head">
        <span class="gc-icon mono">03</span>
        <div>
          <div class="gc-title mono">BACKUP SLA & DISASTER RECOVERY</div>
          <div class="gc-desc">Audits point-in-time backup consistency, recovery point objective (RPO) compliance and image sizes.</div>
        </div>
      </div>
      <button class="ui-btn primary mono" onclick={() => generate('backup_sla')} disabled={generating}>
        <Play size={14} /> GENERATE SLA AUDIT
      </button>
    </div>
  </div>

  <!-- Generated Reports Archive -->
  <section class="ui-panel">
    <div class="panel-head">
      <div class="ph-title mono">GENERATED AUDIT ARCHIVE ({reports.length})</div>
      <button class="ui-btn icon-only" onclick={loadData} title="Refresh list"><RefreshCw size={14} /></button>
    </div>
    <div class="table-wrap">
      <table class="data-table mono">
        <thead>
          <tr>
            <th>DATE</th>
            <th>REPORT TITLE</th>
            <th>TYPE</th>
            <th>FORMAT</th>
            <th>SIZE</th>
            <th>SUMMARY</th>
            <th>ACTIONS</th>
          </tr>
        </thead>
        <tbody>
          {#if !reports.length}
            <tr><td colspan="7" class="empty">No generated audit reports found. Click a generator above.</td></tr>
          {:else}
            {#each reports as r (r.id)}
              <tr>
                <td>{new Date(r.generatedAt).toLocaleString()}</td>
                <td><strong>{r.title}</strong></td>
                <td><span class="type-pill">{r.type.toUpperCase()}</span></td>
                <td><span class="fmt-pill {r.format}">{r.format.toUpperCase()}</span></td>
                <td>{(r.sizeBytes / 1024).toFixed(1)} KB</td>
                <td class="text-dim">{r.summary}</td>
                <td class="acts-cell">
                  <button class="ui-btn small mono" onclick={() => openPreview(r.id)} title="Preview report">
                    <Eye size={12} /> VIEW
                  </button>
                  <a class="ui-btn small icon-only" href="/api/v1/reports/{r.id}/download" download title="Download report file">
                    <Download size={12} />
                  </a>
                  <button class="ui-btn danger small icon-only" onclick={() => deleteReport(r.id)} title="Delete report">
                    <Trash2 size={12} />
                  </button>
                </td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </section>

  <!-- Automated Schedules Card -->
  <section class="ui-panel">
    <div class="panel-head">
      <div class="ph-title mono">AUTOMATED CADENCE SCHEDULES</div>
      <span class="sched-pill mono">{schedules.filter(s => s.enabled).length} ACTIVE SCHEDULES</span>
    </div>
    <div class="schedules-grid">
      {#each schedules as s}
        <div class="schedule-card mono">
          <div class="sc-head">
            <Calendar size={14} />
            <span class="sc-name">{s.name}</span>
            <span class="sc-cadence">{s.cadence.toUpperCase()}</span>
          </div>
          <div class="sc-meta">
            Format: {s.format.toUpperCase()} · Retention: {s.keepDays} days
          </div>
          {#if s.deliveryWebhook}
            <div class="sc-hook">Webhook: {s.deliveryWebhook}</div>
          {/if}
        </div>
      {/each}
    </div>
  </section>
</div>

<!-- Preview Modal -->
<Dialog title={previewTitle} bind:open={previewModalOpen}>
  <div class="preview-deck">
    <div class="preview-bar">
      <span class="mono">PREVIEW FORMAT: {previewFormat.toUpperCase()}</span>
      {#if previewFormat === 'html'}
        <button class="ui-btn primary small mono" onclick={printPreview}>
          <Printer size={13} /> PRINT / EXPORT PDF
        </button>
      {/if}
    </div>

    {#if previewFormat === 'html'}
      <iframe class="preview-frame" srcdoc={previewContent} title="Report Preview"></iframe>
    {:else}
      <pre class="preview-pre mono">{previewContent}</pre>
    {/if}
  </div>
</Dialog>

<style>
  .reports-page {
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 4px;
  }
  .title-row h1 {
    margin: 0;
  }
  .sub {
    font-size: 12px;
    color: var(--text-dim);
  }
  .fmt-selector {
    display: flex;
    align-items: center;
    gap: 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    padding: 3px;
  }
  .fmt-label {
    font-size: 10px;
    color: var(--text-dim);
    padding: 0 6px;
  }
  .fmt-btn {
    padding: 3px 8px;
    background: transparent;
    border: none;
    color: var(--text-dim);
    font-family: inherit;
    font-size: 11px;
    font-weight: 700;
    cursor: pointer;
  }
  .fmt-btn.active {
    background: var(--accent);
    color: #fff;
  }
  .generators-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 16px;
  }
  @media (max-width: 980px) {
    .generators-grid {
      grid-template-columns: 1fr;
    }
  }
  .gen-card {
    padding: 16px;
    background: var(--surface-1);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 0 var(--border-strong);
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 16px;
  }
  .gc-head {
    display: flex;
    gap: 12px;
  }
  .gc-icon {
    font-size: 16px;
    font-weight: 700;
    color: var(--accent);
  }
  .gc-title {
    font-size: 12px;
    font-weight: 700;
    color: var(--text-strong);
  }
  .gc-desc {
    font-size: 11px;
    color: var(--text-dim);
    margin-top: 4px;
    line-height: 1.4;
  }
  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
  }
  .ph-title {
    font-size: 12px;
    font-weight: 700;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .data-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .data-table th {
    text-align: left;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    color: var(--text-dim);
  }
  .data-table td {
    padding: 10px 16px;
    border-bottom: 1px solid var(--border-soft);
  }
  .empty {
    text-align: center;
    color: var(--text-dim);
    padding: 24px !important;
  }
  .type-pill {
    padding: 2px 5px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 10px;
  }
  .fmt-pill {
    padding: 2px 5px;
    font-size: 10px;
    font-weight: 700;
  }
  .fmt-pill.html { color: var(--ok); }
  .fmt-pill.csv { color: var(--accent); }
  .fmt-pill.json { color: var(--warn); }
  .acts-cell {
    display: flex;
    gap: 4px;
  }
  .schedules-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
    padding: 16px;
  }
  .schedule-card {
    background: var(--surface-2);
    border: 1px solid var(--border);
    padding: 12px;
  }
  .sc-head {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    font-weight: 700;
  }
  .sc-cadence {
    margin-left: auto;
    font-size: 10px;
    background: var(--accent);
    color: #fff;
    padding: 1px 4px;
  }
  .sc-meta {
    font-size: 11px;
    color: var(--text-dim);
    margin-top: 6px;
  }
  .sc-hook {
    font-size: 10px;
    color: var(--text-dim);
    margin-top: 4px;
    word-break: break-all;
  }
  .sched-pill {
    font-size: 10px;
    padding: 2px 6px;
    background: var(--surface-1);
    border: 1px solid var(--border);
  }
  .preview-deck {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .preview-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 12px;
  }
  .preview-frame {
    width: 100%;
    height: 550px;
    border: 1px solid var(--border);
    background: #121316;
  }
  .preview-pre {
    height: 500px;
    overflow: auto;
    padding: 14px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 12px;
  }
</style>
