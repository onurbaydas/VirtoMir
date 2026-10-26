<script lang="ts">
  import { t } from '../i18n.svelte'
  import Archive from '@lucide/svelte/icons/archive'
  import FolderCog from '@lucide/svelte/icons/folder-cog'
  import { api, errorText, type BackupInfo, type ScheduleInfo } from '../api'
  import { formatBytes } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { live } from '../live.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import BackupList from '../components/BackupList.svelte'
  import OffsitePanel from '../components/OffsitePanel.svelte'

  import { cluster } from '../cluster.svelte'

  /** A backup and, in a cluster, the host it is stored on. */
  type HostBackup = BackupInfo & { hostId?: string; hostName?: string }
  let backups = $state<HostBackup[] | null>(null)
  let dir = $state('')
  let freeBytes = $state<number | undefined>()
  let totalBytes = $state<number | undefined>()
  let oldDirs = $state<string[]>([])
  let onSystemDisk = $state(false)
  let newDir = $state('')
  let dirEverywhere = $state(false)
  let error = $state('')

  async function load() {
    try {
      const r = await api.backups()
      dir = r.dir
      freeBytes = r.freeBytes
      totalBytes = r.totalBytes
      oldDirs = r.oldDirs ?? []
      onSystemDisk = !!r.onSystemDisk
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
  let lowSpace = $derived(freeBytes !== undefined && !!totalBytes && freeBytes < totalBytes * 0.1)
  let total = $derived((backups ?? []).reduce((n, b) => n + b.sizeBytes, 0))
  let offsiteReady = $state(false)
  // Backups on the host shown here (the off-site settings are per host).
  let localIds = $derived(new Set((backups ?? []).filter((b) => !b.hostId || cluster.members.find((m) => m.id === b.hostId)?.self).map((b) => b.id)))
  let exists = (uuid: string) => live.snapshot?.domains.some((d) => d.uuid === uuid)

  // Protection at a glance: machines of this host without an automatic backup, and
  // automatic backups whose last run failed (so a failure never stays silent).
  let schedules = $state<ScheduleInfo[] | null>(null)
  const loadSchedules = () =>
    api
      .schedules()
      .then((s) => (schedules = s))
      .catch(() => (schedules = null))
  $effect(() => {
    void loadSchedules()
  })
  let backupSchedules = $derived((schedules ?? []).filter((s) => s.kind === 'backup'))
  let unprotected = $derived(
    schedules === null
      ? []
      : (live.snapshot?.domains ?? []).filter((d) => !d.template && !backupSchedules.some((s) => s.enabled && s.domain === d.uuid)),
  )
  let failing = $derived(backupSchedules.filter((s) => s.enabled && s.lastError))
  const nameOf = (uuid: string) => live.snapshot?.domains.find((d) => d.uuid === uuid)?.name ?? uuid
  let protecting = $state<Record<string, boolean>>({})
  async function protect(uuid: string) {
    if (protecting[uuid]) return
    protecting[uuid] = true
    try {
      const existing = backupSchedules.find((s) => s.domain === uuid)
      await api.saveSchedule(
        existing
          ? { ...existing, enabled: true }
          : { id: '', kind: 'backup', domain: uuid, enabled: true, every: 'daily', at: '02:00', weekday: 0, hours: 6, keep: 7, fullEvery: 7 },
      )
      toasts.push({ kind: 'info', title: t('Automatic backups on for {name}', { name: nameOf(uuid) }), detail: t('Every night at 02:00, the last 7 kept. Change it on the machine\'s Backups tab.') })
      await loadSchedules()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Automatic backups not turned on'), detail: errorText(e) })
    } finally {
      protecting[uuid] = false
    }
  }

  async function saveDir() {
    try {
      const target = newDir.trim()
      if (cluster.clustered && dirEverywhere) {
        const res = await cluster.everywhere((h) => api.saveBackupSettings(target, h))
        const failed = res.filter((r) => r.error)
        if (failed.length === res.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
        dir = target
        toasts.push({
          kind: failed.length ? 'warn' : 'info',
          title: failed.length ? t('Saved on {v} of {length} hosts', { v: res.length - failed.length, length: res.length }) : t('Backup location saved on every host'),
          detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') || t('New backups go there; existing backups stay listed.'),
        })
      } else {
        const r = await api.saveBackupSettings(target)
        dir = r.backupDir
        toasts.push({ kind: 'info', title: t('Backup location saved'), detail: t('New backups go there; existing backups stay listed.') })
      }
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    }
  }
</script>

<svelte:head><title>{t('Backups · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Backups')}</h1>
    <p class="lead">{t('Full copies of machines, safe from the disks they came from. Turn on automatic backups on each machine\'s Backups tab.')}</p>
  </div>
</header>

<section class="ui-panel card loc">
  <FolderCog size={18} />
  <div class="loc-text">
    <span class="label">{t('Backup location{v}', { v: cluster.clustered && cluster.current ? ` on ${cluster.current.name}` : "" })}</span>
    {#if session.can('admin')}
      <div class="loc-edit">
        <input class="ui-input mono" bind:value={newDir} aria-label={t('Backup location')} />
        <button class="ui-btn" disabled={!newDir.trim() || (newDir.trim() === dir && !(cluster.clustered && dirEverywhere))} onclick={saveDir}>{t('Save')}</button>
      </div>
      {#if cluster.clustered}
        <label class="everywhere"><input type="checkbox" bind:checked={dirEverywhere} /> {t('Use this location on every host (e.g. each host\'s own folder on a shared NFS mount)')}</label>
      {/if}
      <span class="hint">{t('Ideally another disk or an NFS mount — a backup on the same disk does not survive that disk.')}</span>
    {:else}
      <span class="mono">{dir}</span>
    {/if}
    {#if freeBytes !== undefined && totalBytes}
      <span class="hint" class:low={lowSpace}>{t('{free} free of {total}', { free: formatBytes(freeBytes), total: formatBytes(totalBytes) })}</span>
    {/if}
    {#if oldDirs.length}
      <span class="hint">{t('Backups in earlier locations stay listed: {dirs}', { dirs: oldDirs.join(', ') })}</span>
    {/if}
  </div>
  <div class="total">
    <span class="num">{formatBytes(total)}</span>
    <span class="hint">{backups?.length ?? 0} backups</span>
  </div>
</section>

{#if onSystemDisk}
  <p class="ui-panel card hint">{t('Backups are on the system disk: they do not survive that disk failing. Another disk or an NFS mount is safer.')}</p>
{/if}

{#if lowSpace}
  <section class="ui-panel card failing" role="alert">
    <h2>{t('The backup location is almost full')}</h2>
    <span class="hint">{t('Less than 10% is free. New backups may fail; free up space or choose another location.')}</span>
  </section>
{/if}

{#if failing.length}
  <section class="ui-panel card failing" role="alert">
    <h2>{t('Automatic backups that failed')}</h2>
    {#each failing as s (s.id)}
      <div class="row">
        <a href={machineHref(s.domain, 'backups')} onclick={router.link}>{nameOf(s.domain)}</a>
        <span class="hint">{s.lastError}</span>
      </div>
    {/each}
  </section>
{/if}

{#if unprotected.length}
  <section class="ui-panel card">
    <header class="g-head">
      <span>{t('Machines without automatic backups')}</span>
      <span class="hint">{unprotected.length}</span>
    </header>
    {#each unprotected as d (d.uuid)}
      <div class="row">
        <a href={machineHref(d.uuid, 'backups')} onclick={router.link}>{d.name}</a>
        <button class="ui-btn sm" disabled={!session.can('admin') || protecting[d.uuid]} title={session.why('admin')} onclick={() => protect(d.uuid)}>
          {t('Back up every night')}
        </button>
      </div>
    {/each}
  </section>
{/if}

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if backups === null}
  <p class="muted">{t('Loading…')}</p>
{:else if backups.length === 0}
  <div class="empty ui-panel">
    <Archive size={28} strokeWidth={1.5} />
    <strong>{t('No backups yet')}</strong>
    <span>{t('Open a machine, go to Backups and press "Back up now" — or turn on automatic backups.')}</span>
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
          <span>{g.name} <span class="gone">{t('(machine deleted)')}</span></span>
        {/if}
        <span class="hint">{g.list.length} · {formatBytes(g.size)}</span>
      </header>
      <BackupList backups={g.list} onchange={load} host={g.hostId} offsite={offsiteReady && (!member || member.self)} />
    </section>
  {/each}
{/if}

<OffsitePanel {localIds} onchange={load} onready={(r) => (offsiteReady = r)} />

<style>
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 2px;
    border-top: 1px solid var(--border);
  }
  .row a {
    color: var(--text);
    font-weight: 600;
  }
  .failing {
    border-color: var(--danger);
  }
  .failing h2 {
    margin: 0 0 6px;
    color: var(--danger);
    font-size: 15px;
  }
  .low {
    color: var(--danger);
  }
  .everywhere {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--text-2);
  }
  .host-chip {
    margin-right: 6px;
    padding: 1px 8px;
    border-radius: var(--radius-sm);
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
