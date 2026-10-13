<script lang="ts">
  // The machine page's Backups tab: back up now, the machine's backups, its schedules.
  import Archive from '@lucide/svelte/icons/archive'
  import { api, errorText, type BackupInfo } from '../api'
  import { track } from '../jobs'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import BackupList from './BackupList.svelte'
  import SchedulesCard from './SchedulesCard.svelte'

  let { uuid, name, active }: { uuid: string; name: string; active: boolean } = $props()

  let backups = $state<BackupInfo[] | null>(null)
  let dir = $state('')
  let error = $state('')

  async function load() {
    try {
      const r = await api.backups(uuid)
      backups = r.backups
      dir = r.dir
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  async function backupNow() {
    try {
      const job = await api.backupNow(uuid)
      track(job, { success: `${name} backed up`, onSuccess: () => void load() })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Backup not started', detail: errorText(e) })
    }
  }
</script>

<SchedulesCard {uuid} />

<section class="ui-panel card">
  <header class="head">
    <div>
      <h2><Archive size={16} /> Backups</h2>
      <p class="muted">
        Stored in <span class="mono">{dir || '…'}</span>.
        {active ? 'Backing up a running machine does not stop it.' : 'The machine is off: its disks are copied as they are.'}
      </p>
    </div>
    <button class="ui-btn primary" disabled={!session.can('operator')} title={session.why('operator')} onclick={backupNow}>
      <Archive size={15} /> Back up now
    </button>
  </header>
  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if backups === null}
    <p class="muted">Loading…</p>
  {:else if backups.length === 0}
    <p class="empty">No backups yet. Back up now, or turn on automatic backups above.</p>
  {:else}
    <BackupList {backups} onchange={load} />
  {/if}
</section>

<style>
  .card {
    padding: 16px 18px;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 10px;
    flex-wrap: wrap;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 4px;
    font-size: 14.5px;
    font-weight: 600;
  }
  .muted {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .empty {
    margin: 8px 0 4px;
    color: var(--text-3);
    font-size: 13px;
  }
</style>
