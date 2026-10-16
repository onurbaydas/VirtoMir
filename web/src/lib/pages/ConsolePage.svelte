<script lang="ts">
  import { t } from '../i18n.svelte'
  // Pop-out console: the whole window is the machine's screen.
  import { live } from '../live.svelte'
  import ConsolePanel from '../components/ConsolePanel.svelte'

  let { uuid, head = 0 }: { uuid: string; head?: number } = $props()
  let d = $derived(live.snapshot?.domains.find((x) => x.uuid === uuid))
</script>

<svelte:head><title>{d ? `${d.name} · console` : t('Console · VirtoMir')}</title></svelte:head>

{#if d}
  <ConsolePanel {d} {head} standalone />
{:else if live.snapshot}
  <p class="msg">{t('This machine does not exist (any more).')}</p>
{:else}
  <p class="msg">{t('Connecting…')}</p>
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
