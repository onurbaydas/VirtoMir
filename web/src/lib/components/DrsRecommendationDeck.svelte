<script lang="ts">
  import { onMount } from 'svelte'
  import { ent, type DRSStatus, type DRSRecommendation, type AffinityRule, type DRSMode, type ClusterDomainSummary } from '../enterprise'
  import { errorText } from '../api'
  import { plural } from '../format'
  import { toasts } from '../toast.svelte'
  import { track } from '../jobs'
  import { t } from '../i18n.svelte'
  import Dialog from './Dialog.svelte'

  const MODE_HINTS: Record<DRSMode, string> = {
    manual: t('DRS only recommends; you apply each move.'),
    partially_automated: t('New machines are suggested the best host; moves stay recommendations.'),
    fully_automated: t('Every 5 minutes each host live-migrates one of its own machines when the cluster is out of balance (never the same machine twice within 30 minutes).'),
  }

  let status = $state<DRSStatus | null>(null)
  let loading = $state(false)
  let applying = $state<string | null>(null)

  // Rule dialog state
  let showRuleDialog = $state(false)
  let newRuleName = $state('')
  let newRuleKind = $state<'vm_anti_affinity' | 'vm_affinity' | 'vm_host_affinity'>('vm_anti_affinity')
  let newRuleMandatory = $state(true)
  let newRuleDomains = $state<string[]>([])
  let newRuleTargetHost = $state('')
  // Machines to pick from, by name (nobody types a UUID).
  let inventory = $state<ClusterDomainSummary[]>([])
  let vmFilter = $state('')
  let pickable = $derived(
    inventory
      .filter((m) => !vmFilter.trim() || `${m.name} ${m.hostName}`.toLowerCase().includes(vmFilter.trim().toLowerCase()))
      .sort((a, b) => a.name.localeCompare(b.name)),
  )
  const KIND_LABEL = {
    vm_anti_affinity: t('Keep apart'),
    vm_affinity: t('Keep together'),
    vm_host_affinity: t('Keep on {host}'),
  }

  async function openRuleDialog() {
    showRuleDialog = true
    newRuleTargetHost ||= status?.hosts[0]?.id ?? ''
    try {
      inventory = await ent.clusterInventoryMachines()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not list the machines'), detail: errorText(e) })
    }
  }

  function toggleDomain(uuid: string) {
    newRuleDomains = newRuleDomains.includes(uuid) ? newRuleDomains.filter((u) => u !== uuid) : [...newRuleDomains, uuid]
  }

  // A name nobody has to think up: what the rule does, with the machines it is about.
  let suggestedName = $derived.by(() => {
    const names = newRuleDomains.map((u) => inventory.find((m) => m.uuid === u)?.name).filter(Boolean)
    if (!names.length) return ''
    const host = status?.hosts.find((h) => h.id === newRuleTargetHost)?.name ?? ''
    const what = KIND_LABEL[newRuleKind].replace('{host}', host)
    return `${what}: ${names.slice(0, 3).join(', ')}${names.length > 3 ? ` +${names.length - 3}` : ''}`
  })
  let ruleProblem = $derived(
    newRuleDomains.length === 0
      ? t('Pick the machines this rule is about.')
      : newRuleKind !== 'vm_host_affinity' && newRuleDomains.length < 2
        ? t('Pick at least two machines to keep apart or together.')
        : newRuleKind === 'vm_host_affinity' && !newRuleTargetHost
          ? t('Pick the host.')
          : '',
  )

  async function load() {
    try {
      loading = true
      status = await ent.drsStatus()
    } catch {
      // DRS might not be enabled
    } finally {
      loading = false
    }
  }

  async function updateMode(mode: DRSMode) {
    if (!status) return
    try {
      await ent.saveDRSConfig({
        enabled: status.enabled,
        mode,
        thresholdStdDev: status.thresholdStdDev || 15.0,
      })
      toasts.push({ kind: 'info', title: t('DRS mode updated to {mode}', { mode }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('That did not work'), detail: errorText(e) })
    }
  }

  // Machines whose move is running: their card stays hidden until the move ends.
  let moving = $state<Set<string>>(new Set())
  let shownRecs = $derived((status?.recommendations ?? []).filter((r) => !moving.has(r.domainUuid)))

  async function applyRecommendation(rec: DRSRecommendation) {
    try {
      applying = rec.id
      const res = await ent.applyDRSRecommendation(rec.id)
      // The move is a background job: follow it (it shows in Tasks too) and look again when it ends.
      const job = 'state' in res && 'id' in res ? res : 'job' in res ? res.job : undefined
      const jobHost = 'jobHost' in res ? res.jobHost : undefined
      const done = () => {
        moving.delete(rec.domainUuid)
        moving = new Set(moving)
        void load()
      }
      if (job?.id) {
        moving = new Set(moving).add(rec.domainUuid)
        track(job, {
          success: t('{vm} now runs on {host}', { vm: rec.domainName, host: rec.targetName }),
          host: jobHost,
          onSuccess: done,
          onFailure: done,
        })
      } else {
        toasts.push({ kind: 'info', title: ('message' in res && res.message) || t('Live migration initiated') })
        await load()
      }
    } catch (e) {
      toasts.push({ kind: 'error', title: t('That did not work'), detail: errorText(e) })
    } finally {
      applying = null
    }
  }

  async function handleAddRule() {
    if (ruleProblem) return
    try {
      await ent.addDRSRule({
        name: newRuleName.trim() || suggestedName,
        kind: newRuleKind,
        mandatory: newRuleMandatory,
        domains: newRuleDomains,
        targetHost: newRuleKind === 'vm_host_affinity' ? newRuleTargetHost : undefined,
      })
      toasts.push({ kind: 'info', title: t('Affinity rule created') })
      showRuleDialog = false
      newRuleName = ''
      newRuleDomains = []
      vmFilter = ''
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('That did not work'), detail: errorText(e) })
    }
  }

  async function handleDeleteRule(id: string) {
    try {
      await ent.deleteDRSRule(id)
      toasts.push({ kind: 'info', title: t('Rule deleted') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('That did not work'), detail: errorText(e) })
    }
  }

  onMount(() => {
    void load()
    // The balance follows the live load: look again now and then, quietly.
    const iv = setInterval(() => void load(), 10_000)
    return () => clearInterval(iv)
  })
</script>

{#if status && status.enabled}
  <div class="drs-deck" data-testid="drs-deck">
    <div class="deck-header">
      <div class="title-cluster">
        <span class="drs-icon">⚡</span>
        <div>
          <h3>{t('DRS (Distributed Resource Scheduler)')}</h3>
          <p class="subtitle">{t('Cluster workload balancing & affinity governance')}</p>
        </div>
      </div>

      <div class="controls-cluster">
        <div class="mode-selector">
          <label for="drs-mode-select">{t('Automation:')}</label>
          <select
            id="drs-mode-select"
            value={status.mode}
            onchange={(e) => updateMode(e.currentTarget.value as DRSMode)}
          >
            <option value="manual">{t('Manual (Recommendations Only)')}</option>
            <option value="partially_automated">{t('Partially Automated')}</option>
            <option value="fully_automated">{t('Fully Automated')}</option>
          </select>
          <span class="mode-hint">{MODE_HINTS[status.mode]}</span>
        </div>

        <button class="btn btn-sm" onclick={openRuleDialog}>
          + {t('Add Rule')}
        </button>
      </div>
    </div>

    <!-- Balance Meter -->
    <div class="balance-strip">
      <div class="gauge-card">
        <div class="gauge-val" class:optimal={(status.balanceScore ?? 0) >= 80} class:warning={(status.balanceScore ?? 0) < 60}>
          {(status.balanceScore ?? 0).toFixed(0)}%
        </div>
        <div class="gauge-label">{t('Cluster Balance Score')}</div>
      </div>

      <div class="metric-skew">
        <div class="skew-item">
          <span class="lbl">{t('CPU Skew (StdDev):')}</span>
          <span class="val font-mono">±{(status.cpuStdDev ?? 0).toFixed(1)}%</span>
        </div>
        <div class="skew-item">
          <span class="lbl">{t('RAM Skew (StdDev):')}</span>
          <span class="val font-mono">±{(status.memStdDev ?? 0).toFixed(1)}%</span>
        </div>
        <div class="skew-item">
          <span class="lbl">{t('Threshold:')}</span>
          <span class="val font-mono">±{(status.thresholdStdDev ?? 0).toFixed(1)}%</span>
        </div>
      </div>
    </div>

    <!-- Recommendations -->
    <div class="recommendations-section">
      <h4>
        {t('Optimization Recommendations')}
        {#if shownRecs.length > 0}
          <span class="count-badge">{shownRecs.length}</span>
        {/if}
      </h4>

      {#if shownRecs.length === 0}
        <div class="empty-recs">
          <span class="check-icon">✓</span>
          {t('Cluster is optimally balanced. No migrations recommended.')}
        </div>
      {:else}
        <div class="rec-list">
          {#each shownRecs as rec (rec.id)}
            <div class="rec-row" class:priority-high={rec.priority >= 4}>
              <div class="rec-stars" title="{rec.priority} of 5 priority">
                {'★'.repeat(rec.priority)}{'☆'.repeat(5 - rec.priority)}
              </div>
              <div class="rec-info">
                <div class="rec-action">
                  <strong>{rec.domainName}</strong>
                  <span class="arrow">➔</span>
                  <span class="host-from">{rec.sourceName}</span>
                  <span class="arrow">➔</span>
                  <span class="host-to">{rec.targetName}</span>
                </div>
                <div class="rec-reason">{rec.reason}</div>
              </div>

              <div class="rec-gain font-mono">
                +{rec.expectedGain}% {t('balance')}
              </div>

              <button
                class="btn btn-sm btn-primary"
                disabled={applying === rec.id}
                onclick={() => applyRecommendation(rec)}
              >
                {applying === rec.id ? t('Migrating…') : t('1-Click Apply')}
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Active Rules -->
    {#if status.rules.length > 0}
      <div class="rules-section">
        <h4>{t('Affinity & Anti-Affinity Rules')}</h4>
        <div class="rules-chips">
          {#each status.rules as rule (rule.id)}
            <div class="rule-chip">
              <span class="kind-tag" class:anti={rule.kind === 'vm_anti_affinity'}>
                {rule.kind === 'vm_anti_affinity' ? t('Keep apart') : rule.kind === 'vm_affinity' ? t('Keep together') : t('Host')}
              </span>
              <span class="name">{rule.name}</span>
              <span class="count">({plural(rule.domains.length, 'machine')})</span>
              <button
                class="del-btn"
                title={t('Delete rule')}
                onclick={() => handleDeleteRule(rule.id)}
              >
                ×
              </button>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>
{/if}

<!-- New Rule Dialog -->
<Dialog
  open={showRuleDialog}
  title={t('New Affinity / Anti-Affinity Rule')}
  onclose={() => (showRuleDialog = false)}
>
  <div class="dialog-form">

    <label>
      {t('Constraint Type')}
      <select bind:value={newRuleKind}>
        <option value="vm_anti_affinity">{t('VM-VM Anti-Affinity (Separate Hosts)')}</option>
        <option value="vm_affinity">{t('VM-VM Affinity (Collocate on Same Host)')}</option>
        <option value="vm_host_affinity">{t('VM-Host Affinity (Pin to Specific Host)')}</option>
      </select>
    </label>

    {#if newRuleKind === 'vm_host_affinity'}
      <label>
        {t('Host')}
        <select bind:value={newRuleTargetHost}>
          {#each status?.hosts ?? [] as h (h.id)}<option value={h.id}>{h.name}</option>{/each}
        </select>
      </label>
    {/if}

    <div class="vm-pick">
      <span class="pick-head">{t('Machines')} <span class="muted">{t('{n} selected', { n: newRuleDomains.length })}</span></span>
      {#if inventory.length > 8}<input type="search" placeholder={t('Filter machines…')} bind:value={vmFilter} />{/if}
      <div class="pick-list">
        {#each pickable as vm (vm.uuid)}
          <label class="pick-item">
            <input type="checkbox" checked={newRuleDomains.includes(vm.uuid)} onchange={() => toggleDomain(vm.uuid)} />
            <span>{vm.name}</span>
            <span class="muted">{vm.hostName}</span>
          </label>
        {:else}
          <p class="muted">{t('No machines found.')}</p>
        {/each}
      </div>
    </div>

    <label>
      {t('Name')} <span class="muted">{t('(optional)')}</span>
      <input type="text" bind:value={newRuleName} placeholder={suggestedName || t('Named after the machines')} />
    </label>

    <label class="checkbox-row">
      <input type="checkbox" bind:checked={newRuleMandatory} />
      <span>{t('Strict Mandatory Rule (Hard constraint)')}</span>
    </label>

    <div class="dialog-actions">
      <button class="btn btn-secondary" onclick={() => (showRuleDialog = false)}>
        {t('Cancel')}
      </button>
      {#if ruleProblem}<span class="muted">{ruleProblem}</span>{/if}
      <button class="btn btn-primary" onclick={handleAddRule} disabled={!!ruleProblem}>
        {t('Create Rule')}
      </button>
    </div>
  </div>
</Dialog>

<style>
  .vm-pick {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .pick-head {
    font-weight: 600;
  }
  .pick-list {
    max-height: 220px;
    overflow: auto;
    border: 1px solid var(--border);
    padding: 4px 8px;
  }
  .pick-item {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 3px 0;
    font-weight: 400;
  }
  .muted {
    color: var(--text-2);
    font-weight: 400;
  }
  .drs-deck {
    background: var(--surface);
    border: 2px solid var(--border);
    margin-bottom: 16px;
    box-shadow: 4px 4px 0 var(--shadow);
  }

  .deck-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 8px;
  }

  .title-cluster {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .drs-icon {
    font-size: 1.5rem;
  }

  .title-cluster h3 {
    margin: 0;
    font-size: 1.05rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .subtitle {
    margin: 0;
    font-size: 0.78rem;
    color: var(--text-3);
  }

  .controls-cluster {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .mode-hint {
    flex-basis: 100%;
    font-size: 12px;
    color: var(--text-3);
  }
  .mode-selector {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-size: 0.82rem;
  }

  .mode-selector select {
    padding: 3px 6px;
    font-size: 0.82rem;
    background: var(--surface);
    border: 1px solid var(--border);
  }

  .balance-strip {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 12px 16px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }

  .gauge-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 8px 16px;
    border: 1px solid var(--border);
    background: var(--surface);
    min-width: 140px;
  }

  .gauge-val {
    font-size: 1.8rem;
    font-weight: 900;
    color: var(--accent);
  }

  .gauge-val.optimal {
    color: var(--ok);
  }

  .gauge-val.warning {
    color: var(--danger);
  }

  .gauge-label {
    font-size: 0.72rem;
    text-transform: uppercase;
    color: var(--text-3);
    font-weight: 600;
  }

  .metric-skew {
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
  }

  .skew-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .skew-item .lbl {
    font-size: 0.72rem;
    color: var(--text-3);
    text-transform: uppercase;
  }

  .skew-item .val {
    font-size: 0.95rem;
    font-weight: 700;
  }

  .recommendations-section,
  .rules-section {
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }

  .recommendations-section h4,
  .rules-section h4 {
    margin: 0 0 8px 0;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .count-badge {
    background: var(--accent);
    color: var(--accent-contrast);
    font-size: 0.7rem;
    padding: 1px 6px;
    font-weight: 800;
  }

  .empty-recs {
    padding: 12px;
    font-size: 0.85rem;
    color: var(--text-3);
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .check-icon {
    color: var(--ok);
    font-weight: 900;
  }

  .rec-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .rec-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 12px;
    background: var(--surface);
    border: 1px solid var(--border);
  }

  .rec-row.priority-high {
    border-left: 4px solid var(--accent);
  }

  .rec-stars {
    color: var(--warn);
    font-size: 0.85rem;
    letter-spacing: 1px;
    white-space: nowrap;
  }

  .rec-info {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .rec-action {
    font-size: 0.85rem;
    display: flex;
    align-items: center;
    gap: 4px;
  }

  .arrow {
    color: var(--text-3);
  }

  .host-from {
    color: var(--danger);
  }

  .host-to {
    color: var(--ok);
  }

  .rec-reason {
    font-size: 0.75rem;
    color: var(--text-3);
  }

  .rec-gain {
    font-size: 0.82rem;
    color: var(--ok);
    white-space: nowrap;
  }

  .rules-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .rule-chip {
    display: flex;
    align-items: center;
    gap: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    padding: 3px 8px;
    font-size: 0.78rem;
  }

  .kind-tag {
    font-size: 0.65rem;
    padding: 1px 4px;
    background: var(--info);
    color: var(--accent-contrast);
    font-weight: 700;
  }

  .kind-tag.anti {
    background: var(--danger);
  }

  .rule-chip .name {
    font-weight: 600;
  }

  .rule-chip .count {
    color: var(--text-3);
  }

  .del-btn {
    background: none;
    border: none;
    color: var(--text-3);
    cursor: pointer;
    font-size: 1rem;
    padding: 0 3px;
  }

  .del-btn:hover {
    color: var(--danger);
  }

  .dialog-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .dialog-form label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 0.82rem;
  }

  .dialog-form input,
  .dialog-form select,
  .checkbox-row {
    flex-direction: row !important;
    align-items: center;
  }

  .dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 8px;
  }
</style>
