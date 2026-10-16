<script lang="ts">
  import { t } from '../i18n.svelte'
  // Updates (ADR-0036): the newest release on GitHub and, on hosts that run the .deb
  // package, a button that installs it (checked against the release's SHA-256).
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Download from '@lucide/svelte/icons/download'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import { api, errorText } from '../api'
  import { session } from '../session.svelte'
  import { track } from '../jobs'

  interface UpdateStatus {
    current: string
    repo: string
    latest?: { tag: string; version: string; name: string; notes: string; url: string; published: string; package?: string }
    available: boolean
    installable: boolean
    reason?: string
    checkedAt: string
    error?: string
  }

  let st = $state<UpdateStatus | null>(null)
  let error = $state('')
  let checking = $state(false)
  let installing = $state(false)

  async function check(refresh = false) {
    checking = true
    try {
      st = await api.get<UpdateStatus>(`/api/v1/update${refresh ? '?refresh=1' : ''}`)
      error = ''
    } catch (e) {
      error = errorText(e)
    } finally {
      checking = false
    }
  }
  $effect(() => {
    void check()
  })

  async function install() {
    installing = true
    try {
      const job = await api.post('/api/v1/update/install')
      track(job, { success: t('Update started: VirtoMir restarts in a few seconds'), onSuccess: () => setTimeout(() => location.reload(), 15000) })
    } catch (e) {
      error = errorText(e)
    } finally {
      installing = false
    }
  }
</script>

<section class="ui-panel card">
  <header>
    <h2>{t('Updates')}</h2>
    <button class="ui-btn sm ghost" disabled={checking} onclick={() => check(true)}><RefreshCw size={13} /> {checking ? 'Checking…' : t('Check now')}</button>
  </header>
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if st}
    <p class="line">{t('This host runs')} <b class="mono">{st.current}</b>.
      {#if st.latest}
        {t('Newest release{v}:', { v: st.repo ? ` on ${st.repo}` : '' })} <b class="mono">{st.latest.version}</b>{st.latest.published ? ` (${new Date(st.latest.published).toLocaleDateString()})` : ''}.
      {/if}
    </p>
    {#if st.error}
      <p class="muted">{st.error}</p>
    {:else if st.available}
      <div class="avail">
        <strong>{st.latest?.name || st.latest?.tag} is available.</strong>
        {#if st.latest?.notes}<pre class="notes">{st.latest.notes.slice(0, 1500)}</pre>{/if}
        <div class="acts">
          {#if st.installable && session.can('admin')}
            <button class="ui-btn primary" disabled={installing} onclick={install}><Download size={14} /> {installing ? 'Starting…' : `Install ${st.latest?.version}`}</button>
          {/if}
          {#if st.latest?.url}<a class="ui-btn ghost" href={st.latest.url} target="_blank" rel="noopener"><ExternalLink size={13} /> {t('Release notes')}</a>{/if}
        </div>
        {#if !st.installable}<p class="muted">{t('Cannot install from here: {reason}', { reason: st.reason })}</p>{/if}
        <p class="muted small">{t('In a cluster, update every host (switch host in the sidebar). The download is checked against the release\'s SHA-256; settings and machines are kept.')}</p>
      </div>
    {:else}
      <p class="ok">{t('Up to date.')}</p>
    {/if}
    <p class="muted small">Checked {new Date(st.checkedAt).toLocaleString()}.</p>
  {/if}
</section>

<style>
  .card { padding: 16px; display: flex; flex-direction: column; gap: 8px; margin-bottom: 14px; }
  header { display: flex; justify-content: space-between; align-items: center; }
  h2 { margin: 0; font-size: 15px; }
  .line { margin: 0; }
  .avail { border-left: 3px solid var(--accent); padding-left: 12px; display: flex; flex-direction: column; gap: 8px; }
  .notes { white-space: pre-wrap; font-size: 12px; max-height: 220px; overflow: auto; background: var(--bg-elev); padding: 8px; margin: 0; }
  .acts { display: flex; gap: 8px; }
  .ok { color: var(--ok); margin: 0; }
  .muted { color: var(--text-2); margin: 0; }
  .small { font-size: 12px; }
</style>
