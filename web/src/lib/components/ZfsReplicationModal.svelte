<script lang="ts">
  import { t } from '../i18n.svelte'
  import { ent, type ZFSReplicationStatus, type ZFSReplicationSpec } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { api, errorText, type SSHTrust } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatBytes } from '../format'
  import Dialog from './Dialog.svelte'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Trash from '@lucide/svelte/icons/trash'
  import Plus from '@lucide/svelte/icons/plus'
  import ArrowRight from '@lucide/svelte/icons/arrow-right'
  import Play from '@lucide/svelte/icons/play'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'

  let { open = false, onclose }: { open: boolean; onclose: () => void } = $props()

  let replications = $state<ZFSReplicationStatus[]>([])
  let loading = $state(false)
  let syncingId = $state<string | null>(null)
  let showAdd = $state(false)

  let sourcePool = $state('')
  let sourceVolume = $state('')
  let targetHost = $state('')
  let targetPool = $state('')
  let intervalMinutes = $state(15)
  let keep = $state(3)
  let creating = $state(false)
  let formError = $state('')

  async function load() {
    loading = true
    try {
      replications = await ent.zfsReplications()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to load ZFS replications'), detail: errorText(e) })
    } finally {
      loading = false
    }
  }

  $effect(() => {
    if (open) {
      void load()
    }
  })

  // While a pass runs (here or on its schedule), follow it.
  let anySyncing = $derived(replications.some((r) => r.state === 'syncing'))
  $effect(() => {
    if (!open || !anySyncing) return
    const iv = setInterval(() => void load(), 2000)
    return () => clearInterval(iv)
  })

  async function syncNow(id: string) {
    syncingId = id
    try {
      await ent.syncZFSReplication(id)
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Replication failed'), detail: errorText(e) })
    } finally {
      syncingId = null
    }
  }

  async function toggle(rep: ZFSReplicationStatus) {
    try {
      await ent.updateZFSReplication({ ...rep.spec, enabled: !rep.spec.enabled })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not change the schedule'), detail: errorText(e) })
    }
  }

  async function remove(id: string) {
    if (!confirm(t('Delete this replication schedule?'))) return
    try {
      await ent.deleteZFSReplication(id)
      toasts.push({ kind: 'info', title: t('Replication schedule removed') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Failed to delete schedule'), detail: errorText(e) })
    }
  }

  // The SSH trust the replication needs, set up when the schedule is made: by VirtoMir itself,
  // over the cluster for a member, with root's password there (asked once) for any other host.
  let trust = $state<SSHTrust | null>(null)
  let trustTarget = $state('')
  let rootPassword = $state('')
  let sending = $state(false)
  // Cluster members to pick as the target (any other host can still be typed).
  let memberHosts = $derived(
    cluster.members.filter((m) => !m.self).map((m) => {
      try {
        return new URL(m.address).hostname
      } catch {
        return m.name
      }
    }),
  )

  async function sendPassword(e: SubmitEvent) {
    e.preventDefault()
    sending = true
    try {
      trust = await api.sshTrust(trustTarget, rootPassword)
      if (trust.verified) {
        toasts.push({ kind: 'info', title: t('{host} accepts this host\'s SSH key; the first copy runs on schedule.', { host: trustTarget }) })
        trust = null
      }
    } catch (err) {
      toasts.push({ kind: 'error', title: t('The key could not be added'), detail: errorText(err) })
    } finally {
      rootPassword = ''
      sending = false
    }
  }

  async function createSchedule(e: SubmitEvent) {
    e.preventDefault()
    formError = ''
    creating = true
    try {
      trustTarget = targetHost.trim()
      trust = await api.sshTrust(trustTarget).catch((err) => ({ authorized: false, verified: false, publicKey: '', reason: errorText(err) }))
      await ent.createZFSReplication({
        sourcePool: sourcePool.trim(),
        sourceVolume: sourceVolume.trim(),
        targetHost: targetHost.trim(),
        targetPool: targetPool.trim(),
        intervalMinutes: Number(intervalMinutes),
        keep: Number(keep),
        enabled: true,
      })
      toasts.push({
        kind: trust.verified ? 'info' : 'warn',
        title: t('ZFS replication schedule created'),
        detail: trust.verified
          ? t('{host} accepts this host\'s SSH key; the first copy runs on schedule.', { host: targetHost.trim() })
          : trust.needsPassword
            ? t('Enter root\'s password on the target once, above the list.')
            : t('The target does not accept this host\'s SSH key yet; see the note above the list.'),
      })
      if (trust.verified) trust = null
      sourceVolume = ''
      showAdd = false
      await load()
    } catch (err) {
      formError = errorText(err)
    } finally {
      creating = false
    }
  }
</script>

<Dialog
  {open}
  title={t('ZFS Asynchronous Replication (zfs send/recv)')}
  subtitle={t('Automated differential block-level storage replication between nodes')}
  {onclose}
>
  <div class="rep-container">
    <div class="rep-header">
      <div class="info-banner">
        <ShieldCheck size={18} class="banner-icon" />
        <span>
          {t('On its schedule VirtoMir snapshots the dataset or zvol and sends only what changed since the last snapshot both hosts have (zfs send -i | ssh … zfs receive). VirtoMir sets up the SSH key itself: over the cluster for a member, with root\'s password there (asked once, never kept) for any other host. The target pool must exist there.')}
        </span>
      </div>
      <div class="header-actions">
        <button class="ui-btn sm ghost" onclick={load} disabled={loading} title={t('Refresh status')}>
          <RefreshCw size={14} class={loading ? 'spin' : ''} /> {t('Refresh')}
        </button>
        {#if !showAdd}
          <button class="ui-btn sm primary" disabled={!session.can('admin')} onclick={() => (showAdd = true)}>
            <Plus size={14} /> {t('New Schedule')}
          </button>
        {/if}
      </div>
    </div>

    {#if trust && !trust.verified}
      <div class="trust ui-panel">
        <p>{trust.reason}</p>
        {#if trust.needsPassword}
          <form class="trust-actions" onsubmit={sendPassword}>
            <input class="ui-input" type="password" autocomplete="off" bind:value={rootPassword} placeholder={t('root password on {host}', { host: trustTarget })} aria-label={t('root password on {host}', { host: trustTarget })} required />
            <button class="ui-btn sm primary" disabled={sending || !rootPassword}>{sending ? t('Adding the key…') : t('Add the key')}</button>
            <button type="button" class="ui-btn sm ghost" onclick={() => (trust = null)}>{t('Later')}</button>
          </form>
        {:else}
          <div class="trust-actions">
            <button class="ui-btn sm ghost" onclick={() => (trust = null)}>{t('Done')}</button>
          </div>
        {/if}
      </div>
    {/if}

    {#if showAdd}
      <form class="add-box ui-panel" onsubmit={createSchedule}>
        <h3>{t('Configure ZFS Differential Replication')}</h3>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Source Pool')}</span>
            <input class="ui-input mono" bind:value={sourcePool} placeholder="tank/vms" required />
          </label>
          <label class="ui-field">
            <span class="label">{t('Source Dataset / Zvol')}</span>
            <input class="ui-input mono" bind:value={sourceVolume} placeholder="vm-101-disk-0" required />
          </label>
        </div>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Target host (SSH)')}</span>
            <input class="ui-input mono" bind:value={targetHost} placeholder={memberHosts[0] ?? '192.168.1.50'} list="zfs-targets" required />
            <datalist id="zfs-targets">{#each memberHosts as h (h)}<option value={h}></option>{/each}</datalist>
          </label>
          <label class="ui-field">
            <span class="label">{t('Target ZFS Pool')}</span>
            <input class="ui-input mono" bind:value={targetPool} placeholder="backup-pool" required />
          </label>
        </div>
        <div class="grid2">
          <label class="ui-field">
            <span class="label">{t('Sync Interval (Minutes)')}</span>
            <input class="ui-input" type="number" min="1" max="10080" bind:value={intervalMinutes} required />
          </label>
          <label class="ui-field">
            <span class="label">{t('Snapshots kept on each side')}</span>
            <input class="ui-input" type="number" min="1" max="1000" bind:value={keep} required />
          </label>
        </div>

        {#if formError}
          <div class="ui-error-box">{formError}</div>
        {/if}

        <div class="form-actions">
          <button type="button" class="ui-btn ghost" onclick={() => (showAdd = false)}>{t('Cancel')}</button>
          <button type="submit" class="ui-btn primary" disabled={creating}>
            {creating ? t('Creating…') : t('Save Schedule')}
          </button>
        </div>
      </form>
    {/if}

    {#if loading && replications.length === 0}
      <p class="muted">{t('Loading replication tasks…')}</p>
    {:else if replications.length === 0}
      <div class="empty-state">
        <p class="muted">{t('No active ZFS replication schedules.')}</p>
      </div>
    {:else}
      <div class="task-list">
        {#each replications as rep (rep.spec.id)}
          <div class="rep-card">
            <div class="rep-main">
              <div class="rep-flow">
                <span class="mono bold">{rep.spec.sourcePool}/{rep.spec.sourceVolume}</span>
                <ArrowRight size={14} class="muted" />
                <span class="mono bold">{rep.spec.targetHost}:{rep.spec.targetPool}</span>
              </div>
              <div class="rep-meta">
                <span class="badge" class:green={rep.state === 'idle' && rep.lastSync} class:sync={rep.state === 'syncing'} class:red={rep.state === 'failed'}>
                  {rep.state === 'syncing' ? t('syncing') : rep.state === 'failed' ? t('failed') : rep.lastSync ? t('in sync') : t('not synced yet')}
                </span>
                <span class="muted">{rep.spec.enabled ? t('every {n} min', { n: rep.spec.intervalMinutes }) : t('paused')}</span>
                {#if rep.lastSync}
                  <span class="muted">{t('Last synced: {time}', { time: new Date(rep.lastSync).toLocaleString() })}</span>
                  <span class="muted mono">{rep.lastSnapshot} · {formatBytes(rep.bytesTransferred ?? 0)}</span>
                {/if}
              </div>
              {#if rep.error}<div class="rep-error">{rep.error}</div>{/if}
            </div>
            <div class="rep-actions" inert={!session.can('admin')}>
              <button class="ui-btn sm ghost" onclick={() => toggle(rep)}>
                {rep.spec.enabled ? t('Pause') : t('Resume')}
              </button>
              <button
                class="ui-btn sm ghost"
                disabled={syncingId === rep.spec.id || rep.state === 'syncing'}
                onclick={() => syncNow(rep.spec.id!)}
                title={t('Trigger immediate differential sync')}
              >
                <Play size={14} /> {t('Sync Now')}
              </button>
              <button
                class="ui-btn sm ghost red"
                onclick={() => remove(rep.spec.id!)}
                title={t('Delete schedule')}
              >
                <Trash size={14} />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Close')}</button>
  {/snippet}
</Dialog>

<style>
  .trust {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    margin-bottom: 12px;
    border-left: 3px solid var(--warn);
  }
  .trust p {
    margin: 0;
  }
  .trust-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .rep-container {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: min(620px, 100%);
  }
  .rep-header {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .info-banner {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 13px;
    color: var(--text-2);
    line-height: 1.4;
  }
  :global(.banner-icon) {
    color: var(--accent);
    flex-shrink: 0;
    margin-top: 2px;
  }
  .header-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .add-box {
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .add-box h3 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  @media (max-width: 560px) {
    .grid2 {
      grid-template-columns: 1fr;
    }
  }
  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
  .task-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rep-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 10px;
    padding: 12px 14px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .rep-main {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .rep-flow {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .rep-error {
    font-size: 12px;
    color: var(--danger);
    overflow-wrap: anywhere;
  }
  .rep-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    font-size: 12px;
  }
  .badge {
    padding: 2px 6px;
    border-radius: var(--radius-sm);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    background: var(--surface-hover);
    color: var(--text-2);
  }
  .badge.green {
    background: color-mix(in srgb, var(--ok) 15%, transparent);
    color: var(--ok);
  }
  .badge.red {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .badge.sync {
    background: color-mix(in srgb, var(--info) 15%, transparent);
    color: var(--info);
    animation: pulse 1.5s infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.5; }
  }
  .rep-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .red {
    color: var(--danger);
  }
  .empty-state {
    padding: 24px;
    text-align: center;
  }
</style>
