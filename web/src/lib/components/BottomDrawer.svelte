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
  import X from '@lucide/svelte/icons/x'

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
    const onToggle = () => toggle()
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('toggle-drawer', onToggle)
    return () => {
      clearInterval(timer)
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('toggle-drawer', onToggle)
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

{#if isOpen}
  <section class="drawer" aria-label="Tasks and activity">
    <header>
      <div class="tabs" role="tablist">
        <button role="tab" aria-selected={activeTab === 'tasks'} class:on={activeTab === 'tasks'} onclick={() => show('tasks')}>Tasks{running ? ` · ${running} running` : ''}</button>
        {#if session.can('admin')}<button role="tab" aria-selected={activeTab === 'events'} class:on={activeTab === 'events'} onclick={() => show('events')}>Recent changes</button>{/if}
        <button role="tab" aria-selected={activeTab === 'alerts'} class:on={activeTab === 'alerts'} onclick={() => show('alerts')}>Alerts{alerts.open ? ` · ${alerts.open}` : ''}</button>
        <button role="tab" aria-selected={activeTab === 'inspector'} class:on={activeTab === 'inspector'} onclick={() => show('inspector')}>API calls</button>
      </div>
      <span class="hint">Press ` to toggle</span>
      <button class="close" onclick={toggle} aria-label="Close"><X size={16} /></button>
    </header>

    <div class="body">
      {#if activeTab === 'tasks'}
        {#if jobs.length === 0}
          <p class="empty">No background tasks since VirtoMir started.</p>
        {:else}
          <table>
            <thead><tr><th>Started</th><th>Task</th><th>Detail</th><th>Status</th></tr></thead>
            <tbody>
              {#each jobs.slice().sort((a, b) => Date.parse(b.started) - Date.parse(a.started)) as j (j.id)}
                <tr>
                  <td class="muted">{new Date(j.started).toLocaleTimeString()}</td>
                  <td>{j.title}</td>
                  <td class="muted">{j.state === 'running' ? j.message || 'Working…' : j.state === 'failed' ? j.message : (j.result ?? '')}</td>
                  <td><span class="st {j.state}">{j.state}</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if activeTab === 'events'}
        {#if activities.length === 0}
          <p class="empty">No recent changes.</p>
        {:else}
          <table>
            <thead><tr><th>Time</th><th>User</th><th>Change</th><th>Status</th></tr></thead>
            <tbody>
              {#each activities as a, idx (idx)}
                <tr>
                  <td class="muted">{new Date(a.time).toLocaleTimeString()}</td>
                  <td>{a.user}</td>
                  <td class="mono">{a.method} {a.path}{#if a.error}<div class="muted">{a.error}</div>{/if}</td>
                  <td><span class="st" class:succeeded={a.status < 300} class:failed={a.status >= 400}>{a.status}</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
          <a class="more" href="/activity" onclick={router.link}>All activity →</a>
        {/if}
      {:else if activeTab === 'alerts'}
        {#if alerts.active.length === 0}
          <p class="empty">No active alerts.</p>
        {:else}
          {#each alerts.active as a (a.key)}
            <div class="alert" class:crit={a.severity === 'critical'}>
              <span class="sev">{a.severity}</span>
              <span class="text">{a.hostName ? `${a.hostName}: ` : ''}{a.subject} — {a.message}</span>
              {#if a.ackedBy}<span class="muted">acknowledged by {a.ackedBy}</span>
              {:else if session.can('operator')}<button class="ui-btn sm" onclick={() => ack(a.id, a.hostId)}>Acknowledge</button>{/if}
            </div>
          {/each}
        {/if}
      {:else}
        {#if apiLog.calls.length === 0}
          <p class="empty">No API calls recorded yet (the live stream and polling are not listed).</p>
        {:else}
          <table>
            <thead><tr><th>Time</th><th>Call</th><th>Status</th><th>ms</th><th></th></tr></thead>
            <tbody>
              {#each apiLog.calls as c, idx (idx)}
                <tr>
                  <td class="muted">{new Date(c.time).toLocaleTimeString()}</td>
                  <td class="mono">{c.method} {c.path}</td>
                  <td><span class="st" class:succeeded={c.status > 0 && c.status < 300} class:failed={c.status === 0 || c.status >= 400}>{c.status || 'network'}</span></td>
                  <td class="muted">{c.ms}</td>
                  <td><button class="ui-btn sm ghost" onclick={() => copy(asCurl(c))}>Copy as curl</button></td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>
  </section>
{/if}

<style>
  .drawer {
    position: fixed;
    right: 18px;
    bottom: 18px;
    left: calc(var(--sidebar-w) + 18px);
    z-index: 30;
    display: flex;
    flex-direction: column;
    max-height: 46vh;
    border: var(--frame) solid var(--ink);
    background: var(--surface);
    box-shadow: 8px 8px 0 var(--ink);
  }
  header {
    display: flex;
    align-items: flex-end;
    gap: 12px;
    padding: 6px 6px 0 10px;
    border-bottom: var(--frame) solid var(--ink);
    background: #000;
    color: #f4f1e8;
  }
  .tabs {
    display: flex;
    flex: 1;
    gap: 3px;
  }
  .tabs button {
    padding: 4px 10px 5px;
    border: var(--frame) solid #f4f1e8;
    border-bottom: 0;
    background: #000;
    color: #f4f1e8;
    font-family: var(--font-display);
    font-size: 13.5px;
    font-stretch: 80%;
    font-weight: 700;
    cursor: pointer;
  }
  .tabs button.on {
    background: var(--surface);
    color: var(--text);
  }
  .hint {
    align-self: center;
    font-family: var(--font-mono);
    font-size: 11px;
    opacity: 0.7;
  }
  .close {
    display: grid;
    align-self: center;
    place-items: center;
    width: 24px;
    height: 24px;
    margin-bottom: 4px;
    border: var(--frame) solid #f4f1e8;
    background: #000;
    color: #f4f1e8;
    cursor: pointer;
  }
  .close:hover {
    background: var(--cadmium);
  }
  .body {
    overflow: auto;
    padding: 8px 12px 12px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th {
    padding: 4px 6px;
    border-bottom: var(--frame) solid var(--ink);
    font-family: var(--font-display);
    font-stretch: 80%;
    font-weight: 800;
    text-align: left;
  }
  td {
    padding: 5px 6px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .muted {
    color: var(--text-3);
  }
  .st {
    padding: 0 6px;
    border: 1px solid var(--ink);
    background: var(--bg-elev);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .st.succeeded {
    background: var(--ok);
    color: #fff;
  }
  .st.failed {
    background: var(--cadmium);
    color: #fff;
  }
  .st.running {
    background: var(--ochre);
    color: #000;
  }
  .empty {
    margin: 12px 0;
    color: var(--text-3);
  }
  .more {
    display: inline-block;
    margin-top: 8px;
    font-size: 13px;
  }
  .alert {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
  }
  .sev {
    padding: 0 6px;
    border: 1px solid var(--ink);
    background: var(--ochre);
    color: #000;
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .alert.crit .sev {
    background: var(--cadmium);
    color: #fff;
  }
  .text {
    flex: 1;
  }
  @media (max-width: 900px) {
    .drawer {
      left: 8px;
      right: 8px;
      bottom: 8px;
    }
  }
</style>
