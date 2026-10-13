<script lang="ts">
  // Pop-out console: the whole window is the machine's screen.
  import { live } from '../live.svelte'
  import ConsolePanel from '../components/ConsolePanel.svelte'

  let { uuid }: { uuid: string } = $props()
  let d = $derived(live.snapshot?.domains.find((x) => x.uuid === uuid))
</script>

<svelte:head><title>{d ? `${d.name} · console` : 'Console · VirtoMir'}</title></svelte:head>

{#if d}
  <ConsolePanel {d} standalone />
{:else if live.snapshot}
  <p class="msg">This machine does not exist (any more).</p>
{:else}
  <p class="msg">Connecting…</p>
{/if}

<style>
  .msg {
    display: grid;
    place-items: center;
    height: 100vh;
    margin: 0;
    color: var(--text-3);
  }
</style>
