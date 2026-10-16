<script lang="ts">
  import History from '@lucide/svelte/icons/clock-arrow-up'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleX from '@lucide/svelte/icons/circle-x'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import Download from '@lucide/svelte/icons/download'
  import { api, errorText, type ActivityEntry } from '../api'
  import { describe } from '../activity'
  import { live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { cluster } from '../cluster.svelte'

  /** An entry and, in a cluster, the host it was recorded on. */
  type Entry = ActivityEntry & { hostId?: string; hostName?: string }

  let entries = $state<Entry[] | null>(null)
  /** Machine names (and their host) of other members, for readable sentences. */
  let remoteNames = $state(new Map<string, string>())
  let machineHost = $state(new Map<string, string>())
  let error = $state('')
  let failedOnly = $state(false)
  let user = $state('')

  async function load() {
    try {
      const q = { failed: failedOnly, user: user || undefined }
      if (!cluster.clustered) {
        entries = await api.activity(q)
      } else {
        // Every host logs what was done on it; show them as one timeline.
        const parts = await Promise.all(
          cluster.members
            .filter((m) => m.reachable)
            .map(async (m) => {
              const [list, sum] = await Promise.all([api.activity(q, m.id), api.summary(m.id).catch(() => null)])
              return { m, list, doms: sum?.domains ?? [] }
            }),
        )
        const names = new Map<string, string>()
        const hosts = new Map<string, string>()
        for (const { m, doms } of parts) for (const d of doms) (names.set(d.uuid, d.name), hosts.set(d.uuid, m.id))
        remoteNames = names
        machineHost = hosts
        entries = parts
          .flatMap(({ m, list }) => list.map((e) => ({ ...e, hostId: m.id, hostName: m.name })))
          .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
      }
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void failedOnly
    void user
    void cluster.clustered
    void load()
  })

  let names = $derived(cluster.clustered ? remoteNames : new Map((live.snapshot?.domains ?? []).map((d) => [d.uuid, d.name])))
  // A machine link opens the page on the host the machine is on now.
  function machineLink(uuid: string) {
    const id = machineHost.get(uuid)
    const m = cluster.members.find((x) => x.id === id)
    return { href: m ? router.hostHref(m.self ? null : m.id, `/machines/${uuid}`) : machineHref(uuid), host: m ? (m.self ? null : m.id) : undefined }
  }
  function open(e: MouseEvent, uuid: string) {
    const l = machineLink(uuid)
    if (l.host === undefined) return router.link(e)
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    router.go(l.host, `/machines/${uuid}`)
  }
  let users = $derived([...new Set((entries ?? []).map((e) => e.user))].sort())

  // Audit export: what is listed (all hosts in a cluster), as CSV with a sentence per row.
  function exportCSV() {
    const q = (s: string) => (/[",\n]/.test(s) ? '"' + s.replaceAll('"', '""') + '"' : s)
    const rows = [['time', 'host', 'user', 'token', 'what', 'method', 'path', 'status', 'error', 'remote']]
    for (const e of entries ?? []) {
      const d = describe(e, names)
      rows.push([e.time, e.hostName ?? '', e.user, e.token ?? '', d.before + (d.machineName ?? '') + d.after, e.method, e.path, String(e.status), e.error ?? '', e.remote ?? ''])
    }
    const blob = new Blob([rows.map((r) => r.map(q).join(',')).join('\n') + '\n'], { type: 'text/csv' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `virtomir-activity-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const when = (t: string) => new Date(t).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
  // Group by day so long lists stay readable.
  let days = $derived.by(() => {
    const out: { day: string; items: Entry[] }[] = []
    for (const e of entries ?? []) {
      const day = new Date(e.time).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' })
      if (out.at(-1)?.day !== day) out.push({ day, items: [] })
      out.at(-1)!.items.push(e as Entry)
    }
    return out
  })
</script>

<svelte:head><title>Activity · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Activity</h1>
    <p class="lead">Who changed what, when — every change and sign-in, including refused ones{cluster.clustered ? ', on every host' : ''}.</p>
  </div>
  <div class="tools">
    <select class="ui-input" bind:value={user} aria-label="Filter by user">
      <option value="">Everyone</option>
      {#each users as u (u)}<option value={u}>{u}</option>{/each}
    </select>
    <label class="ui-switch"><input type="checkbox" bind:checked={failedOnly} /> Failed only</label>
    <button class="ui-btn ghost" onclick={load} title="Reload"><RefreshCw size={15} /></button>
    <button class="ui-btn" disabled={!entries?.length} onclick={exportCSV} title="Download what is listed as a CSV file"><Download size={15} /> Export</button>
  </div>
</header>

{#if !session.can('admin')}
  <div class="ui-error-box">{session.why('admin')}</div>
{:else if error}
  <div class="ui-error-box">{error}</div>
{:else if entries === null}
  <p class="muted">Loading…</p>
{:else if entries.length === 0}
  <div class="empty ui-panel">
    <History size={28} strokeWidth={1.5} />
    <strong>Nothing yet</strong>
    <span>Changes made in VirtoMir or through its API appear here.</span>
  </div>
{:else}
  {#each days as d (d.day)}
    <section class="day">
      <h2>{d.day}</h2>
      <ol class="ui-panel list">
        {#each d.items as e, i (e.time + i)}
          {@const info = describe(e, names)}
          {@const ok = e.status > 0 && e.status < 400}
          <li class:failed={!ok}>
            <span class="icon">{#if ok}<CircleCheck size={16} />{:else}<CircleX size={16} />{/if}</span>
            <div class="what">
              <span class="sentence">
                {#if e.hostName}<span class="host-chip">{e.hostName}</span>{/if}
                <b>{e.user || 'someone'}</b>
                {info.before}{#if info.machine}{#if names.has(info.machine)}<a href={machineLink(info.machine).href} onclick={(ev) => open(ev, info.machine!)}>{info.machineName}</a>{:else}<span class="gone">{info.machineName}</span>{/if}{/if}{info.after}
                {#if e.token}<span class="tok" title="Through the API token “{e.token}”"><KeyRound size={12} /> {e.token}</span>{/if}
              </span>
              {#if e.error}<span class="err">{e.error}</span>{/if}
            </div>
            <span class="meta mono" title={e.remote}>{when(e.time).split(', ').pop()}</span>
          </li>
        {/each}
      </ol>
    </section>
  {/each}
{/if}

<style>
  .host-chip {
    margin-right: 4px;
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
  .tools {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .tools select {
    width: 160px;
  }
  .day {
    margin-bottom: 18px;
  }
  h2 {
    margin: 0 0 8px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 600;
  }
  .list {
    margin: 0;
    padding: 0;
    list-style: none;
    overflow: hidden;
  }
  li {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 10px 16px;
    border-top: 1px solid var(--border);
    font-size: 13.5px;
  }
  li:first-child {
    border-top: 0;
  }
  .icon {
    display: grid;
    padding-top: 1px;
    color: var(--ok);
  }
  .failed .icon {
    color: var(--danger);
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .sentence b {
    font-weight: 600;
  }
  .sentence a {
    color: var(--accent);
    text-decoration: none;
  }
  .gone {
    color: var(--text-3);
  }
  .tok {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: 6px;
    padding: 0 7px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-3);
    font-size: 11.5px;
  }
  .err {
    color: var(--danger);
    font-size: 12.5px;
  }
  .meta {
    flex: none;
    color: var(--text-3);
    font-size: 12px;
  }
  .muted {
    color: var(--text-3);
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
