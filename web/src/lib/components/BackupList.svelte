<script lang="ts">
  import { t } from '../i18n.svelte'
  // Backups of one machine (or all, grouped on the Backups page) with restore and delete.
  import ArchiveRestore from '@lucide/svelte/icons/archive-restore'
  import Trash from '@lucide/svelte/icons/trash'
  import Zap from '@lucide/svelte/icons/zap'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Power from '@lucide/svelte/icons/power'
  import CloudUpload from '@lucide/svelte/icons/cloud-upload'
  import { api, errorText, type BackupInfo, type Pool } from '../api'
  import { formatBytes } from '../format'
  import { track } from '../jobs'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { cluster } from '../cluster.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  /** host: the cluster member the backups are on, when it is not necessarily the one shown. */
  /** offsite: this host copies backups off-site (ADR-0043); shows "Copy off-site". */
  let { backups, onchange, host, offsite = false }: { backups: BackupInfo[]; onchange: () => void; host?: string; offsite?: boolean } = $props()
  async function copyOffsite(b: BackupInfo) {
    try {
      const job = await api.offsiteUpload(b.id, host)
      track(job, { success: t('{domain}: copied off-site', { domain: b.domain }), host, onSuccess: onchange })
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Could not copy off-site'), detail: errorText(err) })
    }
  }

  const when = (s: string) => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
  const took = (s: number) => (s < 60 ? `${Math.round(s)} s` : `${Math.round(s / 60)} min`)

  const RESTORE_POOLS = ['dir', 'netfs', 'fs', 'logical', 'zfs']

  async function verify(b: BackupInfo) {
    try {
      const job = await api.verifyBackup(b.id, host)
      track(job, { success: t('{domain}: backup checked', { domain: b.domain }), host, onSuccess: onchange, onFailure: onchange })
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Could not check the backup'), detail: errorText(err) })
    }
  }

  // ---- Restore ------------------------------------------------------------------------
  let restoring = $state<BackupInfo | null>(null)
  let name = $state('')
  let pool = $state('default')
  let pools = $state<Pool[]>([])
  let error = $state('')
  async function openRestore(b: BackupInfo) {
    restoring = b
    name = `${b.domain}-restored`
    error = ''
    try {
      pools = (await (host ? api.poolsOn(host) : api.pools())).filter((p) => p.active && RESTORE_POOLS.includes(p.type))
      if (!pools.some((p) => p.name === pool) && pools[0]) pool = pools[0].name
    } catch (e) {
      error = errorText(e)
    }
  }
  let restoreBusy = $state(false)
  // A restore refused because the backup's files changed or failed their check: the reason
  // and what to retry with, so the person can accept the risk and go on.
  let refused = $state<{ b: BackupInfo; name: string; pool: string; message: string } | null>(null)
  async function restore(e: SubmitEvent) {
    e.preventDefault()
    if (restoring) await startRestore(restoring, name, pool, false)
  }
  async function startRestore(b: BackupInfo, asName: string, asPool: string, force: boolean) {
    if (restoreBusy) return
    restoreBusy = true
    try {
      const job = await api.restoreBackup(b.id, asName, asPool, host, force)
      restoring = null
      refused = null
      const m = host ? cluster.members.find((x) => x.id === host) : undefined
      track(job, {
        success: `${asName} restored`,
        host,
        onFailure: (j) => {
          if (/restore it anyway/i.test(j.message ?? '')) refused = { b, name: asName, pool: asPool, message: j.message ?? '' }
        },
        onSuccess: (j) => j.result && (m ? router.go(m.self ? null : m.id, `/machines/${j.result}`) : router.navigate(machineHref(j.result))),
      })
    } catch (err) {
      error = errorText(err)
    } finally {
      restoreBusy = false
    }
  }
  let needed = $derived(restoring ? restoring.disks.reduce((n, d) => n + d.sizeBytes, 0) : 0)
  let target = $derived(pools.find((p) => p.name === pool))
</script>

<div class="list">
  {#each backups as b (b.id)}
    <div class="backup">
      <span class="kind" title={b.live ? 'Taken while the machine was running (no downtime)' : t('Taken while the machine was off')}>
        {#if b.live}<Zap size={15} />{:else}<Power size={15} />{/if}
      </span>
      <div class="info">
        <span class="when">
          {when(b.created)}
          {#if b.automatic}<span class="tag">{t('automatic')}</span>{/if}
          {#if b.checkError}<span class="tag bad" title={b.checkError}>{t('check failed')}</span>
          {:else if b.verified}<span class="tag ok" title={t('Files checked {time}', { time: when(b.verified) })}>{t('verified')}</span>{/if}
          {#if b.incremental}<span class="tag" title={t('Holds only what changed since the backup before it, which it needs to be restored.')}>{t('incremental')}</span>{/if}
        </span>
        <span class="meta">{formatBytes(b.sizeBytes)} · {b.disks.map((d) => d.target).join(', ')} · {b.live ? 'live' : 'offline'} · took {took(b.durationSec)}</span>
      </div>
      {#if offsite}
        <button class="ui-btn sm ghost" disabled={!session.can('admin')} title={session.why('admin') || t('Copy this backup to the off-site repository')} onclick={() => copyOffsite(b)}>
          <CloudUpload size={14} /> {t('Copy off-site')}
        </button>
      {/if}
      <button class="ui-btn sm ghost" disabled={!session.can('admin')} title={session.why('admin') || t('Check that the backup files are intact')} onclick={() => verify(b)}>
        <ShieldCheck size={14} /> {t('Verify')}
      </button>
      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin') || t('Create a new machine from this backup')} onclick={() => openRestore(b)}>
        <ArchiveRestore size={14} /> {t('Restore…')}
      </button>
      <ConfirmButton
        label={t('Delete')}
        confirm={t('Delete backup?')}
        icon={Trash}
        disabled={!session.can('admin')}
        title={session.why('admin')}
        failure={t('Could not delete the backup')}
        run={async () => {
          await api.deleteBackup(b.id, host)
          toasts.push({ kind: 'info', title: t('Backup deleted') })
          onchange()
        }}
      />
    </div>
  {/each}
</div>

<Dialog open={restoring !== null} title={t('Restore backup')} subtitle={restoring ? `${restoring.domain}, ${when(restoring.created)}` : ''} onclose={() => (restoring = null)}>
  <form id="restore-form" class="form" onsubmit={restore}>
    <p class="text">{t('The backup becomes a')} <b>{t('new machine')}</b> {t('with its own name, identity and network addresses. The original — if it still exists — is not touched.')}</p>
    <label class="ui-field">
      <span class="label">{t('Name of the new machine')}</span>
      <input class="ui-input" bind:value={name} required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <label class="ui-field">
      <span class="label">{t('Put its disks in')}</span>
      <select class="ui-input" bind:value={pool}>
        {#each pools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
      </select>
      {#if pools.length === 0 && !error}<span class="hint warn">{t('No started pool to restore into. Create or start one under Storage.')}</span>{/if}
      <span class="hint" class:warn={!!target && needed > target.availableBytes}>{t('Needs about {needed} (more if the disks are fuller than the compressed backup).', { needed: formatBytes(needed) })}</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (restoring = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="restore-form" disabled={restoreBusy || !pool || pools.length === 0}><ArchiveRestore size={14} /> {restoreBusy ? t('Starting…') : t('Restore')}</button>
  {/snippet}
</Dialog>

<Dialog open={refused !== null} title={t('Backup may be damaged')} subtitle={refused ? `${refused.b.domain}, ${when(refused.b.created)}` : ''} onclose={() => (refused = null)}>
  <div class="ui-error-box">{refused?.message}</div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (refused = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={restoreBusy} onclick={() => refused && startRestore(refused.b, refused.name, refused.pool, true)}>
      <ArchiveRestore size={14} /> {restoreBusy ? t('Starting…') : t('Restore anyway')}
    </button>
  {/snippet}
</Dialog>

<style>
  .list {
    display: grid;
  }
  .backup {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 2px;
    border-top: 1px solid var(--border);
  }
  .backup:first-child {
    border-top: 0;
  }
  .kind {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--accent);
  }
  .info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .when {
    font-weight: 500;
    font-size: 13.5px;
  }
  .tag {
    margin-left: 6px;
    padding: 0 7px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-3);
    font-size: 11px;
    font-weight: 400;
  }
  .tag.ok {
    color: var(--ok);
  }
  .tag.bad {
    color: var(--danger);
  }
  .meta {
    color: var(--text-3);
    font-size: 12px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .text {
    margin: 0;
    color: var(--text-2);
    font-size: 13.5px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .warn {
    color: var(--warn);
  }
</style>
