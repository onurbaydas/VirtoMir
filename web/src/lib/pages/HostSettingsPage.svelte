<script lang="ts">
  import { t } from '../i18n.svelte'
  // Host settings: what an admin sets once for this host (updates, sign-in, quotas,
  // integrations, the first-run setup). The personal things stay on the Account page.
  import Wrench from '@lucide/svelte/icons/wrench'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import QuotasCard from '../components/QuotasCard.svelte'
  import SigninCard from '../components/SigninCard.svelte'
  import IntegrationsCard from '../components/IntegrationsCard.svelte'
  import UpdatesCard from '../components/UpdatesCard.svelte'
</script>

<svelte:head><title>{t('Host settings · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Host settings')}</h1>
    <p class="lead">{t('Updates, sign-in, quotas and integrations of this host.')}</p>
  </div>
</header>

{#if !session.can('admin')}
  <div class="ui-panel card">
    <p class="hint">{t('Only admins can change the host settings.')}</p>
  </div>
{:else}
  <section class="ui-panel card setup">
    <span class="icon"><Wrench size={18} /></span>
    <div>
      <h2>{t('Host setup')}</h2>
      <p class="hint">{t('Checks what this host needs for machines (virtualization, a storage pool, a network, the backup folder) and fixes it in one click.')}</p>
    </div>
    <a class="ui-btn sm" href="/setup" onclick={router.link}>{t('Run the host setup')}</a>
  </section>
  <UpdatesCard />
  <SigninCard />
  <QuotasCard />
  <IntegrationsCard />
{/if}

<style>
  .lead,
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .card {
    padding: 16px 18px;
    margin-bottom: 16px;
  }
  .setup {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .setup > div {
    flex: 1;
    min-width: 0;
  }
  .setup h2 {
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 600;
  }
  .icon {
    display: grid;
    place-items: center;
    flex: none;
    width: 40px;
    height: 40px;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--accent);
  }
  @media (max-width: 640px) {
    .setup {
      flex-wrap: wrap;
    }
  }
</style>
