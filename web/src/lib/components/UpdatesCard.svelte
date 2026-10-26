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

  interface AutoConfig {
    enabled: boolean
    autoInstall: boolean
    intervalHours: number
    lastCheckedAt?: string
    lastUpdatedAt?: string
    lastResult?: string
    lastError?: string
  }

  interface UpdateStatus {
    current: string
    repo: string
    latest?: { tag: string; version: string; name: string; notes: string; url: string; published: string; package?: string }
    available: boolean
    installable: boolean
    reason?: string
    checkedAt: string
    error?: string
    auto?: AutoConfig
  }

  let st = $state<UpdateStatus | null>(null)
  let error = $state('')
  let checking = $state(false)
  let installing = $state(false)
  let savingAuto = $state(false)
  let autoSavedMsg = $state('')

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

  async function saveAutoConfig() {
    if (!st?.auto) return
    savingAuto = true
    try {
      const res = await api.post<AutoConfig>('/api/v1/update/auto', st.auto)
      st.auto = res
      autoSavedMsg = t('Auto-update settings saved')
      setTimeout(() => (autoSavedMsg = ''), 3000)
    } catch (e) {
      error = errorText(e)
    } finally {
      savingAuto = false
    }
  }

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
    <button class="ui-btn sm ghost" disabled={checking} onclick={() => check(true)}><RefreshCw size={13} /> {checking ? t('Checking…') : t('Check now')}</button>
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
          <!-- Always show the button: disabled with the reason when this host cannot install, so it never just "disappears". -->
          <button
            class="ui-btn primary"
            disabled={installing || !st.installable || !session.can('admin')}
            title={!st.installable ? t('Cannot install from here: {reason}', { reason: st.reason }) : session.why('admin')}
            onclick={install}
          ><Download size={14} /> {installing ? t('Starting…') : t('Install {v}', { v: st.latest?.version ?? '' })}</button>
          {#if st.latest?.url}<a class="ui-btn ghost" href={st.latest.url} target="_blank" rel="noopener"><ExternalLink size={13} /> {t('Release notes')}</a>{/if}
        </div>
        {#if !st.installable}<div class="ui-error-box">{t('Cannot install from here: {reason}', { reason: st.reason })}</div>{/if}
        <p class="muted small">{t('In a cluster, update every host (switch host in the sidebar). The download is checked against the release\'s SHA-256; settings and machines are kept.')}</p>
      </div>
    {:else}
      <p class="ok">{t('Up to date.')}</p>
    {/if}
    <p class="muted small">Checked {new Date(st.checkedAt).toLocaleString()}.</p>

    {#if st.auto && session.can('admin')}
      <div class="auto-box">
        <div class="auto-top">
          <div class="auto-meta">
            <span class="badge" class:active={st.auto.enabled}>
              {st.auto.enabled ? t('Automatic updates active') : t('Automatic updates off')}
            </span>
            <span class="auto-title">{t('Automatic GitHub Updates')}</span>
          </div>
          <label class="ui-switch" title={t('Toggle automatic updates')}>
            <input type="checkbox" bind:checked={st.auto.enabled} onchange={saveAutoConfig} disabled={savingAuto} aria-label={t('Toggle automatic updates')} />
          </label>
        </div>

        {#if st.auto.enabled}
          <div class="auto-details">
            <label class="check-row">
              <input type="checkbox" bind:checked={st.auto.autoInstall} onchange={saveAutoConfig} disabled={savingAuto} />
              <span>{t('Automatically install new releases and restart daemon')}</span>
            </label>

            <div class="freq-row">
              <span class="muted small">{t('Check interval:')}</span>
              <select bind:value={st.auto.intervalHours} onchange={saveAutoConfig} disabled={savingAuto} class="ui-input">
                <option value={1}>{t('Every 1 hour')}</option>
                <option value={6}>{t('Every 6 hours')}</option>
                <option value={12}>{t('Every 12 hours')}</option>
                <option value={24}>{t('Every 24 hours')}</option>
              </select>
            </div>

            {#if st.auto.lastResult}
              <p class="muted small">{t('Status: {res}', { res: st.auto.lastResult })}</p>
            {/if}
            {#if st.auto.lastUpdatedAt}
              <p class="muted small">{t('Last auto-upgrade: {time}', { time: new Date(st.auto.lastUpdatedAt).toLocaleString() })}</p>
            {/if}
            {#if st.auto.lastError}
              <p class="err-text small">{t('Last error: {err}', { err: st.auto.lastError })}</p>
            {/if}
          </div>
        {/if}
        {#if autoSavedMsg}
          <p class="ok-toast small">{autoSavedMsg}</p>
        {/if}
      </div>
    {/if}
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

  .auto-box {
    margin-top: 10px;
    border-top: 1px solid var(--border);
    padding-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .auto-top {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .auto-meta {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .badge {
    font-size: 11px;
    font-weight: 600;
    padding: 2px 7px;
    background: var(--bg-elev);
    color: var(--text-2);
  }
  .badge.active {
    background: color-mix(in srgb, var(--ok) 15%, transparent);
    color: var(--ok);
    border: 1px solid color-mix(in srgb, var(--ok) 30%, transparent);
  }
  .auto-title {
    font-size: 13px;
    font-weight: 600;
  }
  .auto-details {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-left: 4px;
  }
  .check-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    cursor: pointer;
  }
  .freq-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 2px;
  }
  .err-text {
    color: var(--danger);
    margin: 0;
  }
  .ok-toast {
    color: var(--ok);
    margin: 0;
  }
</style>
