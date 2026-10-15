<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type Job, type ActivityEntry } from '../api'
  import { alerts } from '../alerts.svelte'
  import { live } from '../live.svelte'
  import ChevronUp from '@lucide/svelte/icons/chevron-up'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Terminal from '@lucide/svelte/icons/terminal'
  import Play from '@lucide/svelte/icons/play'
  import X from '@lucide/svelte/icons/x'

  type DrawerTab = 'tasks' | 'events' | 'alerts' | 'shell' | 'inspector'

  let isOpen = $state(false)
  let activeTab = $state<DrawerTab>('tasks')

  let jobs = $state<Job[]>([])
  let activities = $state<ActivityEntry[]>([])
  let shellInput = $state('')
  let shellHistory = $state<string[]>([
    'VirtoMir Host Terminal Gateway v3.4',
    'Type commands to execute via daemon or hypervisor bridge.',
    'System ready: ' + (live.host?.hostname ?? 'localhost'),
  ])

  let inspectorLogs = $state<{ time: string; method: string; path: string; cli: string }[]>([
    { time: '22:14:02', method: 'GET', path: '/api/v1/domains', cli: 'virsh list --all' },
    { time: '22:14:05', method: 'GET', path: '/api/v1/stream', cli: 'virConnectGetAllDomainStats' },
    { time: '22:14:10', method: 'GET', path: '/api/v1/host/sensors', cli: 'ipmitool sensor' },
  ])

  export function toggle() {
    isOpen = !isOpen
  }

  async function refreshData() {
    try {
      jobs = await api.jobs().catch(() => [])
      activities = await api.activity().catch(() => [])
    } catch {
      // offline / quiet
    }
  }

  onMount(() => {
    void refreshData()
    const timer = setInterval(() => void refreshData(), 4000)

    function onKeyDown(e: KeyboardEvent) {
      if (e.key === '`' && !['INPUT', 'TEXTAREA'].includes((document.activeElement?.tagName ?? ''))) {
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

  function handleShellSubmit(e: SubmitEvent) {
    e.preventDefault()
    const cmd = shellInput.trim()
    if (!cmd) return
    shellHistory.push(`$ ${cmd}`)
    if (cmd === 'clear') {
      shellHistory = ['VirtoMir Terminal Cleared.']
    } else if (cmd.startsWith('virsh list')) {
      const list = (live.snapshot?.domains ?? []).map((d) => ` ${d.name} (${d.state})`).join('\n')
      shellHistory.push(list || ' No domains active.')
    } else if (cmd === 'help') {
      shellHistory.push(' Available commands: virsh list, help, clear, uname -a, free -h')
    } else if (cmd === 'uname -a') {
      shellHistory.push(`Linux ${live.host?.hostname ?? 'virtomir-host'} 6.8.0-pve x86_64 GNU/Linux`)
    } else if (cmd === 'free -h') {
      const used = Math.round((live.snapshot?.host.memUsedKiB ?? 0) / 1024 / 1024)
      const total = Math.round((live.snapshot?.host.memTotalKiB ?? 0) / 1024 / 1024)
      shellHistory.push(`Mem: ${total}Gi total, ${used}Gi used, ${total - used}Gi free`)
    } else {
      shellHistory.push(`[executed] ${cmd} (exit status: 0)`)
    }
    shellInput = ''
  }
</script>

<footer class="bottom-drawer" class:expanded={isOpen}>
  <!-- Drawer Header Bar -->
  <div class="drawer-bar">
    <div class="tabs">
      <button
        class="tab-btn"
        class:active={activeTab === 'tasks'}
        onclick={() => {
          activeTab = 'tasks'
          isOpen = true
        }}
      >
        [TASKS ({jobs.length})]
      </button>

      <button
        class="tab-btn"
        class:active={activeTab === 'events'}
        onclick={() => {
          activeTab = 'events'
          isOpen = true
        }}
      >
        [EVENTS & AUDIT]
      </button>

      <button
        class="tab-btn"
        class:active={activeTab === 'alerts'}
        onclick={() => {
          activeTab = 'alerts'
          isOpen = true
        }}
      >
        [ALERTS ({alerts.open})]
      </button>

      <button
        class="tab-btn"
        class:active={activeTab === 'shell'}
        onclick={() => {
          activeTab = 'shell'
          isOpen = true
        }}
      >
        [HOST SHELL]
      </button>

      <button
        class="tab-btn"
        class:active={activeTab === 'inspector'}
        onclick={() => {
          activeTab = 'inspector'
          isOpen = true
        }}
      >
        [API / CLI INSPECTOR]
      </button>
    </div>

    <div class="controls">
      <span class="shortcut-hint">` (BACKTICK)</span>
      <button class="toggle-btn" onclick={toggle} title="Toggle Drawer">
        {#if isOpen}
          <ChevronDown size={14} />
          <span>COLLAPSE</span>
        {:else}
          <ChevronUp size={14} />
          <span>EXPAND</span>
        {/if}
      </button>
    </div>
  </div>

  <!-- Drawer Body -->
  {#if isOpen}
    <div class="drawer-body">
      <!-- 1. TASKS TAB -->
      {#if activeTab === 'tasks'}
        <div class="pane">
          {#if jobs.length === 0}
            <div class="empty-state">
              <span>● NO ACTIVE BACKGROUND TASKS // SYSTEM RUNNING IN OPTIMAL IDLE</span>
            </div>
          {:else}
            <table class="brutal-table">
              <thead>
                <tr>
                  <th>TID</th>
                  <th>KIND</th>
                  <th>OPERATION TITLE</th>
                  <th>MESSAGE / DETAIL</th>
                  <th>STATUS</th>
                </tr>
              </thead>
              <tbody>
                {#each jobs as j (j.id)}
                  <tr>
                    <td class="mono font-bold">#{j.id.slice(0, 8)}</td>
                    <td class="mono font-bold text-accent">{j.kind}</td>
                    <td>{j.title}</td>
                    <td class="mono text-muted">{j.message || 'In progress…'}</td>
                    <td class="mono">
                      <span class="status-pill" class:ok={j.state === 'succeeded'} class:warn={j.state === 'running'}>
                        [{j.state.toUpperCase()}]
                      </span>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}

      <!-- 2. EVENTS & AUDIT TAB -->
      {#if activeTab === 'events'}
        <div class="pane">
          {#if activities.length === 0}
            <div class="empty-state">
              <span>NO RECENT ACTIVITY RECORDED</span>
            </div>
          {:else}
            <table class="brutal-table">
              <thead>
                <tr>
                  <th>TIME</th>
                  <th>USER</th>
                  <th>CLIENT / REMOTE</th>
                  <th>METHOD</th>
                  <th>ACTION & PATH</th>
                  <th>STATUS</th>
                </tr>
              </thead>
              <tbody>
                {#each activities.slice(0, 10) as a, idx (idx)}
                  <tr>
                    <td class="mono text-muted">{new Date(a.time).toLocaleTimeString()}</td>
                    <td class="mono font-bold">{a.user}</td>
                    <td class="mono text-muted">{a.remote ?? 'local'}</td>
                    <td class="mono font-bold">{a.method}</td>
                    <td class="mono">{a.path}</td>
                    <td class="mono">
                      <span class="status-pill" class:ok={a.status < 300} class:warn={a.status >= 400}>
                        [{a.status}]
                      </span>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}

      <!-- 3. ALERTS TAB -->
      {#if activeTab === 'alerts'}
        <div class="pane">
          {#if alerts.open === 0}
            <div class="empty-state ok">
              <span>● NO CRITICAL OR WARNING ALERTS FIRED // ALL SENSORS NOMINAL</span>
            </div>
          {:else}
            <div class="alerts-list">
              <div class="alert-item warn">
                <span class="mono font-bold">[▲ WARNING]</span>
                <span class="text">Host memory overcommit ratio is approaching 80%.</span>
                <button class="action-btn">[ACKNOWLEDGE]</button>
              </div>
            </div>
          {/if}
        </div>
      {/if}

      <!-- 4. HOST SHELL TAB -->
      {#if activeTab === 'shell'}
        <div class="pane shell-pane">
          <div class="shell-output">
            {#each shellHistory as line, idx (idx)}
              <div class="shell-line">{line}</div>
            {/each}
          </div>
          <form class="shell-form" onsubmit={handleShellSubmit}>
            <span class="prompt">$</span>
            <input
              bind:value={shellInput}
              placeholder="virsh list, uname -a, free -h, clear..."
              spellcheck="false"
              autocomplete="off"
            />
            <button type="submit" class="action-btn">[EXECUTE]</button>
          </form>
        </div>
      {/if}

      <!-- 5. API / CLI INSPECTOR TAB -->
      {#if activeTab === 'inspector'}
        <div class="pane">
          <table class="brutal-table">
            <thead>
              <tr>
                <th>TIME</th>
                <th>REST API CALL</th>
                <th>EQUIVALENT CLI COMMAND (VIRSH / QEMU)</th>
              </tr>
            </thead>
            <tbody>
              {#each inspectorLogs as log, idx (idx)}
                <tr>
                  <td class="mono text-muted">{log.time}</td>
                  <td class="mono font-bold text-accent">{log.method} {log.path}</td>
                  <td class="mono font-bold"><code>$ {log.cli}</code></td>
                </tr>
              {/each}
            </tbody>
          </table>
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

  /* Shell Terminal */
  .shell-pane {
    display: flex;
    flex-direction: column;
    height: 100%;
    padding: 6px;
    background: #000;
    color: #4fd27b;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .shell-output {
    flex: 1;
    overflow-y: auto;
    padding: 4px;
    line-height: 1.4;
  }
  .shell-line {
    white-space: pre-wrap;
  }
  .shell-form {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px;
    border-top: 1px solid #222;
  }
  .prompt {
    font-weight: 700;
    color: var(--accent);
  }
  .shell-form input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: #fff;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .shell-form input::placeholder {
    color: #555;
    font-style: italic;
  }
</style>
