<script lang="ts">
  // The strip above the online demo: says the data is made up and where the real thing is.
  import { t } from '../i18n.svelte'
  import { feedbackURL } from '../feedback'
  import megaLogo from './mega-bilgisayar.png'

  const REPO = 'https://github.com/onurbaydas/VirtoMir'
</script>

<div class="demo-banner" role="note">
  <span class="tag" title={t('Simulated hosts and machines. Click anything; nothing here touches a real server, and a reload starts over.')}>{t('Live demo')}</span>
  <span class="what">{t('Simulated hosts and machines. Click anything; nothing here touches a real server, and a reload starts over.')}</span>
  <span class="thanks">{t('Thanks to')} <img src={megaLogo} alt="Mega Bilgisayar" width="96" height="29" /></span>
  <span class="links">
    <a href="{REPO}#install" target="_blank" rel="noopener">{t('Install')}</a>
    <a href="{REPO}/blob/main/docs/try-in-a-vm.md" target="_blank" rel="noopener">{t('Try it in a VM')}</a>
    <a href="{REPO}" target="_blank" rel="noopener">GitHub</a>
    <a href={feedbackURL({}, 'demo')} target="_blank" rel="noopener">{t('Idea / Report a bug')}</a>
  </span>
</div>

<style>
  .demo-banner {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 14px;
    padding: 7px 16px;
    border-bottom: var(--frame) solid var(--ink);
    background: var(--hazard, var(--warn));
    color: #000;
    font-size: 13px;
    line-height: 1.35;
  }
  .tag {
    padding: 1px 7px;
    background: #000;
    color: #f4f1e8;
    font-family: var(--font-display);
    font-weight: 700;
    letter-spacing: 0.02em;
    text-transform: uppercase;
  }
  .what {
    flex: 1 1 320px;
  }
  /* The logo is blue on transparent: on a light chip it reads on the yellow strip. */
  .thanks {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 2px 8px 2px 10px;
    background: #f4f1e8;
    border: 1px solid #000;
    font-size: 12px;
    white-space: nowrap;
  }
  .thanks img {
    display: block;
  }
  .links {
    display: flex;
    gap: 14px;
    font-weight: 700;
  }
  @media (max-width: 600px) {
    .what {
      display: none;
    }
  }
  .links a {
    color: #000;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
</style>
