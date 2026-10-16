<script lang="ts">
  import { onMount } from 'svelte'
  import { live } from './lib/live.svelte'
  import { router } from './lib/router.svelte'
  import { session } from './lib/session.svelte'
  import { alerts } from './lib/alerts.svelte'
  import { cluster } from './lib/cluster.svelte'
  import ClusterPage from './lib/pages/ClusterPage.svelte'
  import ImportPage from './lib/pages/ImportPage.svelte'
  import HardwarePage from './lib/pages/HardwarePage.svelte'
  import AlertsPage from './lib/pages/AlertsPage.svelte'
  import BackupsPage from './lib/pages/BackupsPage.svelte'
  import Sidebar from './lib/components/Sidebar.svelte'
  import ConnectionBanner from './lib/components/ConnectionBanner.svelte'
  import Toasts from './lib/components/Toasts.svelte'
  import DeployDialog from './lib/components/DeployDialog.svelte'
  import Overview from './lib/pages/Overview.svelte'
  import MachinesPage from './lib/pages/MachinesPage.svelte'
  import MachinePage from './lib/pages/MachinePage.svelte'
  import NewMachine from './lib/pages/NewMachine.svelte'
  import StoragePage from './lib/pages/StoragePage.svelte'
  import NetworksPage from './lib/pages/NetworksPage.svelte'
  import NotFound from './lib/pages/NotFound.svelte'
  import ActivityPage from './lib/pages/ActivityPage.svelte'
  import AccountPage from './lib/pages/AccountPage.svelte'
  import ReportsPage from './lib/pages/ReportsPage.svelte'
  import ConsolePage from './lib/pages/ConsolePage.svelte'
  import Login from './lib/pages/Login.svelte'
  import CommandPalette from './lib/components/CommandPalette.svelte'
  import BottomDrawer from './lib/components/BottomDrawer.svelte'
  import GlobalTopBar from './lib/components/GlobalTopBar.svelte'
  import { theme } from './lib/theme.svelte'

  let version = $state('')
  onMount(() => {
    void session.check()
    fetch('/api/v1/version')
      .then((r) => r.json())
      .then((v) => (version = v.version))
      .catch(() => {})

    function onKey(e: KeyboardEvent) {
      if (e.key.toLowerCase() === 't' && !['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName ?? '')) {
        theme.set(theme.resolved === 'dark' ? 'light' : 'dark')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // The live stream needs a session; stop it when signed out so it does not retry on 401s.
  $effect(() => {
    if (session.ready) {
      live.start()
      alerts.start()
      cluster.start()
      void session.loadQuota()
    } else {
      live.stop()
      alerts.stop()
      cluster.stop()
    }
  })

  // Once it is known that this host is in a cluster, alerts come from every member: start
  // over so the ones already open there are not announced as new.
  let wasClustered = false
  $effect(() => {
    const c = cluster.clustered
    if (c === wasClustered) return
    wasClustered = c
    if (session.ready) alerts.reset()
  })

  // Another cluster member: start the live view and alerts over for it.
  let shownHost = router.host
  $effect(() => {
    const h = router.host
    if (h === shownHost) return
    shownHost = h
    if (!session.ready) return
    live.restart()
    alerts.reset()
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
  <div class="app" data-page={route.name}>
    <Sidebar />

    <div class="deck-workspace">
      <GlobalTopBar />

      <main>
        <ConnectionBanner />

        {#if route.name === 'overview'}
          <Overview />
        {:else if route.name === 'machines'}
          <MachinesPage />
        {:else if route.name === 'machine'}
          {#key route.uuid}
            <MachinePage uuid={route.uuid} />
          {/key}
        {:else if route.name === 'new'}
          <NewMachine />
        {:else if route.name === 'import'}
          <ImportPage />
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
        {:else if route.name === 'hardware'}
          <HardwarePage />
        {:else if route.name === 'reports'}
          <ReportsPage />
        {:else if route.name === 'cluster'}
          <ClusterPage />
        {:else}
          <NotFound />
        {/if}
      </main>
    </div>
  </div>
{/if}

<DeployDialog />
<Toasts />
<CommandPalette />
<BottomDrawer />

<style>
  .app {
    display: flex;
    min-height: 100vh;
  }
  .deck-workspace {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 100vh;
  }
  main {
    flex: 1;
    min-width: 0;
    max-width: 1440px;
    padding: 34px 36px 72px;
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
