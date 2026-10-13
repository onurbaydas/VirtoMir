<script lang="ts">
  import { onMount } from 'svelte'
  import { live } from './lib/live.svelte'
  import { router } from './lib/router.svelte'
  import Sidebar from './lib/components/Sidebar.svelte'
  import ConnectionBanner from './lib/components/ConnectionBanner.svelte'
  import Toasts from './lib/components/Toasts.svelte'
  import Overview from './lib/pages/Overview.svelte'
  import MachinePage from './lib/pages/MachinePage.svelte'
  import NotFound from './lib/pages/NotFound.svelte'

  let version = $state('')
  onMount(() => {
    live.start()
    fetch('/api/v1/version')
      .then((r) => r.json())
      .then((v) => (version = v.version))
      .catch(() => {})
  })

  let route = $derived(router.route)
</script>

<div class="app">
  <Sidebar {version} />

  <main>
    <ConnectionBanner />

    {#if route.name === 'overview'}
      <Overview />
    {:else if route.name === 'machine'}
      {#key route.uuid}
        <MachinePage uuid={route.uuid} />
      {/key}
    {:else}
      <NotFound />
    {/if}
  </main>
</div>

<Toasts />

<style>
  .app {
    display: flex;
    min-height: 100vh;
  }
  main {
    flex: 1;
    min-width: 0;
    max-width: 1440px;
    padding: 28px 32px 48px;
  }
  @media (max-width: 700px) {
    main {
      padding: 20px 16px 40px;
    }
  }
</style>
