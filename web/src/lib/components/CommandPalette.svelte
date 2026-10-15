<script lang="ts">
  import { onMount } from 'svelte'
  import { live, isActive } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { theme } from '../theme.svelte'
  import { toasts } from '../toast.svelte'
  import { api, errorText } from '../api'
  import Search from '@lucide/svelte/icons/search'
  import Terminal from '@lucide/svelte/icons/terminal'
  import Monitor from '@lucide/svelte/icons/monitor'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Network from '@lucide/svelte/icons/network'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw'

  let isOpen = $state(false)
  let query = $state('')
  let selectedIndex = $state(0)
  let inputEl = $state<HTMLInputElement | null>(null)

  export function open() {
    isOpen = true
    query = ''
    selectedIndex = 0
    setTimeout(() => inputEl?.focus(), 50)
  }

  export function close() {
    isOpen = false
  }

  onMount(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        if (isOpen) close()
        else open()
      } else if (e.key === 'Escape' && isOpen) {
        close()
      }
    }
    function onCustomOpen() {
      open()
    }
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('open-command-palette', onCustomOpen)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('open-command-palette', onCustomOpen)
    }
  })

  interface PaletteItem {
    id: string
    title: string
    category: 'VM' | 'PAGE' | 'COMMAND'
    detail: string
    badge?: string
    run: () => void | Promise<void>
  }

  let domains = $derived(live.snapshot?.domains ?? [])

  let allItems = $derived.by((): PaletteItem[] => {
    const list: PaletteItem[] = []

    // 1. Pages
    const pages = [
      { path: '/', title: 'Overview', detail: 'Datacenter & Host Telemetry' },
      { path: '/machines', title: 'Virtual Machines', detail: 'Guest Workload Inventory & Fleet Management' },
      { path: '/storage', title: 'Storage Pools', detail: 'Disks, Datastores, ISOs & Volumes' },
      { path: '/networks', title: 'Virtual Networks', detail: 'Bridges, VLANs & Virtual Switches' },
      { path: '/hardware', title: 'Hardware & Devices', detail: 'PCI Passthrough, GPUs & Host Devices' },
      { path: '/backups', title: 'Backups & Schedules', detail: 'CBT Backups, Targets & Retention' },
      { path: '/alerts', title: 'System Alerts', detail: 'Open Alerts, Rules & Notifications' },
      { path: '/activity', title: 'Activity & Audit Log', detail: 'User actions & changing API logs' },
      { path: '/cluster', title: 'Cluster Overview', detail: 'Multi-host federation and node status' },
      { path: '/account', title: 'Account & Quotas', detail: 'Roles, API tokens and sign-in config' },
      { path: '/machines/new', title: 'New Virtual Machine', detail: 'Step-by-step VM creation wizard' },
    ]
    for (const p of pages) {
      list.push({
        id: `page-${p.path}`,
        title: p.title,
        category: 'PAGE',
        detail: p.detail,
        run: () => {
          router.navigate(p.path)
          close()
        },
      })
    }

    // 2. Virtual Machines
    for (const d of domains) {
      const active = isActive(d.state)
      list.push({
        id: `vm-${d.uuid}`,
        title: d.name,
        category: 'VM',
        detail: `State: ${d.state} · ${d.vcpus} vCPU · ${Math.round(d.memoryKiB / 1024)} MB`,
        badge: active ? 'ONLINE' : 'STOPPED',
        run: () => {
          router.navigate(machineHref(d.uuid))
          close()
        },
      })

      // Action Commands
      if (!active) {
        list.push({
          id: `cmd-start-${d.uuid}`,
          title: `> start ${d.name}`,
          category: 'COMMAND',
          detail: `Power on virtual machine ${d.name}`,
          badge: 'ACTION',
          run: () => {
            close()
            live.act(d, 'start')
          },
        })
      } else {
        list.push({
          id: `cmd-reboot-${d.uuid}`,
          title: `> reboot ${d.name}`,
          category: 'COMMAND',
          detail: `ACPI reboot signal to ${d.name}`,
          badge: 'ACTION',
          run: () => {
            close()
            live.act(d, 'reboot')
          },
        })
        list.push({
          id: `cmd-stop-${d.uuid}`,
          title: `> shutdown ${d.name}`,
          category: 'COMMAND',
          detail: `Clean ACPI shutdown of ${d.name}`,
          badge: 'ACTION',
          run: () => {
            close()
            live.act(d, 'shutdown')
          },
        })
      }
    }

    // 3. Theme toggles
    list.push({
      id: 'theme-dark',
      title: '> theme dark',
      category: 'COMMAND',
      detail: 'Switch to Matte Obsidian dark theme',
      badge: 'THEME',
      run: () => {
        theme.set('dark')
        close()
      },
    })
    list.push({
      id: 'theme-light',
      title: '> theme light',
      category: 'COMMAND',
      detail: 'Switch to Warm Limestone light theme',
      badge: 'THEME',
      run: () => {
        theme.set('light')
        close()
      },
    })

    return list
  })

  let filteredItems = $derived.by(() => {
    const q = query.trim().toLowerCase()
    if (!q) return allItems.slice(0, 15)
    return allItems
      .filter((i) => i.title.toLowerCase().includes(q) || i.detail.toLowerCase().includes(q) || i.category.toLowerCase().includes(q))
      .slice(0, 15)
  })

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      selectedIndex = (selectedIndex + 1) % Math.max(1, filteredItems.length)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      selectedIndex = (selectedIndex - 1 + filteredItems.length) % Math.max(1, filteredItems.length)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const item = filteredItems[selectedIndex]
      if (item) void item.run()
    }
  }

  $effect(() => {
    // Reset selection index when query changes
    if (query) selectedIndex = 0
  })
</script>

{#if isOpen}
  <div class="backdrop" onclick={close} role="presentation">
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div
      class="palette"
      role="dialog"
      aria-modal="true"
      aria-label="Command Palette"
      tabindex="-1"
      onclick={(e) => e.stopPropagation()}
    >
      <header class="bar">
        <span class="prompt">>_</span>
        <input
          bind:this={inputEl}
          bind:value={query}
          onkeydown={onKey}
          placeholder="SEARCH RESOURCES OR TYPE COMMAND (> start, > shutdown, > theme)..."
          spellcheck="false"
          autocomplete="off"
        />
        <button type="button" class="esc" onclick={close} aria-label="Close palette">ESC</button>
      </header>

      <ul class="results" role="listbox">
        {#each filteredItems as item, idx (item.id)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            class="item"
            class:active={idx === selectedIndex}
            role="option"
            aria-selected={idx === selectedIndex}
            onmouseenter={() => (selectedIndex = idx)}
            onclick={() => void item.run()}
          >
            <span class="cat-badge">{item.category}</span>
            <div class="text">
              <span class="title">{item.title}</span>
              <span class="detail">{item.detail}</span>
            </div>
            {#if item.badge}
              <span class="tag-badge" class:online={item.badge === 'ONLINE'} class:action={item.badge === 'ACTION'}>
                [{item.badge}]
              </span>
            {/if}
          </li>
        {:else}
          <li class="empty">No matching resources or commands.</li>
        {/each}
      </ul>

      <footer class="footer">
        <span><kbd>↑</kbd><kbd>↓</kbd> Navigate</span>
        <span><kbd>↵</kbd> Select</span>
        <span><kbd>ESC</kbd> Close</span>
        <span class="count">{filteredItems.length} results</span>
      </footer>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 9999;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: 80px;
    background: rgba(0, 0, 0, 0.65);
    animation: fade-in 100ms var(--ease-out);
  }
  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  .palette {
    width: 100%;
    max-width: 640px;
    border: var(--frame) solid var(--border-strong);
    background: var(--surface);
    box-shadow: 6px 6px 0px var(--ink);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
  }
  .prompt {
    font-family: var(--font-mono);
    font-weight: 700;
    font-size: 14px;
    color: var(--accent);
  }
  input {
    flex: 1;
    min-width: 0;
    background: transparent;
    border: none;
    outline: none;
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 13px;
  }
  input::placeholder {
    color: var(--text-3);
    font-style: italic;
    font-size: 11px;
  }
  .esc {
    padding: 2px 6px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: 10px;
    cursor: pointer;
  }
  .results {
    margin: 0;
    padding: 4px 0;
    list-style: none;
    max-height: 380px;
    overflow-y: auto;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    cursor: pointer;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 40%, transparent);
    transition: background 40ms;
  }
  .item.active {
    background: var(--surface-hover);
    border-left: 3px solid var(--accent);
  }
  .cat-badge {
    padding: 1px 5px;
    border: 1px solid var(--border);
    background: var(--bg-elev);
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: 9.5px;
    font-weight: 700;
  }
  .text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .title {
    font-family: var(--font-mono);
    font-weight: 600;
    font-size: 13px;
    color: var(--text);
  }
  .detail {
    font-size: 11px;
    color: var(--text-3);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tag-badge {
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
    color: var(--text-3);
  }
  .tag-badge.online {
    color: var(--ok);
  }
  .tag-badge.action {
    color: var(--accent);
  }
  .empty {
    padding: 24px;
    text-align: center;
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 14px;
    background: var(--bg-elev);
    border-top: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--text-3);
  }
  kbd {
    padding: 1px 4px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-2);
    font-size: 9.5px;
    margin-right: 3px;
  }
  .count {
    color: var(--text-2);
    font-weight: 600;
  }
</style>
