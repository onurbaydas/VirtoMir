<script lang="ts">
  // Backups of one machine (or all, grouped on the Backups page) with restore and delete.
  import ArchiveRestore from '@lucide/svelte/icons/archive-restore'
  import Trash from '@lucide/svelte/icons/trash'
  import Zap from '@lucide/svelte/icons/zap'
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
      track(job, { success: `${b.domain}: copied off-site`, host, onSuccess: onchange })
    } catch (err) {
      toasts.push({ kind: 'error', title: 'Could not copy off-site', detail: errorText(err) })
    }
  }

  const when = (s: string) => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
  const took = (s: number) => (s < 60 ? `${Math.round(s)} s` : `${Math.round(s / 60)} min`)

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
      pools = (await (host ? api.poolsOn(host) : api.pools())).filter((p) => p.active && p.type === 'dir')
      if (!pools.some((p) => p.name === pool) && pools[0]) pool = pools[0].name
    } catch (e) {
      error = errorText(e)
    }
  }
  async function restore(e: SubmitEvent) {
    e.preventDefault()
    if (!restoring) return
    try {
      const job = await api.restoreBackup(restoring.id, name, pool, host)
      restoring = null
      const m = host ? cluster.members.find((x) => x.id === host) : undefined
      track(job, {
        success: `${name} restored`,
        host,
        onSuccess: (j) => j.result && (m ? router.go(m.self ? null : m.id, `/machines/${j.result}`) : router.navigate(machineHref(j.result))),
      })
    } catch (err) {
      error = errorText(err)
    }
  }
  let needed = $derived(restoring ? restoring.disks.reduce((n, d) => n + d.sizeBytes, 0) : 0)
  let target = $derived(pools.find((p) => p.name === pool))
</script>

<div class="list">
  {#each backups as b (b.id)}
    <div class="backup">
      <span class="kind" title={b.live ? 'Taken while the machine was running (no downtime)' : 'Taken while the machine was off'}>
        {#if b.live}<Zap size={15} />{:else}<Power size={15} />{/if}
      </span>
      <div class="info">
        <span class="when">{when(b.created)} {#if b.automatic}<span class="tag">automatic</span>{/if}</span>
        <span class="meta">{formatBytes(b.sizeBytes)} · {b.disks.map((d) => d.target).join(', ')} · {b.live ? 'live' : 'offline'} · took {took(b.durationSec)}</span>
      </div>
      {#if offsite}
        <button class="ui-btn sm ghost" disabled={!session.can('admin')} title={session.why('admin') || 'Copy this backup to the off-site repository'} onclick={() => copyOffsite(b)}>
          <CloudUpload size={14} /> Copy off-site
        </button>
      {/if}
      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin') || 'Create a new machine from this backup'} onclick={() => openRestore(b)}>
        <ArchiveRestore size={14} /> Restore…
      </button>
      <ConfirmButton
        label="Delete"
        confirm="Delete backup?"
        icon={Trash}
        disabled={!session.can('admin')}
        title={session.why('admin')}
        failure="Could not delete the backup"
        run={async () => {
          await api.deleteBackup(b.id, host)
          toasts.push({ kind: 'info', title: 'Backup deleted' })
          onchange()
        }}
      />
    </div>
  {/each}
</div>

<Dialog open={restoring !== null} title="Restore backup" subtitle={restoring ? `${restoring.domain}, ${when(restoring.created)}` : ''} onclose={() => (restoring = null)}>
  <form id="restore-form" class="form" onsubmit={restore}>
    <p class="text">The backup becomes a <b>new machine</b> with its own name, identity and network addresses. The original — if it still exists — is not touched.</p>
    <label class="ui-field">
      <span class="label">Name of the new machine</span>
      <input class="ui-input" bind:value={name} required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <label class="ui-field">
      <span class="label">Put its disks in</span>
      <select class="ui-input" bind:value={pool}>
        {#each pools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
      </select>
      <span class="hint" class:warn={!!target && needed > target.availableBytes}>Needs about {formatBytes(needed)} (more if the disks are fuller than the compressed backup).</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (restoring = null)}>Cancel</button>
    <button class="ui-btn primary" form="restore-form"><ArchiveRestore size={14} /> Restore</button>
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
