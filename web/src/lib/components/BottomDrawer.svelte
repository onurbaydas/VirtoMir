<script lang="ts">
  // The drawer at the bottom: background jobs, recent changes, active alerts, and the API
  // calls this browser made (copyable as curl). All of it live; nothing here is sample data.
  import { onMount } from 'svelte'
  import { api, type Job, type ActivityEntry } from '../api'
  import { alerts } from '../alerts.svelte'
  import { apiLog, asCurl } from '../apilog.svelte'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ChevronUp from '@lucide/svelte/icons/chevron-up'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'

  type DrawerTab = 'tasks' | 'events' | 'alerts' | 'inspector'

  let isOpen = $state(false)
  let activeTab = $state<DrawerTab>('tasks')
  let jobs = $state<Job[]>([])
  let activities = $state<ActivityEntry[]>([])
  let running = $derived(jobs.filter((j) => j.state === 'running').length)

  export function toggle() {
    isOpen = !isOpen
  }

  async function refreshData() {
    jobs = await api.jobs().catch(() => jobs)
    if (isOpen && activeTab === 'events' && session.can('admin')) activities = await api.activity({ limit: 30 }).catch(() => activities)
  }

  onMount(() => {
    void refreshData()
    const timer = setInterval(() => void refreshData(), 4000)
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === '`' && !['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName ?? '')) {
        e.preventDefault()
        toggle()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      clearInterval(timer)
      window.removeEventListener('keydown', onKeyDown)
    }
  })

  function show(t: DrawerTab) {
    activeTab = t
    isOpen = true
    void refreshData()
  }

  async function ack(id: string, host?: string) {
    try {
      await api.ackAlert(id, host)
      void alerts.refresh()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not acknowledged', detail: String(e) })
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      toasts.push({ kind: 'info', title: 'Copied', detail: 'Create a token under Account → API tokens and export it as VIRTOMIR_TOKEN.' })
    } catch {
      /* the clipboard needs https */
    }
  }
</script>

<footer class="bottom-drawer" class:expanded={isOpen}>
  <div class="drawer-bar">
    <div class="tabs">
      <button class="tab-btn" class:active={activeTab === 'tasks'} onclick={() => show('tasks')}>[TASKS{running ? ` (${running} RUNNING)` : ''}]</button>
      {#if session.can('admin')}<button class="tab-btn" class:active={activeTab === 'events'} onclick={() => show('events')}>[RECENT CHANGES]</button>{/if}
      <button class="tab-btn" class:active={activeTab === 'alerts'} onclick={() => show('alerts')}>[ALERTS ({alerts.open})]</button>
      <button class="tab-btn" class:active={activeTab === 'inspector'} onclick={() => show('inspector')}>[API CALLS]</button>
    </div>
    <div class="controls">
      <span class="shortcut-hint">` (BACKTICK)</span>
      <button class="toggle-btn" onclick={toggle} title="Toggle Drawer">
        {#if isOpen}<ChevronDown size={14} /><span>COLLAPSE</span>{:else}<ChevronUp size={14} /><span>EXPAND</span>{/if}
      </button>
    </div>
  </div>

  {#if isOpen}
    <div class="drawer-body">
      {#if activeTab === 'tasks'}
        <div class="pane">
          {#if jobs.length === 0}
            <div class="empty-state"><span>NO BACKGROUND TASKS SINCE VIRTOMIRD STARTED</span></div>
          {:else}
            <table class="brutal-table">
              <thead><tr><th>STARTED</th><th>KIND</th><th>TASK</th><th>DETAIL</th><th>STATUS</th></tr></thead>
              <tbody>
                {#each jobs.slice().sort((a, b) => Date.parse(b.started) - Date.parse(a.started)) as j (j.id)}
                  <tr>
                    <td class="mono text-muted">{new Date(j.started).toLocaleTimeString()}</td>
                    <td class="mono font-bold text-accent">{j.kind}</td>
                    <td>{j.title}</td>
                    <td class="mono text-muted">{j.state === 'running' ? j.message || 'Working…' : j.state === 'failed' ? j.message : (j.result ?? '')}</td>
                    <td class="mono"><span class="status-pill" class:ok={j.state === 'succeeded'} class:warn={j.state !== 'succeeded'}>[{j.state.toUpperCase()}]</span></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {:else if activeTab === 'events'}
        <div class="pane">
          {#if activities.length === 0}
            <div class="empty-state"><span>NO RECENT CHANGES</span></div>
          {:else}
            <table class="brutal-table">
              <thead><tr><th>TIME</th><th>USER</th><th>FROM</th><th>METHOD</th><th>PATH</th><th>STATUS</th></tr></thead>
              <tbody>
                {#each activities as a, idx (idx)}
                  <tr>
                    <td class="mono text-muted">{new Date(a.time).toLocaleTimeString()}</td>
                    <td class="mono font-bold">{a.user}</td>
                    <td class="mono text-muted">{a.remote ?? ''}</td>
                    <td class="mono font-bold">{a.method}</td>
                    <td class="mono">{a.path}{#if a.error}<div class="text-muted">{a.error}</div>{/if}</td>
                    <td class="mono"><span class="status-pill" class:ok={a.status < 300} class:warn={a.status >= 400}>[{a.status}]</span></td>
                  </tr>
                {/each}
              </tbody>
            </table>
            <a class="more mono" href="/activity" onclick={router.link}>ALL ACTIVITY →</a>
          {/if}
        </div>
      {:else if activeTab === 'alerts'}
        <div class="pane">
          {#if alerts.active.length === 0}
            <div class="empty-state ok"><span>● NO ACTIVE ALERTS</span></div>
          {:else}
            <div class="alerts-list">
              {#each alerts.active as a (a.key)}
                <div class="alert-item" class:warn={a.severity === 'warning'} class:crit={a.severity === 'critical'}>
                  <span class="mono font-bold">[{a.severity.toUpperCase()}]</span>
                  <span class="text">{a.hostName ? `${a.hostName}: ` : ''}{a.subject} — {a.message}</span>
                  {#if a.ackedBy}<span class="mono text-muted">acked by {a.ackedBy}</span>
                  {:else if session.can('operator')}<button class="action-btn" onclick={() => ack(a.id, a.hostId)}>[ACKNOWLEDGE]</button>{/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <div class="pane">
          {#if apiLog.calls.length === 0}
            <div class="empty-state"><span>NO API CALLS RECORDED YET (THE LIVE STREAM AND POLLING ARE NOT LISTED)</span></div>
          {:else}
            <table class="brutal-table">
              <thead><tr><th>TIME</th><th>CALL</th><th>STATUS</th><th>MS</th><th>AS CURL</th></tr></thead>
              <tbody>
                {#each apiLog.calls as c, idx (idx)}
                  <tr>
                    <td class="mono text-muted">{new Date(c.time).toLocaleTimeString()}</td>
                    <td class="mono font-bold text-accent">{c.method} {c.path}</td>
                    <td class="mono"><span class="status-pill" class:ok={c.status > 0 && c.status < 300} class:warn={c.status === 0 || c.status >= 400}>[{c.status || 'NET'}]</span></td>
                    <td class="mono text-muted">{c.ms}</td>
                    <td><button class="action-btn" onclick={() => copy(asCurl(c))}>[COPY]</button></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</footer>

<style>
  .bottom-drawer {
    position: fixed;
    bottom: 0;
    left: var(--sidebar-w);
    right: 0;
    z-index: 1000;
    background: var(--surface);
    border-top: var(--frame) solid var(--border-strong);
    box-shadow: 0 -2px 0 var(--border);
    transition: height 120ms var(--ease-out);
    display: flex;
    flex-direction: column;
  }
  @media (max-width: 900px) {
    .bottom-drawer {
      left: 0;
    }
  }
  .drawer-bar {
    height: 34px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 10px;
  }
  .tabs {
    display: flex;
    gap: 2px;
    height: 100%;
    align-items: center;
  }
  .tab-btn {
    height: 26px;
    padding: 0 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 700;
    cursor: pointer;
  }
  .tab-btn.active {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 8px;
    font-family: var(--font-mono);
    font-size: 10px;
  }
  .shortcut-hint {
    color: var(--text-3);
  }
  .toggle-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding: 0 8px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
    cursor: pointer;
  }
  .toggle-btn:hover {
    border-color: var(--accent);
  }
  .drawer-body {
    height: 220px;
    overflow-y: auto;
    background: var(--bg);
  }
  .pane {
    padding: 10px;
    height: 100%;
    overflow-y: auto;
  }
  .empty-state {
    padding: 24px;
    text-align: center;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--text-3);
  }
  .empty-state.ok {
    color: var(--ok);
  }
  .brutal-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11.5px;
  }
  .brutal-table th {
    padding: 6px 10px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 11px;
    font-weight: 700;
    text-align: left;
    letter-spacing: 0.05em;
  }
  .brutal-table td {
    padding: 6px 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text);
  }
  .text-accent {
    color: var(--accent);
  }
  .text-muted {
    color: var(--text-3);
  }
  .status-pill {
    padding: 1px 6px;
    border: 1px solid var(--border);
    font-size: 10px;
    font-weight: 700;
  }
  .status-pill.ok {
    border-color: var(--ok);
    color: var(--ok);
  }
  .status-pill.warn {
    border-color: var(--warn);
    color: var(--warn);
  }
  .action-btn {
    padding: 3px 8px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 10.5px;
    font-weight: 700;
    cursor: pointer;
  }
  .action-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
</style>
