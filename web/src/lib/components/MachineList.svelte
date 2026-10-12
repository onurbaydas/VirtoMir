<script lang="ts">
  import Search from '@lucide/svelte/icons/search'
  import MonitorOff from '@lucide/svelte/icons/monitor-off'
  import { isActive, live } from '../live.svelte'
  import MachineRow from './MachineRow.svelte'

  type Filter = 'all' | 'running' | 'stopped'
  let filter = $state<Filter>('all')
  let query = $state('')

  let domains = $derived(live.snapshot?.domains ?? [])
  let counts = $derived({
    all: domains.length,
    running: domains.filter((d) => isActive(d.state) || d.state === 'paused').length,
    stopped: domains.filter((d) => !isActive(d.state) && d.state !== 'paused').length,
  })
  let shown = $derived(
    domains.filter((d) => {
      const on = isActive(d.state) || d.state === 'paused'
      if (filter === 'running' && !on) return false
      if (filter === 'stopped' && on) return false
      const q = query.trim().toLowerCase()
      return !q || d.name.toLowerCase().includes(q) || d.uuid.startsWith(q)
    }),
  )
  let loading = $derived(live.snapshot === null)
</script>

<section class="panel">
  <header class="head">
    <h2>Machines</h2>
    {#if counts.all > 0}
      <div class="tabs" role="tablist">
        {#each [['all', 'All'], ['running', 'Running'], ['stopped', 'Stopped']] as [key, label] (key)}
          <button
            role="tab"
            aria-selected={filter === key}
            class:active={filter === key}
            onclick={() => (filter = key as Filter)}
          >
            {label}<span class="count num">{counts[key as Filter]}</span>
          </button>
        {/each}
      </div>
      <label class="search">
        <Search size={15} />
        <input type="search" placeholder="Find by name or UUID" bind:value={query} spellcheck="false" />
      </label>
    {/if}
  </header>

  <div class="table" role="table" aria-label="Virtual machines">
    <div class="colhead" role="row">
      <span role="columnheader">Name</span>
      <span role="columnheader">State</span>
      <span role="columnheader">CPUs</span>
      <span role="columnheader">Memory</span>
      <span role="columnheader">CPU usage · 60 s</span>
      <span role="columnheader" class="sr">Actions</span>
    </div>

    {#if loading}
      {#each [0, 1, 2] as i (i)}
        <div class="sk-row"><span class="sk" style:width="{180 - i * 30}px"></span></div>
      {/each}
    {:else if shown.length === 0}
      <div class="empty">
        <MonitorOff size={28} strokeWidth={1.5} />
        {#if counts.all === 0}
          <strong>No virtual machines on this host yet</strong>
          <span>Machines created with virt-manager or virsh appear here instantly.</span>
        {:else}
          <strong>Nothing matches</strong>
          <button class="link" onclick={() => ((filter = 'all'), (query = ''))}>Show all machines</button>
        {/if}
      </div>
    {:else}
      {#each shown as d (d.uuid)}
        <MachineRow {d} />
      {/each}
    {/if}
  </div>
</section>

<style>
  .panel {
    --cols: minmax(200px, 1.6fr) 150px 80px 120px minmax(140px, 1.2fr) 190px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 14px 16px;
    flex-wrap: wrap;
  }
  h2 {
    margin: 0 8px 0 0;
    font-size: 16px;
    font-weight: 600;
  }
  .tabs {
    display: flex;
    gap: 2px;
    padding: 3px;
    background: var(--surface-2);
    border-radius: 9px;
  }
  .tabs button {
    display: flex;
    align-items: center;
    gap: 7px;
    height: 28px;
    padding: 0 11px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    transition:
      background var(--dur-fast),
      color var(--dur-fast);
  }
  .tabs button:hover {
    color: var(--text);
  }
  .tabs button.active {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .count {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .search {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
    width: min(280px, 100%);
    height: 34px;
    padding: 0 11px;
    border: 1px solid var(--border);
    border-radius: 9px;
    background: var(--bg-elev);
    color: var(--text-3);
    transition: border-color var(--dur-fast);
  }
  .search:focus-within {
    border-color: var(--accent);
  }
  .search input {
    flex: 1;
    min-width: 0;
    border: 0;
    background: transparent;
    outline: none;
    font-size: 13.5px;
  }
  .colhead {
    display: grid;
    grid-template-columns: var(--cols);
    gap: 16px;
    padding: 8px 16px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .sk-row {
    display: flex;
    align-items: center;
    height: 64px;
    padding: 0 16px;
    border-top: 1px solid var(--border);
  }
  .sk {
    height: 14px;
    border-radius: 6px;
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 48px 16px 56px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    margin-top: 6px;
    color: var(--text);
    font-weight: 600;
  }
  .link {
    border: 0;
    background: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0;
  }

  @media (max-width: 1100px) {
    .panel {
      --cols: minmax(180px, 1.5fr) 140px 110px minmax(120px, 1fr) 170px;
    }
    .colhead > :nth-child(3),
    .panel :global(.row > :nth-child(3)) {
      display: none;
    }
  }
  @media (max-width: 760px) {
    .panel {
      --cols: minmax(0, 1fr) auto;
    }
    .colhead {
      display: none;
    }
    .panel :global(.row) {
      padding: 10px 16px;
      row-gap: 8px;
    }
    .panel :global(.row > :nth-child(3)),
    .panel :global(.row > :nth-child(4)),
    .panel :global(.row > :nth-child(5)) {
      display: none;
    }
    .panel :global(.row > :nth-child(6)) {
      grid-column: 1 / -1;
      justify-content: flex-start;
    }
  }
</style>
