<script lang="ts">
  // The last changes on this host (who did what, how long ago), for the overview.
  import { onMount } from 'svelte'
  import History from '@lucide/svelte/icons/history'
  import { api, type ActivityEntry } from '../api'
  import { describe } from '../activity'
  import { ago } from '../format'
  import { t } from '../i18n.svelte'
  import { live } from '../live.svelte'
  import { router } from '../router.svelte'

  let entries = $state<ActivityEntry[] | null>(null)
  let now = $state(Date.now())
  let names = $derived(new Map((live.snapshot?.domains ?? []).map((d) => [d.uuid, d.name])))

  async function load() {
    try {
      // Changes only: sign-ins and reads are not news.
      entries = (await api.activity({ limit: 40 })).filter((e) => e.method !== 'GET' && !e.path.startsWith('/api/v1/auth/')).slice(0, 10)
    } catch {
      entries = []
    }
    now = Date.now()
  }
  onMount(() => {
    void load()
    const id = setInterval(load, 15000)
    return () => clearInterval(id)
  })
</script>

<section class="ui-panel recent">
  <header>
    <h2><History size={15} /> {t('Recent changes')}</h2>
    <a href="/activity" onclick={router.link}>{t('All activity')}</a>
  </header>
  {#if entries && entries.length === 0}
    <p class="muted">{t('Nothing changed lately.')}</p>
  {:else}
    <ul>
      {#each entries ?? [] as e (e.time + e.path)}
        {@const d = describe(e, names)}
        <li class:failed={e.status >= 400}>
          <span class="who">{e.user || t('someone')}</span>
          <span class="what">{d.before}{#if d.machineName}<strong>{d.machineName}</strong>{/if}{d.after}</span>
          <span class="when">{ago(now - Date.parse(e.time))}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .recent {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px 16px;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
  }
  header a {
    font-size: 12.5px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: 8px;
    padding: 6px 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
  }
  li.failed .what {
    color: var(--danger);
  }
  .who {
    font-weight: 600;
  }
  .what {
    overflow-wrap: anywhere;
  }
  .when,
  .muted {
    color: var(--text-3);
    font-size: 12px;
    white-space: nowrap;
  }
</style>
