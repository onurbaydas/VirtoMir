<script lang="ts">
  import BellRing from '@lucide/svelte/icons/bell-ring'
  import Check from '@lucide/svelte/icons/check'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import Plus from '@lucide/svelte/icons/plus'
  import Send from '@lucide/svelte/icons/send'
  import Trash from '@lucide/svelte/icons/trash'
  import Save from '@lucide/svelte/icons/save'
  import Pencil from '@lucide/svelte/icons/pencil'
  import { api, errorText, type Alert, type AlertChannel, type AlertConfig, type AlertKind, type AlertRule } from '../api'
  import { alerts, type HostAlert } from '../alerts.svelte'
  import { cluster } from '../cluster.svelte'
  import { live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'

  let { tab = '' }: { tab?: string } = $props()
  let view = $derived(tab === 'rules' || tab === 'notifications' ? tab : 'alerts')
  const go = (t: string) => router.navigate(`/alerts${t ? '#' + t : ''}`, { replace: true })

  // ---- Alerts ---------------------------------------------------------------------------
  $effect(() => {
    void alerts.refresh()
  })
  let resolved = $derived(alerts.list.filter((a) => a.resolved))

  async function ack(a: HostAlert) {
    try {
      await api.ackAlert(a.id, a.hostId)
      await alerts.refresh()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Could not acknowledge', detail: errorText(e) })
    }
  }
  const since = (s: string) => {
    const m = Math.round((Date.now() - Date.parse(s)) / 60000)
    return m < 1 ? 'just now' : m < 60 ? `${m} min` : m < 1440 ? `${Math.round(m / 60)} h` : `${Math.round(m / 1440)} d`
  }
  const lasted = (a: Alert) => {
    const m = Math.round((Date.parse(a.resolved!) - Date.parse(a.started)) / 60000)
    return m < 1 ? '<1 min' : m < 60 ? `${m} min` : `${Math.round(m / 60)} h`
  }
  const when = (s: string) => new Date(s).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })
  // In a cluster an alert belongs to a member: links open that member's page.
  const memberOf = (a: HostAlert) => cluster.members.find((m) => m.id === a.hostId)
  const hostOf = (a: HostAlert) => {
    const m = memberOf(a)
    return !m || m.self ? null : m.id
  }
  const hrefOn = (a: HostAlert, path: string) => (a.hostId ? router.hostHref(hostOf(a), path) : router.scoped(path))
  function goOn(e: MouseEvent, a: HostAlert, path: string) {
    if (!a.hostId) return router.link(e)
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    router.go(hostOf(a), path)
  }

  const isVM = (a: Alert) => !!a.subjectId && /^[0-9a-f-]{36}$/.test(a.subjectId)

  // ---- Config -----------------------------------------------------------------------------
  let cfg = $state<AlertConfig | null>(null)
  let saved = $state('')
  let cfgErr = $state('')
  let saving = $state(false)
  async function loadCfg() {
    if (!session.can('admin')) return
    try {
      const c = await api.alertConfig()
      cfg = c
      saved = JSON.stringify(c)
      cfgErr = ''
    } catch (e) {
      cfgErr = errorText(e)
    }
  }
  $effect(() => {
    if (view !== 'alerts' && !cfg) void loadCfg()
  })
  let dirty = $derived(!!cfg && JSON.stringify(cfg) !== saved)
  /** Save the rules and channels on every cluster host, not just the one shown. */
  let everywhere = $state(true)

  async function saveCfg() {
    if (!cfg) return
    saving = true
    cfgErr = ''
    try {
      const snap = $state.snapshot(cfg)
      if (cluster.clustered && everywhere) {
        const res = await cluster.everywhere((h) => api.saveAlertConfig(snap, h))
        const failed = res.filter((r) => r.error)
        if (failed.length === res.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
        saved = JSON.stringify(snap)
        toasts.push({
          kind: failed.length ? 'warn' : 'info',
          title: failed.length ? `Saved on ${res.length - failed.length} of ${res.length} hosts` : 'Alert settings saved on every host',
          detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') || undefined,
        })
      } else {
        const c = await api.saveAlertConfig(snap)
        cfg = c
        saved = JSON.stringify(c)
        toasts.push({ kind: 'info', title: 'Alert settings saved' })
      }
    } catch (e) {
      cfgErr = errorText(e)
    } finally {
      saving = false
    }
  }

  const KINDS: Record<AlertKind, { label: string; unit?: string; above?: boolean; target?: 'vm' | 'pool' }> = {
    'host.cpu': { label: 'Host CPU above', unit: '%', above: true },
    'host.mem': { label: 'Host memory above', unit: '%', above: true },
    'vm.cpu': { label: 'Machine CPU above', unit: '%', above: true, target: 'vm' },
    'vm.crashed': { label: 'Machine crashed', target: 'vm' },
    'vm.down': { label: 'Autostart machine not running', target: 'vm' },
    'pool.free': { label: 'Pool free space below', unit: '%', target: 'pool' },
    'libvirt.down': { label: 'libvirt unreachable' },
  }
  const machines = $derived(live.snapshot?.domains ?? [])
  let pools = $state<string[]>([])
  $effect(() => {
    if (view === 'rules') void api.pools().then((p) => (pools = p.map((x) => x.name))).catch(() => {})
  })

  function addRule(kind: AlertKind) {
    cfg?.rules.push({ id: '', kind, enabled: true, threshold: KINDS[kind].unit ? 90 : undefined, forSec: 300, severity: 'warning', target: '' })
  }

  // ---- Channels ---------------------------------------------------------------------------
  let editing = $state<AlertChannel | null>(null)
  let editIndex = $state(-1)
  let testing = $state(false)
  function newChannel(kind: 'webhook' | 'email') {
    editIndex = -1
    editing = kind === 'webhook'
      ? { id: '', name: '', kind, enabled: true, minSeverity: 'warning', url: '', format: 'json', resolved: true }
      : { id: '', name: '', kind, enabled: true, minSeverity: 'warning', to: '', resolved: true }
  }
  function editChannel(i: number) {
    editIndex = i
    editing = structuredClone($state.snapshot(cfg!.channels[i]))
  }
  function keepChannel(e: SubmitEvent) {
    e.preventDefault()
    if (!cfg || !editing) return
    if (editIndex >= 0) cfg.channels[editIndex] = editing
    else cfg.channels.push(editing)
    editing = null
  }
  async function test(ch: AlertChannel) {
    testing = true
    try {
      await api.testChannel($state.snapshot(ch))
      toasts.push({ kind: 'info', title: `Test sent to ${ch.name || 'the channel'}` })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Test failed', detail: errorText(e) })
    } finally {
      testing = false
    }
  }
  const FORMATS = [
    { key: 'json', label: 'Generic JSON', hint: 'Any webhook receiver, n8n, Home Assistant…' },
    { key: 'slack', label: 'Slack / Mattermost', hint: 'Incoming webhook URL' },
    { key: 'discord', label: 'Discord', hint: 'Channel webhook URL' },
    { key: 'ntfy', label: 'ntfy', hint: 'https://ntfy.sh/<topic> or your own server — push to your phone' },
  ] as const
</script>

<svelte:head><title>Alerts · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Alerts</h1>
    <p class="lead">
      {#if cluster.clustered}
        Every host watches itself and its machines; this list shows all hosts.{#if view !== 'alerts'} Rules and notifications below are those of <b>{cluster.current?.name}</b> — switch host in the sidebar to change another one's.{/if}
      {:else}
        VirtoMir watches the host and its machines and tells you when something needs attention.
      {/if}
    </p>
  </div>
</header>

<div class="tabs" role="tablist">
  <button role="tab" aria-selected={view === 'alerts'} class:on={view === 'alerts'} onclick={() => go('')}>
    Alerts {#if alerts.active.length}<span class="count">{alerts.active.length}</span>{/if}
  </button>
  {#if session.can('admin')}
    <button role="tab" aria-selected={view === 'rules'} class:on={view === 'rules'} onclick={() => go('rules')}>Rules</button>
    <button role="tab" aria-selected={view === 'notifications'} class:on={view === 'notifications'} onclick={() => go('notifications')}>Notifications</button>
  {/if}
</div>

{#if view === 'alerts'}
  {#if alerts.active.length === 0}
    <div class="ok ui-panel">
      <CircleCheck size={30} strokeWidth={1.5} />
      <strong>All clear</strong>
      <span>Nothing needs attention right now.</span>
    </div>
  {:else}
    <section class="list">
      {#each alerts.active as a (a.key)}
        <article class="alert ui-panel {a.severity}" class:acked={!!a.ackedBy}>
          <span class="sev"><BellRing size={16} /></span>
          <div class="body">
            <div class="top">
              <span class="badge {a.severity}">{a.severity}</span>
              {#if a.hostName}<span class="host-chip" title="Host">{a.hostName}</span>{/if}
              {#if isVM(a)}
                <a href={hrefOn(a, `/machines/${a.subjectId}`)} onclick={(e) => goOn(e, a, `/machines/${a.subjectId}`)} class="subj">{a.subject}</a>
              {:else if a.kind === 'pool.free'}
                <a href={hrefOn(a, `/storage#${a.subjectId}`)} onclick={(e) => goOn(e, a, `/storage#${a.subjectId}`)} class="subj">{a.subject}</a>
              {:else}
                <span class="subj">{a.subject}</span>
              {/if}
              <span class="since">for {since(a.started)}</span>
            </div>
            <p>{a.message}</p>
            {#if a.ackedBy}<span class="acked-by"><Check size={12} /> Seen by {a.ackedBy}</span>{/if}
          </div>
          {#if !a.ackedBy}
            <button class="ui-btn sm" disabled={!session.can('operator')} title={session.why('operator') || 'Mark as seen; it stays until the problem is gone'} onclick={() => ack(a)}>
              <Check size={14} /> Acknowledge
            </button>
          {/if}
        </article>
      {/each}
    </section>
  {/if}

  {#if resolved.length}
    <h2 class="sub">Recently resolved</h2>
    <div class="ui-panel history">
      {#each resolved.slice(0, 50) as a (a.key)}
        <div class="h-row">
          <span class="badge {a.severity} small">{a.severity}</span>
          {#if a.hostName}<span class="host-chip">{a.hostName}</span>{/if}
          <span class="h-msg">{a.message}</span>
          <span class="h-meta">{when(a.started)} · lasted {lasted(a)}</span>
        </div>
      {/each}
    </div>
  {/if}
{:else if !cfg}
  {#if cfgErr}<div class="ui-error-box">{cfgErr}</div>{:else}<p class="muted">Loading…</p>{/if}
{:else if view === 'rules'}
  <section class="ui-panel card">
    <div class="rules">
      {#each cfg.rules as r, i (r.id || i)}
        {@const k = KINDS[r.kind]}
        <div class="rule" class:off={!r.enabled}>
          <label class="ui-switch"><input type="checkbox" bind:checked={r.enabled} aria-label="Enabled" /></label>
          <span class="rname">{k.label}</span>
          {#if k.unit}
            <span class="inline"><input class="ui-input num" type="number" min="1" max="100" bind:value={r.threshold} /> {k.unit}</span>
          {/if}
          <span class="inline">
            {k.unit || r.kind === 'vm.down' || r.kind === 'libvirt.down' ? 'for' : 'after'}
            <select class="ui-input dur" bind:value={r.forSec}>
              <option value={0}>at once</option>
              <option value={60}>1 min</option>
              <option value={120}>2 min</option>
              <option value={300}>5 min</option>
              <option value={600}>10 min</option>
              <option value={1800}>30 min</option>
              <option value={3600}>1 h</option>
            </select>
          </span>
          {#if k.target === 'vm'}
            <select class="ui-input tgt" bind:value={r.target} aria-label="Which machine">
              <option value="">any machine</option>
              {#each machines as m (m.uuid)}<option value={m.uuid}>{m.name}</option>{/each}
            </select>
          {:else if k.target === 'pool'}
            <select class="ui-input tgt" bind:value={r.target} aria-label="Which pool">
              <option value="">any pool</option>
              {#each pools as p (p)}<option value={p}>{p}</option>{/each}
            </select>
          {:else}
            <span class="tgt"></span>
          {/if}
          <select class="ui-input sevsel {r.severity}" bind:value={r.severity} aria-label="Severity">
            <option value="warning">warning</option>
            <option value="critical">critical</option>
          </select>
          <button class="ui-btn sm ghost icon" aria-label="Remove rule" title="Remove rule" onclick={() => cfg?.rules.splice(i, 1)}><Trash size={14} /></button>
        </div>
      {/each}
    </div>
    <div class="add">
      <span class="muted">Add a rule:</span>
      {#each Object.entries(KINDS) as [kind, k] (kind)}
        <button class="chip-btn" onclick={() => addRule(kind as AlertKind)}><Plus size={12} /> {k.label}</button>
      {/each}
    </div>
  </section>
{:else}
  <section class="ui-panel card">
    <header class="ch-head">
      <h2>Channels</h2>
      <div class="ch-add">
        <button class="ui-btn sm" onclick={() => newChannel('webhook')}><Plus size={14} /> Webhook</button>
        <button class="ui-btn sm" onclick={() => newChannel('email')}><Plus size={14} /> Email</button>
      </div>
    </header>
    {#if cfg.channels.length === 0}
      <p class="muted">No channels yet: alerts only show up here. Add ntfy for phone pushes, a chat webhook, or email.</p>
    {:else}
      {#each cfg.channels as ch, i (ch.id || i)}
        <div class="channel" class:off={!ch.enabled}>
          <label class="ui-switch"><input type="checkbox" bind:checked={ch.enabled} aria-label="Enabled" /></label>
          <div class="ch-text">
            <strong>{ch.name}</strong>
            <span class="muted mono">{ch.kind === 'webhook' ? `${ch.format} · ${ch.url}` : `email · ${ch.to}`}</span>
          </div>
          <span class="badge {ch.minSeverity} small">{ch.minSeverity === 'critical' ? 'critical only' : 'all'}</span>
          <button class="ui-btn sm ghost" disabled={testing} onclick={() => test(ch)}><Send size={14} /> Test</button>
          <button class="ui-btn sm ghost icon" aria-label="Edit" onclick={() => editChannel(i)}><Pencil size={14} /></button>
          <button class="ui-btn sm ghost icon" aria-label="Remove" onclick={() => cfg?.channels.splice(i, 1)}><Trash size={14} /></button>
        </div>
      {/each}
    {/if}
  </section>

  <section class="ui-panel card">
    <h2>Mail server</h2>
    <p class="muted">Used by email channels.</p>
    <div class="smtp">
      <label class="ui-field"><span class="label">Server</span><input class="ui-input" bind:value={cfg.smtp.host} placeholder="smtp.example.com" /></label>
      <label class="ui-field"><span class="label">Security</span>
        <select class="ui-input" bind:value={cfg.smtp.tls}>
          <option value="starttls">STARTTLS (587)</option>
          <option value="tls">TLS (465)</option>
          <option value="none">None (local relay)</option>
        </select>
      </label>
      <label class="ui-field"><span class="label">Port</span><input class="ui-input" type="number" bind:value={cfg.smtp.port} placeholder="auto" /></label>
      <label class="ui-field"><span class="label">From</span><input class="ui-input" bind:value={cfg.smtp.from} placeholder="VirtoMir <virtomir@example.com>" /></label>
      <label class="ui-field"><span class="label">User</span><input class="ui-input" bind:value={cfg.smtp.username} autocomplete="off" /></label>
      <label class="ui-field"><span class="label">Password</span><input class="ui-input" type="password" bind:value={cfg.smtp.password} autocomplete="new-password" /></label>
    </div>
  </section>
{/if}

{#if view !== 'alerts' && cfg}
  <footer class="ui-panel bar">
    <span class="status">{cfgErr || (dirty ? 'Unsaved changes' : 'Everything is saved')}</span>
    {#if cluster.clustered}
      <label class="everywhere"><input type="checkbox" bind:checked={everywhere} /> Same on every host</label>
    {/if}
    <button class="ui-btn ghost" disabled={!dirty || saving} onclick={() => (cfg = JSON.parse(saved))}>Reset</button>
    <button class="ui-btn primary" disabled={!dirty || saving} onclick={saveCfg}><Save size={14} /> {saving ? 'Saving…' : 'Save'}</button>
  </footer>
{/if}

<Dialog open={editing !== null} title={editIndex >= 0 ? 'Edit channel' : editing?.kind === 'email' ? 'New email channel' : 'New webhook'} onclose={() => (editing = null)} width={520}>
  {#if editing}
    <form id="ch-form" class="form" onsubmit={keepChannel}>
      <label class="ui-field"><span class="label">Name</span><input class="ui-input" bind:value={editing.name} placeholder={editing.kind === 'email' ? 'Ops team' : 'My phone'} required /></label>
      {#if editing.kind === 'webhook'}
        <div class="formats" role="radiogroup" aria-label="Format">
          {#each FORMATS as f (f.key)}
            <label class="fmt" class:on={editing.format === f.key}>
              <input type="radio" name="fmt" value={f.key} bind:group={editing.format} />
              <strong>{f.label}</strong>
              <span>{f.hint}</span>
            </label>
          {/each}
        </div>
        <label class="ui-field"><span class="label">URL</span><input class="ui-input mono" bind:value={editing.url} placeholder={editing.format === 'ntfy' ? 'https://ntfy.sh/my-virtomir-alerts' : 'https://…'} required /></label>
      {:else}
        <label class="ui-field"><span class="label">Recipients</span><input class="ui-input" bind:value={editing.to} placeholder="ops@example.com, me@example.com" required /></label>
      {/if}
      <div class="two">
        <label class="ui-field"><span class="label">Send</span>
          <select class="ui-input" bind:value={editing.minSeverity}>
            <option value="warning">warnings and critical alerts</option>
            <option value="critical">critical alerts only</option>
          </select>
        </label>
        <label class="ui-switch resolved"><input type="checkbox" bind:checked={editing.resolved} /> Also when resolved</label>
      </div>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" type="button" disabled={testing || !editing} onclick={() => editing && test(editing)}><Send size={14} /> Send test</button>
    <span class="spacer"></span>
    <button class="ui-btn ghost" onclick={() => (editing = null)}>Cancel</button>
    <button class="ui-btn primary" form="ch-form">{editIndex >= 0 ? 'Keep changes' : 'Add channel'}</button>
  {/snippet}
</Dialog>

<style>
  .everywhere {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--text-2);
  }
  .host-chip {
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
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 16px;
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 38px;
    padding: 0 12px;
    margin-bottom: -1px;
    border: 0;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--text-2);
    font-size: 13.5px;
    font-weight: 500;
    cursor: pointer;
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .count {
    padding: 0 7px;
    border-radius: var(--radius-sm);
    border: 1px solid #000;
    background: var(--hazard);
    color: #000;
    font-size: 11.5px;
    font-weight: 700;
  }
  .ok {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 48px 16px;
    color: var(--ok);
    text-align: center;
  }
  .ok strong {
    color: var(--text);
    font-size: 15px;
  }
  .ok span {
    color: var(--text-3);
  }
  .list {
    display: grid;
    gap: 10px;
  }
  .alert {
    --c: var(--warn);
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 14px 16px;
    border-left: 3px solid var(--c);
  }
  .alert.critical {
    --c: var(--danger);
  }
  .alert.acked {
    opacity: 0.75;
  }
  .sev {
    display: grid;
    color: var(--c);
    padding-top: 2px;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .subj {
    font-weight: 600;
    color: var(--text);
    text-decoration: none;
  }
  a.subj:hover {
    color: var(--accent);
  }
  .since {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .body p {
    margin: 4px 0 0;
    color: var(--text-2);
    font-size: 13.5px;
  }
  .acked-by {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-top: 6px;
    color: var(--text-3);
    font-size: 12px;
  }
  .badge {
    padding: 1px 8px;
    border-radius: var(--radius-sm);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.03em;
  }
  .badge.small {
    font-size: 10.5px;
  }
  .badge.warning {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
  }
  .badge.critical {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .sub {
    margin: 22px 0 8px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 600;
  }
  .history {
    padding: 4px 0;
  }
  .h-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 16px;
    border-top: 1px solid var(--border);
    font-size: 13px;
  }
  .h-row:first-child {
    border-top: 0;
  }
  .h-msg {
    flex: 1;
    min-width: 0;
    color: var(--text-2);
  }
  .h-meta {
    color: var(--text-3);
    font-size: 12px;
    white-space: nowrap;
  }
  .card {
    padding: 14px 16px;
    margin-bottom: 14px;
  }
  .card h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
  }
  .rules {
    display: grid;
  }
  .rule {
    display: grid;
    grid-template-columns: 40px minmax(180px, 1.4fr) 110px 150px minmax(130px, 1fr) 110px 36px;
    gap: 10px;
    align-items: center;
    padding: 8px 0;
    border-top: 1px solid var(--border);
    font-size: 13.5px;
  }
  .rule:first-child {
    border-top: 0;
  }
  .rule.off .rname,
  .channel.off .ch-text {
    opacity: 0.5;
  }
  .rname {
    font-weight: 500;
  }
  .inline {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .num {
    width: 64px;
    height: 32px;
  }
  .dur,
  .tgt,
  .sevsel {
    height: 32px;
    font-size: 12.5px;
  }
  .sevsel.critical {
    color: var(--danger);
  }
  .sevsel.warning {
    color: var(--warn);
  }
  .icon {
    width: 32px;
    padding: 0;
  }
  .add {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }
  .chip-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 28px;
    padding: 0 10px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 12.5px;
    cursor: pointer;
  }
  .chip-btn:hover {
    color: var(--accent);
    border-color: var(--accent);
  }
  .ch-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }
  .ch-add {
    display: flex;
    gap: 6px;
  }
  .channel {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 0;
    border-top: 1px solid var(--border);
  }
  .ch-text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .ch-text span {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .smtp {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr;
    gap: 12px;
    margin-top: 10px;
  }
  .muted {
    color: var(--text-3);
    font-size: 13px;
  }
  .bar {
    position: sticky;
    bottom: 12px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    box-shadow: var(--shadow-pop);
  }
  .status {
    flex: 1;
    color: var(--text-3);
    font-size: 13px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .formats {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .fmt {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 9px 11px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .fmt input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .fmt.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .fmt:focus-within {
    outline: 2px solid var(--accent);
  }
  .fmt strong {
    font-size: 13px;
  }
  .fmt span {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 12px;
    align-items: end;
  }
  .resolved {
    padding-bottom: 8px;
  }
  .spacer {
    flex: 1;
  }
  @media (max-width: 1000px) {
    .rule {
      grid-template-columns: 40px 1fr auto;
    }
    .rule > :nth-child(n + 3) {
      grid-column: 2 / -1;
    }
    .smtp {
      grid-template-columns: 1fr;
    }
    .formats {
      grid-template-columns: 1fr;
    }
  }
</style>
