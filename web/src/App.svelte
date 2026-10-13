<script lang="ts">
  import { onMount } from 'svelte'
  import { live } from './lib/live.svelte'
  import { router } from './lib/router.svelte'
  import { session } from './lib/session.svelte'
  import { alerts } from './lib/alerts.svelte'
  import AlertsPage from './lib/pages/AlertsPage.svelte'
  import BackupsPage from './lib/pages/BackupsPage.svelte'
  import Sidebar from './lib/components/Sidebar.svelte'
  import ConnectionBanner from './lib/components/ConnectionBanner.svelte'
  import Toasts from './lib/components/Toasts.svelte'
  import Overview from './lib/pages/Overview.svelte'
  import MachinePage from './lib/pages/MachinePage.svelte'
  import NewMachine from './lib/pages/NewMachine.svelte'
  import StoragePage from './lib/pages/StoragePage.svelte'
  import NetworksPage from './lib/pages/NetworksPage.svelte'
  import NotFound from './lib/pages/NotFound.svelte'
  import ActivityPage from './lib/pages/ActivityPage.svelte'
  import AccountPage from './lib/pages/AccountPage.svelte'
  import ConsolePage from './lib/pages/ConsolePage.svelte'
  import Login from './lib/pages/Login.svelte'

  let version = $state('')
  onMount(() => {
    void session.check()
    fetch('/api/v1/version')
      .then((r) => r.json())
      .then((v) => (version = v.version))
      .catch(() => {})
  })

  // The live stream needs a session; stop it when signed out so it does not retry on 401s.
  $effect(() => {
    if (session.ready) {
      live.start()
      alerts.start()
    } else {
      live.stop()
      alerts.stop()
    }
  })

  let route = $derived(router.route)
</script>

{#if session.info === null}
  <div class="splash" aria-busy="true"><img src="/favicon.svg" alt="VirtoMir" width="44" height="44" /></div>
{:else if !session.ready}
  <Login />
{:else if route.name === 'console'}
  <ConsolePage uuid={route.uuid} />
{:else}
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
      {:else if route.name === 'new'}
        <NewMachine />
      {:else if route.name === 'storage'}
        <StoragePage pool={route.pool} />
      {:else if route.name === 'networks'}
        <NetworksPage />
      {:else if route.name === 'backups'}
        <BackupsPage />
      {:else if route.name === 'alerts'}
        <AlertsPage tab={route.tab} />
      {:else if route.name === 'activity'}
        <ActivityPage />
      {:else if route.name === 'account'}
        <AccountPage />
      {:else}
        <NotFound />
      {/if}
    </main>
  </div>
{/if}

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
  .splash {
    display: grid;
    place-items: center;
    min-height: 100vh;
  }
  .splash img {
    animation: breathe 1.6s ease-in-out infinite;
  }
  @keyframes breathe {
    50% {
      opacity: 0.4;
    }
  }
  @media (max-width: 900px) {
    .app {
      flex-direction: column;
    }
  }
  @media (max-width: 700px) {
    main {
      padding: 20px 16px 40px;
    }
  }
</style>
