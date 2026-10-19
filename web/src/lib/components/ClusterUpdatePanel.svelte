<script lang="ts">
  import { t } from '../i18n.svelte'
  // Rolling cluster upgrade (ADR-0070): check every member, then update VirtoMir (and
  // optionally the OS packages) one host at a time while its machines live elsewhere.
  import Play from '@lucide/svelte/icons/play'
  import Pause from '@lucide/svelte/icons/pause'
  import Square from '@lucide/svelte/icons/square'
  import ListChecks from '@lucide/svelte/icons/list-checks'
  import { api, errorText, type HostCheck, type LCMPlan, type LCMStatus, type LCMStepKind } from '../api'
  import { cluster } from '../cluster.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  let status = $state<LCMStatus | null>(null)
  let checks = $state<HostCheck[] | null>(null)
  let checking = $state(false)
  let error = $state('')
  let selected = $state<LCMPlan | null>(null)
  let targetVersion = $state('')
  let osUpdates = $state(false)
  let reboot = $state(false)
  let moveBack = $state(true)
  let admin = $derived(session.can('admin'))

  let active = $derived(status?.activePlan ?? null)
  let shown = $derived(selected ?? active)
  let history = $derived((status?.plans ?? []).filter((p) => p.id !== active?.id))

  async function load() {
    try {
      status = await api.lcmStatus()
      if (selected) selected = status.plans.find((p) => p.id === selected!.id) ?? selected
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
    void api
      .get<{ latest?: { version: string }; available: boolean }>('/api/v1/update')
      .then((u) => {
        if (u.available && u.latest && !targetVersion) targetVersion = u.latest.version
      })
      .catch(() => {})
  })
  // Follow a running plan every 2 s.
  $effect(() => {
    if (!active) return
    const timer = setInterval(() => void load(), 2000)
    return () => clearInterval(timer)
  })

  async function precheck() {
    checking = true
    try {
      checks = (await api.lcmPrecheck(targetVersion.trim())).checks
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Pre-checks failed'), detail: errorText(e) })
    } finally {
      checking = false
    }
  }

  async function start() {
    const plan = await api.lcmStart({ targetVersion: targetVersion.trim() || undefined, osUpdates, reboot: osUpdates && reboot, moveBack })
    selected = null
    toasts.push({ kind: 'info', title: t('Rolling upgrade started'), detail: t('{n} hosts, this one last.', { n: plan.hostOrder.length }) })
    await load()
  }

  async function control(fn: (id: string) => Promise<LCMPlan>, id: string) {
    try {
      await fn(id)
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not possible'), detail: errorText(e) })
    }
  }

  const KIND: Record<LCMStepKind, string> = {
    precheck: t('Pre-check'),
    enter_maintenance: t('Enter maintenance'),
    evacuate: t('Move machines away'),
    update_virtomir: t('Update VirtoMir'),
    update_os: t('Upgrade OS packages'),
    reboot: t('Reboot'),
    wait_rejoin: t('Wait until it is back'),
    exit_maintenance: t('Leave maintenance'),
    move_back: t('Move machines back'),
    aborted: t('Aborted'),
  }
  const STATE: Record<string, string> = {
    planned: t('planned'),
    running: t('running'),
    paused: t('paused'),
    succeeded: t('succeeded'),
    failed: t('failed'),
    aborted: t('aborted'),
    done: t('done'),
    skipped: t('skipped'),
  }
  const TONE: Record<string, string> = { running: 'warn', paused: 'warn', succeeded: 'ok', done: 'ok', failed: 'danger', aborted: 'danger', skipped: 'idle', planned: 'idle' }
  let hostName = (id: string) => cluster.members.find((m) => m.id === id)?.name ?? id.slice(0, 8)
  let doneHosts = $derived(
    shown ? shown.hostOrder.filter((h) => (shown!.steps ?? []).some((s) => s.hostId === h && s.kind === 'exit_maintenance' && s.state === 'done')).length : 0,
  )
  let when = (iso: string) => new Date(iso).toLocaleString()
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if status && !status.enabled}
  <p class="muted">{t('Rolling upgrades need a cluster. A single host updates itself on its Updates card (Setup page).')}</p>
{:else if status}
  {#if !active}
    <section class="ui-panel card">
      <h3>{t('Update the cluster')}</h3>
      <p class="muted">
        {t('One host at a time: its running machines move to the other members by live migration, VirtoMir is updated, the host comes back and takes machines again. This host goes last.')}
      </p>
      <div class="form">
        <label class="ui-field">
          <span class="label">{t('Target version')}</span>
          <input class="ui-input mono" bind:value={targetVersion} placeholder={t('the newest release')} />
        </label>
        <label class="ui-switch"><input type="checkbox" bind:checked={osUpdates} /> {t('Also upgrade the OS packages (apt/dnf)')}</label>
        {#if osUpdates}
          <label class="ui-switch"><input type="checkbox" bind:checked={reboot} /> {t('Reboot each host after its packages')}</label>
        {/if}
        <label class="ui-switch"><input type="checkbox" bind:checked={moveBack} /> {t('Move the machines back afterwards')}</label>
      </div>
      <div class="actions">
        <button class="ui-btn" class:busy={checking} disabled={checking} onclick={precheck}><ListChecks size={14} /> {t('Run pre-checks')}</button>
        <ConfirmButton
          label={t('Start rolling upgrade')}
          confirm={t('Start: hosts go into maintenance one by one')}
          icon={Play}
          size="md"
          disabled={!admin}
          title={session.why('admin')}
          failure={t('Rolling upgrade not started')}
          run={start}
        />
      </div>
      {#if checks}
        <table class="t checks">
          <thead><tr><th>{t('Host')}</th><th>{t('Ready')}</th><th>{t('Problems')}</th></tr></thead>
          <tbody>
            {#each checks as c (c.hostId)}
              <tr>
                <td>{c.hostName}</td>
                <td><span class="tag {c.ok ? 'ok' : 'danger'}">{c.ok ? t('ready') : t('blocked')}</span></td>
                <td>{#if c.problems.length}<ul>{#each c.problems as p, i (i)}<li>{p}</li>{/each}</ul>{:else}—{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {/if}

  {#if shown}
    <section class="ui-panel card">
      <header class="plan-head">
        <div>
          <h3>{shown.targetVersion ? t('Upgrade to {v}', { v: shown.targetVersion }) : t('Upgrade to the newest release')}</h3>
          <span class="muted">{t('Started {when} by {user}', { when: when(shown.started), user: shown.startedBy })} · {t('{done} of {total} hosts done', { done: doneHosts, total: shown.hostOrder.length })}</span>
        </div>
        <span class="tag {TONE[shown.state]}">{STATE[shown.state]}</span>
      </header>
      {#if shown.id === active?.id && admin}
        <div class="actions">
          {#if shown.state === 'paused'}
            <button class="ui-btn" onclick={() => control(api.lcmResume, shown!.id)}><Play size={14} /> {t('Resume')}</button>
          {:else}
            <button class="ui-btn" onclick={() => control(api.lcmPause, shown!.id)}><Pause size={14} /> {t('Pause after this step')}</button>
          {/if}
          <ConfirmButton label={t('Abort')} confirm={t('Abort after this step')} icon={Square} run={() => control(api.lcmAbort, shown!.id)} />
        </div>
      {/if}
      <ol class="steps">
        {#each shown.steps ?? [] as s, i (i)}
          <li>
            <span class="time mono">{new Date(s.at).toLocaleTimeString()}</span>
            <span class="host">{s.host || hostName(s.hostId)}</span>
            <span class="kind">{KIND[s.kind] ?? s.kind}</span>
            <span class="tag {TONE[s.state]}">{STATE[s.state] ?? s.state}</span>
            {#if s.message}<span class="msg">{s.message}</span>{/if}
          </li>
        {:else}
          <li class="muted">{t('Starting…')}</li>
        {/each}
      </ol>
      {#if selected}
        <button class="ui-btn ghost sm" onclick={() => (selected = null)}>{t('Close')}</button>
      {/if}
    </section>
  {/if}

  {#if history.length}
    <h3 class="sub">{t('Earlier upgrades')}</h3>
    <table class="t">
      <thead><tr><th>{t('Started')}</th><th>{t('Target')}</th><th>{t('Hosts')}</th><th>{t('Result')}</th></tr></thead>
      <tbody>
        {#each history as p (p.id)}
          <tr class="clickable" onclick={() => (selected = p)}>
            <td>{when(p.started)}</td>
            <td class="mono">{p.targetVersion || t('newest')}</td>
            <td>{p.hostOrder.length}</td>
            <td><span class="tag {TONE[p.state]}">{STATE[p.state]}</span></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<style>
  .card {
    padding: 16px;
    margin-bottom: 16px;
  }
  h3 {
    margin: 0 0 6px;
    font-size: 15px;
  }
  .sub {
    margin: 8px 0;
  }
  .muted {
    color: var(--text-3);
    margin: 0 0 12px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 420px;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin: 12px 0;
    flex-wrap: wrap;
  }
  .plan-head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }
  .tag {
    display: inline-block;
    padding: 1px 7px;
    border: var(--frame) solid var(--ink);
    font: 600 11px var(--font-mono);
    color: var(--text-3);
    white-space: nowrap;
  }
  .tag.ok {
    color: var(--ok);
  }
  .tag.warn {
    color: var(--warn);
  }
  .tag.danger {
    color: var(--danger);
  }
  .steps {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .steps li {
    display: grid;
    grid-template-columns: 76px 110px minmax(140px, auto) auto 1fr;
    gap: 10px;
    align-items: baseline;
    font-size: 13px;
  }
  .time {
    color: var(--text-3);
    font-size: 12px;
  }
  .msg {
    color: var(--text-2);
  }
  .t {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    margin-top: 10px;
  }
  .t th {
    text-align: left;
    font: 500 12px var(--font-sans);
    color: var(--text-2);
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .t td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .checks ul {
    margin: 0;
    padding-left: 16px;
  }
  .clickable {
    cursor: pointer;
  }
  @media (max-width: 720px) {
    .steps li {
      grid-template-columns: 1fr;
      gap: 2px;
    }
  }
</style>
