<script lang="ts">
  import Archive from '@lucide/svelte/icons/archive'
  import FolderCog from '@lucide/svelte/icons/folder-cog'
  import { api, errorText, type BackupInfo } from '../api'
  import { formatBytes } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { live } from '../live.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import BackupList from '../components/BackupList.svelte'

  let backups = $state<BackupInfo[] | null>(null)
  let dir = $state('')
  let newDir = $state('')
  let error = $state('')

  async function load() {
    try {
      const r = await api.backups()
      backups = r.backups
      dir = r.dir
      if (!newDir) newDir = r.dir
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  let groups = $derived.by(() => {
    const m = new Map<string, BackupInfo[]>()
    for (const b of backups ?? []) m.set(b.domainUuid, [...(m.get(b.domainUuid) ?? []), b])
    return [...m.entries()].map(([uuid, list]) => ({ uuid, name: list[0].domain, list, size: list.reduce((n, b) => n + b.sizeBytes, 0) }))
  })
  let total = $derived((backups ?? []).reduce((n, b) => n + b.sizeBytes, 0))
  let exists = (uuid: string) => live.snapshot?.domains.some((d) => d.uuid === uuid)

  async function saveDir() {
    try {
      const r = await api.saveBackupSettings(newDir.trim())
      dir = r.backupDir
      toasts.push({ kind: 'info', title: 'Backup location saved', detail: 'New backups go there; existing ones stay where they are.' })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not saved', detail: errorText(e) })
    }
  }
</script>

<svelte:head><title>Backups · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Backups</h1>
    <p class="lead">Full copies of machines, safe from the disks they came from. Turn on automatic backups on each machine's Backups tab.</p>
  </div>
</header>

<section class="ui-panel card loc">
  <FolderCog size={18} />
  <div class="loc-text">
    <span class="label">Backup location</span>
    {#if session.can('admin')}
      <div class="loc-edit">
        <input class="ui-input mono" bind:value={newDir} aria-label="Backup location" />
        <button class="ui-btn" disabled={!newDir.trim() || newDir.trim() === dir} onclick={saveDir}>Save</button>
      </div>
      <span class="hint">Ideally another disk or an NFS mount — a backup on the same disk does not survive that disk.</span>
    {:else}
      <span class="mono">{dir}</span>
    {/if}
  </div>
  <div class="total">
    <span class="num">{formatBytes(total)}</span>
    <span class="hint">{backups?.length ?? 0} backups</span>
  </div>
</section>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if backups === null}
  <p class="muted">Loading…</p>
{:else if backups.length === 0}
  <div class="empty ui-panel">
    <Archive size={28} strokeWidth={1.5} />
    <strong>No backups yet</strong>
    <span>Open a machine, go to Backups and press "Back up now" — or turn on automatic backups.</span>
  </div>
{:else}
  {#each groups as g (g.uuid)}
    <section class="ui-panel card">
      <header class="g-head">
        {#if exists(g.uuid)}
          <a href={machineHref(g.uuid, 'backups')} onclick={router.link}>{g.name}</a>
        {:else}
          <span>{g.name} <span class="gone">(machine deleted)</span></span>
        {/if}
        <span class="hint">{g.list.length} · {formatBytes(g.size)}</span>
      </header>
      <BackupList backups={g.list} onchange={load} />
    </section>
  {/each}
{/if}

<style>
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .card {
    padding: 14px 16px;
    margin-bottom: 14px;
  }
  .loc {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  .loc :global(svg) {
    margin-top: 2px;
    color: var(--accent);
  }
  .loc-text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .loc-edit {
    display: flex;
    gap: 8px;
    max-width: 560px;
  }
  .label {
    font-size: 13px;
    font-weight: 600;
  }
  .total {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
  }
  .num {
    font-size: 20px;
    font-weight: 600;
  }
  .hint,
  .muted {
    color: var(--text-3);
    font-size: 12px;
  }
  .g-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 4px;
    font-weight: 600;
  }
  .g-head a {
    color: var(--text);
    text-decoration: none;
  }
  .g-head a:hover {
    color: var(--accent);
  }
  .gone {
    color: var(--text-3);
    font-weight: 400;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 56px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
</style>
