<script lang="ts">
  import { t } from '../i18n.svelte'
  import Compass from '@lucide/svelte/icons/compass'
  import { router } from '../router.svelte'
</script>

<svelte:head><title>{t('Not found · VirtoMir')}</title></svelte:head>

<div class="nf">
  <Compass size={32} strokeWidth={1.5} />
  <strong>{t('This page does not exist')}</strong>
  <a href="/" onclick={router.link}>{t('Back to the overview')}</a>
</div>

<style>
  .nf {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 96px 16px;
    color: var(--text-3);
    text-align: center;
  }
  strong {
    color: var(--text);
    font-size: 16px;
    font-weight: 600;
  }
  a {
    color: var(--accent);
    text-decoration: none;
  }
</style>
