<script lang="ts">
  // High availability (ADR-0031): what every member sees, how each is fenced, which machines
  // are protected and where they run, and what HA did. All of it comes from /ha/status.
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import Power from '@lucide/svelte/icons/power'
  import FlaskConical from '@lucide/svelte/icons/flask-conical'
  import Save from '@lucide/svelte/icons/save'
  import Trash from '@lucide/svelte/icons/trash'
  import { errorText } from '../api'
  import { ent, PRIORITIES, type FenceAgent, type HASettings, type HAStatus, type Simulation } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { formatKiB } from '../format'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'

  let status = $state<HAStatus | null>(null)
  let error = $state('')
  let form = $state<HASettings | null>(null)
  let dirty = $state(false)
  let saving = $state(false)
  let sim = $state<Simulation | null>(null)
  let simError = $state('')
  let busyNode = $state('')

  async function load() {
    try {
      status = await ent.haStatus()
      error = ''
      if (!dirty) form = toForm(status)
    } catch (e) {
      error = errorText(e)
    }
  }

  function toForm(s: HAStatus): HASettings {
    const agents: Record<string, FenceAgent> = {}
    for (const n of s.nodes) agents[n.id] = s.config.fenceAgents[n.id] ? { ...s.config.fenceAgents[n.id] } : { driver: '', endpoint: '' }
    return {
      autoFailover: s.config.autoFailover,
      heartbeatIntervalSec: s.config.heartbeatIntervalSec,
      failoverTimeoutSec: s.config.failoverTimeoutSec,
      witness: s.config.witness ?? '',
      watchdog: s.config.watchdog,
      watchdogTimeoutSec: s.config.watchdogTimeoutSec,
      fenceAgents: agents,
    }
  }

  $effect(() => {
    void load()
    const t = setInterval(() => void load(), 3000)
    return () => clearInterval(t)
  })

  async function save() {
    if (!form) return
    saving = true
    try {
      status = await ent.haSettings(form)
      dirty = false
      form = toForm(status)
      toasts.push({ kind: 'info', title: 'HA settings saved', detail: 'Every member picks them up with its next heartbeat.' })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not saved', detail: errorText(e) })
    } finally {
      saving = false
    }
  }

  async function fence(node: string, action: 'status' | 'off' | 'reboot') {
    busyNode = node
    try {
      const r = await ent.haFence(node, action)
      toasts.push({ kind: 'info', title: `Fence ${action}`, detail: r.message })
    } catch (e) {
      toasts.push({ kind: 'error', title: `Fence ${action} failed`, detail: errorText(e) })
    } finally {
      busyNode = ''
      void load()
    }
  }

  async function confirmOff(node: string) {
    try {
      await ent.haConfirmOff(node)
      toasts.push({ kind: 'warn', title: 'Confirmed off', detail: 'The coordinator restarts its machines elsewhere now.' })
      void load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not confirmed', detail: errorText(e) })
    }
  }

  async function simulate(node: string) {
    simError = ''
    try {
      sim = await ent.haSimulate(node)
    } catch (e) {
      simError = errorText(e)
    }
  }

  async function unprotect(uuid: string, name: string) {
    try {
      await ent.haDeletePolicy(uuid)
      toasts.push({ kind: 'info', title: `${name} is no longer protected` })
      void load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not changed', detail: errorText(e) })
    }
  }

  const ago = (t?: string) => {
    if (!t) return 'never'
    const s = Math.max(0, Math.round((Date.now() - Date.parse(t)) / 1000))
    return s < 90 ? `${s}s ago` : s < 5400 ? `${Math.round(s / 60)} min ago` : new Date(t).toLocaleString()
  }
  const recoveryText: Record<string, string> = {
    fencing: 'Fencing through its BMC…',
    'waiting-confirmation': 'Waiting for an admin to confirm it is off',
    'waiting-watchdog': 'Waiting for its watchdog to reset it',
    'waiting-auto': 'Automatic failover is off',
    restarting: 'Restarting its machines elsewhere…',
    done: 'Its machines run elsewhere',
    failed: 'Recovery failed (see the log)',
  }
  let admin = $derived(session.can('admin'))
  let nameOf = $derived((id?: string) => status?.nodes.find((n) => n.id === id)?.name ?? id ?? '—')

  // What would go wrong if a host died now, worked out from the saved settings (P3: say it
  // where it matters instead of explaining HA in general).
  let risks = $derived.by(() => {
    if (!status?.clustered) return []
    const c = status.config
    const out: string[] = []
    if (status.nodes.length === 2 && !c.witness)
      out.push('Two hosts and no witness: when one dies, the other has no quorum and restarts nothing. Add a witness below.')
    for (const n of status.nodes) {
      const d = c.fenceAgents[n.id]?.driver ?? ''
      if (n.machines && (d === '' || d === 'manual') && !c.watchdog)
        out.push(`${n.name}: no BMC and no watchdog. If it dies, its ${n.machines} protected machine${n.machines === 1 ? '' : 's'} wait${n.machines === 1 ? 's' : ''} until an admin confirms it is off.`)
    }
    if (!c.autoFailover && status.machines.some((m) => m.enabled)) out.push('Automatic failover is off: machines of a dead host are only restarted by hand.')
    return out
  })
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !status || !form}
  <p class="muted">Reading the HA state…</p>
{:else}
  {@const q = status.quorum}
  <section class="ui-panel banner" class:ok={q.quorate} class:bad={!q.quorate && status.clustered}>
    {#if q.quorate || !status.clustered}<ShieldCheck size={22} />{:else}<ShieldAlert size={22} />{/if}
    <div>
      {#if !status.clustered}
        <strong>Standalone host</strong>
        <span>HA needs at least two members (Cluster → Add a host). Protected machines here are still started again after this host reboots.</span>
      {:else if q.quorate}
        <strong>Quorum: {q.votes} of {q.totalVotes} votes</strong>
        <span>
          {q.required} needed · coordinator {nameOf(q.coordinator)}
          {#if q.witness}· witness {q.witness} {q.witnessReachable ? 'reachable' : 'NOT reachable'}{/if}
          · automatic failover {status.config.autoFailover ? 'on' : 'off'}
        </span>
      {:else}
        <strong>No quorum: this host sees {q.votes} of {q.totalVotes} votes ({q.required} needed)</strong>
        <span>It has stopped its protected machines; the members that still have quorum restart them.</span>
      {/if}
    </div>
    {#if status.watchdogArmed}<span class="chip mono">Watchdog armed</span>{/if}
  </section>
  {#if status.watchdogError}<div class="ui-error-box">Watchdog: {status.watchdogError}</div>{/if}
  {#each risks as r (r)}<p class="risk">{r}</p>{/each}

  <h2 class="sub">Members and fencing</h2>
  <section class="ui-panel table-wrap">
    <table class="t">
      <thead><tr><th>Host</th><th>State</th><th>Protected</th><th>Memory free</th><th>Fencing</th><th></th></tr></thead>
      <tbody>
        {#each status.nodes as n (n.id)}
          {@const a = form.fenceAgents[n.id]}
          <tr>
            <td><b>{n.name}</b>{#if n.self} <span class="muted">(this host)</span>{/if}{#if q.coordinator === n.id} <span class="chip mono">coordinator</span>{/if}</td>
            <td>
              <span class="dot {n.status}"></span>{n.status}
              <div class="muted small">{n.self ? '' : `heartbeat ${ago(n.lastSeen)}`}</div>
              {#if n.recovery}<div class="rec {n.recovery}">{recoveryText[n.recovery] ?? n.recovery}</div>{/if}
              {#if n.fence}<div class="muted small mono">{n.fence.message}</div>{/if}
            </td>
            <td class="mono">{n.machines}</td>
            <td class="mono">{n.report?.memTotalKiB ? `${formatKiB(n.report.memFreeKiB)} / ${formatKiB(n.report.memTotalKiB)}` : '—'}</td>
            <td class="agent">
              {#if a}
                <select class="ui-input" bind:value={a.driver} disabled={!admin} onchange={() => (dirty = true)}>
                  <option value="">None (watchdog or confirmation)</option>
                  <option value="ipmi">IPMI (ipmitool)</option>
                  <option value="redfish">Redfish</option>
                  <option value="manual">Manual confirmation</option>
                </select>
                {#if a.driver === 'ipmi' || a.driver === 'redfish'}
                  <input class="ui-input mono" placeholder={a.driver === 'ipmi' ? 'BMC address (host[:port])' : 'https://bmc-address'} bind:value={a.endpoint} oninput={() => (dirty = true)} disabled={!admin} />
                  <div class="row2">
                    <input class="ui-input" placeholder="user" bind:value={a.username} oninput={() => (dirty = true)} disabled={!admin} autocomplete="off" />
                    <input class="ui-input" type="password" placeholder="password" bind:value={a.password} oninput={() => (dirty = true)} disabled={!admin} autocomplete="new-password" />
                  </div>
                  {#if a.driver === 'redfish'}
                    <label class="ui-switch small"><input type="checkbox" bind:checked={a.insecureTls} onchange={() => (dirty = true)} disabled={!admin} /> Accept self-signed certificate</label>
                  {/if}
                {/if}
              {/if}
            </td>
            <td class="acts">
              {#if admin && !n.self}
                {#if status.config.fenceAgents[n.id]?.driver === 'ipmi' || status.config.fenceAgents[n.id]?.driver === 'redfish'}
                  <button class="ui-btn sm" disabled={busyNode === n.id} onclick={() => fence(n.id, 'status')}>Test BMC</button>
                  <ConfirmButton label="Power off" confirm="Really power it off?" icon={Power} run={() => fence(n.id, 'off')} />
                {/if}
                {#if n.status === 'dead'}
                  <ConfirmButton label="Confirm it is off" confirm="I checked: it is powered off" run={() => confirmOff(n.id)} />
                {/if}
              {/if}
              <button class="ui-btn sm ghost" onclick={() => simulate(n.id)}><FlaskConical size={13} /> If it died…</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>

  <h2 class="sub">Settings <span class="muted">· the same on every member (version {status.config.version})</span></h2>
  <section class="ui-panel settings">
    <label class="ui-switch"><input type="checkbox" bind:checked={form.autoFailover} onchange={() => (dirty = true)} disabled={!admin} /> Restart the machines of a dead host automatically</label>
    <div class="grid">
      <label class="ui-field"><span class="label">Heartbeat every (s)</span>
        <input class="ui-input" type="number" min="1" max="30" bind:value={form.heartbeatIntervalSec} oninput={() => (dirty = true)} disabled={!admin} /></label>
      <label class="ui-field"><span class="label">Dead after silence of (s)</span>
        <input class="ui-input" type="number" min="10" bind:value={form.failoverTimeoutSec} oninput={() => (dirty = true)} disabled={!admin} />
        <span class="hint">Shorter also fails over on a network hiccup.</span></label>
      <label class="ui-field"><span class="label">Witness (host:port)</span>
        <input class="ui-input mono" placeholder="10.0.0.9:22" bind:value={form.witness} oninput={() => (dirty = true)} disabled={!admin} />
        <span class="hint">An extra vote: any machine that accepts TCP on that port.</span></label>
    </div>
    <label class="ui-switch"><input type="checkbox" bind:checked={form.watchdog} onchange={() => (dirty = true)} disabled={!admin} /> Arm the hardware watchdog on hosts with protected machines</label>
    {#if form.watchdog}
      <div class="grid">
        <label class="ui-field"><span class="label">Watchdog timeout (s)</span>
          <input class="ui-input" type="number" min="10" max="600" bind:value={form.watchdogTimeoutSec} oninput={() => (dirty = true)} disabled={!admin} /></label>
        <p class="hint warn">A host that hangs or loses quorum <b>resets itself</b>. Needs /dev/watchdog (or <span class="mono">modprobe softdog</span>).</p>
      </div>
    {/if}
    <p class="hint">Protected machines' disks must be on storage every member reaches.</p>
    {#if admin}
      <div class="actions"><button class="ui-btn primary" disabled={!dirty || saving} onclick={save}><Save size={14} /> {saving ? 'Saving…' : 'Save settings'}</button>
        {#if dirty}<button class="ui-btn ghost" onclick={() => { dirty = false; form = toForm(status!) }}>Discard</button>{/if}</div>
    {/if}
  </section>

  <h2 class="sub">Protected machines</h2>
  <section class="ui-panel table-wrap">
    {#if status.machines.length === 0}
      <p class="muted pad">No machine is protected. Protect one on its Settings tab (High availability).</p>
    {:else}
      <table class="t">
        <thead><tr><th>Machine</th><th>Priority</th><th>Runs on</th><th>Preferred host</th><th>Definition copied</th><th></th></tr></thead>
        <tbody>
          {#each status.machines as m (m.uuid)}
            <tr>
              <td><b>{m.name || m.uuid}</b>{#if !m.enabled} <span class="muted">(off)</span>{/if}</td>
              <td><span class="prio {m.priority}">{m.priority}</span>{#if m.restartDelaySec} <span class="muted small">+{m.restartDelaySec}s</span>{/if}</td>
              <td>{m.ownerName ?? '—'} {#if m.known}<span class="muted small">{m.running ? 'running' : 'stopped'}</span>{/if}</td>
              <td>{m.targetNode ? nameOf(m.targetNode) : 'most free memory'}</td>
              <td class="muted small">{m.known ? ago(m.updated) : 'not yet — it cannot be recovered until its host sends it'}</td>
              <td>{#if admin}<ConfirmButton label="Unprotect" confirm="Stop protecting it?" icon={Trash} run={() => unprotect(m.uuid, m.name)} />{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <h2 class="sub">What HA did <span class="muted">· on this host</span></h2>
  <section class="ui-panel log mono">
    {#each status.events.slice(0, 60) as e, i (i)}
      <div class="ev {e.level}"><span class="t">{new Date(e.time).toLocaleString()}</span>{#if e.node}<span class="who">{e.node}</span>{/if}<span>{e.message}</span></div>
    {:else}
      <p class="muted">Nothing yet.</p>
    {/each}
  </section>
{/if}

<Dialog open={!!sim || !!simError} title={sim ? `If ${sim.node.name} died now` : 'Simulation'} onclose={() => { sim = null; simError = '' }} width={620}>
  {#if simError}
    <div class="ui-error-box">{simError}</div>
  {:else if sim}
    <ul class="facts">
      <li class:bad={!sim.quorate}>The others {sim.quorate ? 'keep' : 'would NOT have'} quorum.</li>
      <li>It would be fenced: {sim.fencing}.</li>
      <li class:bad={!sim.canRecover}>{sim.canRecover ? 'Its protected machines would be restarted as below.' : 'Its machines would NOT all be restarted automatically.'}</li>
    </ul>
    {#if sim.steps?.length}
      <table class="t">
        <thead><tr><th>#</th><th>Machine</th><th>Priority</th><th>Restarts on</th></tr></thead>
        <tbody>
          {#each sim.steps as s (s.uuid)}
            <tr><td>{s.order}</td><td>{s.name}</td><td>{s.priority}</td><td class:bad={!!s.problem}>{s.target ?? s.problem}</td></tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted">No protected machine runs there: nothing would be fenced or restarted.</p>
    {/if}
  {/if}
</Dialog>

<style>
  .banner { display: flex; gap: 12px; align-items: center; padding: 14px 16px; margin-bottom: 12px; border-left: 4px solid var(--idle); }
  .banner.ok { border-left-color: var(--ok); }
  .banner.bad { border-left-color: var(--danger); background: var(--danger-soft); }
  .banner div { display: flex; flex-direction: column; gap: 2px; flex: 1; }
  .banner span { color: var(--text-2); font-size: 13px; }
  .sub { font-size: 14px; margin: 20px 0 8px; }
  .table-wrap { overflow-x: auto; padding: 0; }
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 8px 10px; border-bottom: 1px solid var(--border); }
  .t td { padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
  .dot { display: inline-block; width: 8px; height: 8px; margin-right: 6px; background: var(--idle); }
  .dot.online { background: var(--ok); }
  .dot.suspect { background: var(--warn); }
  .dot.dead { background: var(--danger); }
  .rec { font-size: 12px; margin-top: 4px; color: var(--warn); }
  .rec.done { color: var(--ok); }
  .rec.failed, .rec.waiting-confirmation { color: var(--danger); font-weight: 600; }
  .agent { min-width: 240px; display: flex; flex-direction: column; gap: 4px; }
  .row2 { display: grid; grid-template-columns: 1fr 1fr; gap: 4px; }
  .acts { white-space: nowrap; }
  .acts :global(.ui-btn) { margin: 0 4px 4px 0; }
  .settings { display: flex; flex-direction: column; gap: 12px; padding: 16px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; }
  .hint { color: var(--text-2); font-size: 12px; margin: 0; }
  .hint.warn { color: var(--warn); }
  .risk { margin: 0 0 8px; padding: 8px 12px; border: 2px solid var(--warn); font-size: 13px; }
  .actions { display: flex; gap: 8px; }
  .prio { font: 500 12px var(--font-sans); }
  .prio.critical { color: var(--danger); }
  .prio.high { color: var(--warn); }
  .small { font-size: 11px; }
  .pad { padding: 14px; margin: 0; }
  .log { max-height: 320px; overflow: auto; font-size: 12px; padding: 8px 12px; }
  .ev { display: flex; gap: 10px; padding: 3px 0; border-bottom: 1px dashed var(--border); }
  .ev .t { color: var(--text-2); white-space: nowrap; }
  .ev .who { font-weight: 600; }
  .ev.warn { color: var(--warn); }
  .ev.error { color: var(--danger); }
  .facts { margin: 0 0 12px; padding-left: 18px; }
  .bad { color: var(--danger); font-weight: 600; }
  .chip { border: 1px solid currentColor; padding: 1px 6px; font-size: 10px; }
  .muted { color: var(--text-2); }
</style>
