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

  import { cluster } from '../cluster.svelte'

  /** A backup and, in a cluster, the host it is stored on. */
  type HostBackup = BackupInfo & { hostId?: string; hostName?: string }
  let backups = $state<HostBackup[] | null>(null)
  let dir = $state('')
  let newDir = $state('')
  let error = $state('')

  async function load() {
    try {
      const r = await api.backups()
      dir = r.dir
      if (!cluster.clustered) {
        backups = r.backups
      } else {
        // Each host keeps its own backups; list them all, grouped by host and machine.
        const parts = await Promise.all(
          cluster.members
            .filter((m) => m.reachable)
            .map(async (m) => (await api.backups(undefined, m.id).catch(() => ({ backups: [] }))).backups.map((b) => ({ ...b, hostId: m.id, hostName: m.name }))),
        )
        backups = parts.flat()
      }
      if (!newDir) newDir = r.dir
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void cluster.clustered
    void load()
  })

  let groups = $derived.by(() => {
    const m = new Map<string, HostBackup[]>()
    for (const b of backups ?? []) {
      const key = `${b.hostId ?? ''}/${b.domainUuid}`
      m.set(key, [...(m.get(key) ?? []), b])
    }
    return [...m.entries()].map(([key, list]) => ({
      key,
      uuid: list[0].domainUuid,
      name: list[0].domain,
      hostId: list[0].hostId,
      hostName: list[0].hostName,
      list,
      size: list.reduce((n, b) => n + b.sizeBytes, 0),
    }))
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
    <span class="label">Backup location{cluster.clustered && cluster.current ? ` on ${cluster.current.name}` : ""}</span>
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
  {#each groups as g (g.key)}
    {@const member = cluster.members.find((m) => m.id === g.hostId)}
    <section class="ui-panel card">
      <header class="g-head">
        {#if member}
          <span>
            <span class="host-chip">{member.name}</span>
            <a
              href={router.hostHref(member.self ? null : member.id, `/machines/${g.uuid}#backups`)}
              onclick={(e) => {
                if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
                e.preventDefault()
                router.go(member.self ? null : member.id, `/machines/${g.uuid}#backups`)
              }}>{g.name}</a
            >
          </span>
        {:else if exists(g.uuid)}
          <a href={machineHref(g.uuid, 'backups')} onclick={router.link}>{g.name}</a>
        {:else}
          <span>{g.name} <span class="gone">(machine deleted)</span></span>
        {/if}
        <span class="hint">{g.list.length} · {formatBytes(g.size)}</span>
      </header>
      <BackupList backups={g.list} onchange={load} host={g.hostId} />
    </section>
  {/each}
{/if}

<style>
  .host-chip {
    margin-right: 6px;
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11px;
    font-weight: 600;
  }
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
