<script lang="ts">
  import { downloadCSV } from '../csv'
  import { t } from '../i18n.svelte'
  import History from '@lucide/svelte/icons/clock-arrow-up'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleX from '@lucide/svelte/icons/circle-x'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import Download from '@lucide/svelte/icons/download'
  import Radio from '@lucide/svelte/icons/radio'
  import Dialog from '../components/Dialog.svelte'
  import { toasts } from '../toast.svelte'
  import { api, errorText, type ActivityEntry, type SIEMConfig } from '../api'
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

  let siemOpen = $state(false)
  let siemBusy = $state(false)
  let siemError = $state('')
  let siemEnabled = $state(false)
  let siemTarget = $state('udp://')
  let siemFacility = $state(1)
  let siemTag = $state('virtomir')
  let siemHostname = $state('')
  let siemTLSInsecure = $state(false)
  let siemLastError = $state('')

  async function openSIEM() {
    siemError = ''
    try {
      const cfg = await api.siemConfig()
      siemEnabled = cfg.enabled
      siemTarget = cfg.target || 'udp://'
      siemFacility = cfg.facility || 1
      siemTag = cfg.tag || 'virtomir'
      siemHostname = cfg.hostname || ''
      siemTLSInsecure = !!cfg.tlsInsecure
      siemLastError = cfg.lastError ?? ''
      siemOpen = true
    } catch (e) {
      siemError = errorText(e)
      siemOpen = true
    }
  }

  async function saveSIEM() {
    siemBusy = true
    siemError = ''
    try {
      await api.saveSiemConfig({
        enabled: siemEnabled,
        target: siemTarget.trim(),
        facility: Number(siemFacility),
        tag: siemTag.trim() || 'virtomir',
        hostname: siemHostname.trim(),
        tlsInsecure: siemTLSInsecure,
      })
      toasts.push({
        kind: 'info',
        title: t('SIEM / Syslog forwarding updated'),
        detail: siemEnabled ? t('Audit entries are now streamed via RFC 5424 to {target}', { target: siemTarget }) : t('Syslog forwarding disabled.'),
      })
      siemOpen = false
    } catch (e) {
      siemError = errorText(e)
    } finally {
      siemBusy = false
    }
  }


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
    const rows = [['time', 'host', 'user', 'token', 'what', 'method', 'path', 'status', 'error', 'remote']]
    for (const e of entries ?? []) {
      const d = describe(e, names)
      rows.push([e.time, e.hostName ?? '', e.user, e.token ?? '', d.before + (d.machineName ?? '') + d.after, e.method, e.path, String(e.status), e.error ?? '', e.remote ?? ''])
    }
    downloadCSV('virtomir-activity', rows)
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

<svelte:head><title>{t('Activity · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Activity')}</h1>
    <p class="lead">{t('Who changed what, when — every change and sign-in, including refused ones{v}.', { v: cluster.clustered ? ', on every host' : '' })}</p>
  </div>
  <div class="tools">
    <select class="ui-input" bind:value={user} aria-label={t('Filter by user')}>
      <option value="">{t('Everyone')}</option>
      {#each users as u (u)}<option value={u}>{u}</option>{/each}
    </select>
    <label class="ui-switch"><input type="checkbox" bind:checked={failedOnly} /> {t('Failed only')}</label>
    <button class="ui-btn ghost" onclick={load} title={t('Reload')}><RefreshCw size={15} /></button>
    <button class="ui-btn" disabled={!entries?.length} onclick={exportCSV} title={t('Download what is listed as a CSV file')}><Download size={15} /> {t('Export')}</button>
    {#if session.can('admin')}
      <button class="ui-btn" onclick={openSIEM} title={t('Configure remote SIEM / Syslog forwarding')}><Radio size={15} /> {t('SIEM / Syslog…')}</button>
    {/if}
  </div>

</header>

{#if !session.can('admin')}
  <div class="ui-error-box">{session.why('admin')}</div>
{:else if error}
  <div class="ui-error-box">{error}</div>
{:else if entries === null}
  <p class="muted">{t('Loading…')}</p>
{:else if entries.length === 0}
  <div class="empty ui-panel">
    <History size={28} strokeWidth={1.5} />
    <strong>{t('Nothing yet')}</strong>
    <span>{t('Changes made in VirtoMir or through its API appear here.')}</span>
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
                {#if e.token}<span class="tok" title={t('Through the API token “{token}”', { token: e.token })}><KeyRound size={12} /> {e.token}</span>{/if}
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

<Dialog open={siemOpen} title={t('Remote SIEM / Syslog Forwarding')} subtitle={t('Stream audit trail in RFC 5424 syslog format to enterprise SIEM (Splunk, Graylog, ELK).')} onclose={() => (siemOpen = false)}>
  <form id="siem" class="form" onsubmit={(e) => (e.preventDefault(), saveSIEM())}>
    <label class="ui-switch">
      <input type="checkbox" bind:checked={siemEnabled} />
      <span>{t('Enable live SIEM log streaming')}</span>
    </label>

    <label class="ui-field">
      <span class="label">{t('Target Collector URI')}</span>
      <input class="ui-input mono" bind:value={siemTarget} placeholder="udp://siem.corp.internal:514" />
      <span class="hint">{t('Supported schemes: udp://, tcp://, tls://')}</span>
    </label>

    <div class="row">
      <label class="ui-field">
        <span class="label">{t('Syslog Facility')}</span>
        <select class="ui-input" bind:value={siemFacility}>
          <option value={1}>{t('1 — User-Level (default)')}</option>
          <option value={16}>{t('16 — Local0')}</option>
          <option value={17}>{t('17 — Local1')}</option>
          <option value={18}>{t('18 — Local2')}</option>
          <option value={19}>{t('19 — Local3')}</option>
          <option value={20}>{t('20 — Local4')}</option>
          <option value={21}>{t('21 — Local5')}</option>
          <option value={22}>{t('22 — Local6')}</option>
          <option value={23}>{t('23 — Local7')}</option>
        </select>
      </label>
      <label class="ui-field">
        <span class="label">{t('Syslog Tag (App-Name)')}</span>
        <input class="ui-input mono" bind:value={siemTag} placeholder="virtomir" />
      </label>
    </div>

    <label class="ui-field">
      <span class="label">{t('Hostname identifier')} <span class="muted">{t('(optional)')}</span></span>
      <input class="ui-input mono" bind:value={siemHostname} placeholder={t('default: host system name')} />
    </label>

    {#if siemTarget.startsWith('tls://')}
      <label class="ui-switch">
        <input type="checkbox" bind:checked={siemTLSInsecure} />
        <span>{t('Skip TLS certificate verification (insecure/self-signed)')}</span>
      </label>
    {/if}

    {#if siemLastError && siemEnabled}<div class="ui-error-box">{t('The last entry could not be delivered: {error}', { error: siemLastError })}</div>{/if}
    {#if siemError}<div class="ui-error-box">{siemError}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (siemOpen = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="siem" disabled={siemBusy}>{siemBusy ? t('Saving…') : t('Save Configuration')}</button>
  {/snippet}
</Dialog>


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
