<script lang="ts">
  import { session } from '../session.svelte'
  import { live } from '../live.svelte'
  import { router } from '../router.svelte'
  import Plus from '@lucide/svelte/icons/plus'
  import Download from '@lucide/svelte/icons/download'
  import Boxes from '@lucide/svelte/icons/boxes'
  import MachineList from '../components/MachineList.svelte'
  import { isActive } from '../live.svelte'

  let domains = $derived(live.snapshot?.domains ?? [])
  let running = $derived(domains.filter((d) => isActive(d.state)))
  let paused = $derived(domains.filter((d) => d.state === 'paused'))
  let stopped = $derived(domains.filter((d) => !isActive(d.state) && d.state !== 'paused'))
</script>

<svelte:head><title>Virtual Machines · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <div class="title-row">
      <Boxes size={22} />
      <h1>Virtual Machines</h1>
    </div>
    <div class="chips mono">
      <span class="chip on" title="Running machines">● {running.length} RUNNING</span>
      {#if paused.length}
        <span class="chip paused" title="Paused machines">⏸ {paused.length} PAUSED</span>
      {/if}
      <span class="chip" title="Stopped machines">○ {stopped.length} STOPPED</span>
      <span class="chip mono" title="Total defined guests">{domains.length} TOTAL</span>
    </div>
  </div>
  <div class="head-right">
    <a class="ui-btn" href="/machines/import" onclick={router.link} title="Import OVA or disk image">
      <Download size={14} /> Import image
    </a>
    {#if session.canCreate}
      <a class="ui-btn primary" href="/machines/new" onclick={router.link}>
        <Plus size={15} /> New machine
      </a>
    {/if}
  </div>
</header>

<MachineList />

<style>
  .title-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 4px;
  }
  .title-row h1 {
    margin: 0;
  }
  .head-right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .chips {
    display: flex;
    gap: 6px;
    margin-top: 6px;
  }
  .chip {
    padding: 2px 7px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 11px;
    color: var(--text-2);
  }
  .chip.on {
    color: var(--ok);
    border-color: color-mix(in srgb, var(--ok) 40%, transparent);
    background: color-mix(in srgb, var(--ok) 10%, var(--surface));
  }
  .chip.paused {
    color: var(--warn);
    border-color: color-mix(in srgb, var(--warn) 40%, transparent);
  }
</style>
