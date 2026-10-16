<script lang="ts">
  import { t } from '../i18n.svelte'
  // Quotas (ADR-0022): your own, and for admins the rules — which users or Linux groups may
  // create machines themselves, and how much.
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import Gauge from '@lucide/svelte/icons/gauge'
  import { api, errorText, type QuotaRule } from '../api'
  import { cluster } from '../cluster.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let rules = $state<QuotaRule[] | null>(null)
  let saved = $state('')
  let error = $state('')
  let saving = $state(false)
  let everywhere = $state(true)
  let admin = $derived(session.can('admin'))

  $effect(() => {
    void session.loadQuota()
    if (!admin) return
    api
      .quotas()
      .then((r) => {
        rules = r
        saved = JSON.stringify(r)
      })
      .catch((e) => (error = errorText(e)))
  })
  let dirty = $derived(rules !== null && JSON.stringify(rules) !== saved)

  function add() {
    rules = [...(rules ?? []), { kind: 'group', name: 'virtomir-operator', machines: 3, vcpus: 8, memoryMiB: 16384 }]
  }

  async function save() {
    if (!rules) return
    saving = true
    error = ''
    const snap = $state.snapshot(rules) as QuotaRule[]
    try {
      if (cluster.clustered && everywhere) {
        const res = await cluster.everywhere((h) => api.saveQuotas(snap, h))
        const failed = res.filter((r) => r.error)
        if (failed.length === res.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
        if (failed.length) toasts.push({ kind: 'warn', title: t('Saved on {v} of {length} hosts', { v: res.length - failed.length, length: res.length }), detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') })
        else toasts.push({ kind: 'info', title: t('Quotas saved on every host') })
      } else {
        await api.saveQuotas(snap)
        toasts.push({ kind: 'info', title: t('Quotas saved') })
      }
      saved = JSON.stringify(snap)
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = false
    }
  }

  const pct = (a: number, b: number) => (b ? Math.min(100, (a / b) * 100) : 100)
</script>

{#if session.quota?.rule && session.quota.used}
  {@const q = session.quota}
  <section class="ui-panel card">
    <h2><Gauge size={16} /> {t('Your quota')}</h2>
    <p class="hint">{t('You may create machines yourself up to these limits{v}, and change or delete the ones you created.', { v: cluster.clustered ? ' (on this host)' : '' })}</p>
    {#each [['Machines', q.used!.machines, q.rule!.machines, ''], ['vCPUs', q.used!.vcpus, q.rule!.vcpus, ''], ['Memory', q.used!.memoryMiB, q.rule!.memoryMiB, ' MiB'], ...(q.rule!.diskGiB != null ? [['Disk', q.used!.diskGiB ?? 0, q.rule!.diskGiB, ' GiB']] : [])] as [label, used, max, unit] (label)}
      <div class="meter">
        <span class="m-label">{label}</span>
        <span class="bar" class:full={Number(used) >= Number(max)}><span style:width="{pct(Number(used), Number(max))}%"></span></span>
        <span class="m-val mono">{used} / {max}{unit}</span>
      </div>
    {/each}
  </section>
{/if}

{#if admin}
  <section class="ui-panel card">
    <h2><Gauge size={16} /> {t('Quotas')}</h2>
    <p class="hint">
      {t('Operators covered by a rule can create machines themselves (wizard, templates) within it, and change or delete the ones they created. A user\'s own rule wins over their groups; of several groups, the most generous counts. Admins have no limit.')}
    </p>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    {#if rules === null}
      <p class="hint">{t('Loading…')}</p>
    {:else}
      {#if rules.length}
        <div class="rules">
          <span class="th">{t('For')}</span><span class="th">{t('Name')}</span><span class="th">{t('Machines')}</span><span class="th">{t('vCPUs')}</span><span class="th">{t('Memory (MiB)')}</span><span class="th" title="The full size of their machines' own disks; empty: no limit">{t('Disk (GiB)')}</span><span></span>
          {#each rules as r, i (i)}
            <select class="ui-input" bind:value={r.kind} aria-label={t('Rule for')}>
              <option value="group">{t('Group')}</option>
              <option value="user">{t('User')}</option>
            </select>
            <input class="ui-input mono" bind:value={r.name} placeholder={r.kind === 'group' ? 'virtomir-operator' : 'username'} aria-label={t('Name')} />
            <input class="ui-input mono" type="number" min="0" bind:value={r.machines} aria-label={t('Machines')} />
            <input class="ui-input mono" type="number" min="0" bind:value={r.vcpus} aria-label={t('vCPUs')} />
            <input class="ui-input mono" type="number" min="0" step="512" bind:value={r.memoryMiB} aria-label={t('Memory')} />
            <input class="ui-input mono" type="number" min="0" step="10" bind:value={r.diskGiB} placeholder={t('no limit')} aria-label={t('Disk (GiB)')} />
            <button class="ui-btn sm ghost" onclick={() => (rules = rules!.filter((_, j) => j !== i))} aria-label={t('Remove rule')}><Trash size={14} /></button>
          {/each}
        </div>
      {:else}
        <p class="hint">{t('No rules: only admins create machines.')}</p>
      {/if}
      <div class="actions">
        <button class="ui-btn sm" onclick={add}><Plus size={14} /> {t('Add rule')}</button>
        <span class="spacer"></span>
        {#if cluster.clustered}<label class="ui-switch"><input type="checkbox" bind:checked={everywhere} /> {t('Same on every host')}</label>{/if}
        <button class="ui-btn primary" disabled={!dirty || saving} onclick={save}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    {/if}
  </section>
{/if}

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 18px;
    margin-bottom: 14px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
  }
  h2 :global(svg) {
    color: var(--accent);
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .rules {
    display: grid;
    grid-template-columns: 110px minmax(140px, 1fr) 90px 90px 120px auto;
    gap: 8px;
    align-items: center;
  }
  .th {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-3);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .spacer {
    flex: 1;
  }
  .meter {
    display: grid;
    grid-template-columns: 80px 1fr 130px;
    gap: 10px;
    align-items: center;
    font-size: 13px;
  }
  .m-label {
    color: var(--text-3);
  }
  .m-val {
    text-align: right;
  }
  .bar {
    height: 6px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .bar.full span {
    background: var(--warn);
  }
  @media (max-width: 760px) {
    .rules {
      grid-template-columns: 1fr 1fr;
    }
    .th {
      display: none;
    }
  }
</style>
