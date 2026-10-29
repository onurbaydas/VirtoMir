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
    /** main: every change merged to main (the edge build); releases: tagged releases only. */
    channel?: 'main' | 'releases'
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

  async function setChannel(c: 'main' | 'releases') {
    if (!st?.auto) return
    st.auto.channel = c
    await saveAutoConfig()
    await check(true)
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
    <span class="head-acts">
      <button class="ui-btn sm ghost" disabled={checking} onclick={() => check(true)}><RefreshCw size={13} /> {checking ? t('Checking…') : t('Check for updates')}</button>
      <!-- Lit only when there is something newer; the reason shows when this host cannot install. -->
      <button
        class="ui-btn sm"
        class:primary={st?.available}
        class:lit={st?.available && st.installable}
        disabled={installing || !st?.available || !st.installable || !session.can('admin')}
        title={st && !st.available ? t('Up to date.') : st && !st.installable ? t('Cannot install from here: {reason}', { reason: st.reason }) : session.why('admin')}
        onclick={install}
      ><Download size={13} /> {installing ? t('Starting…') : st?.available ? t('Update to {v}', { v: st.latest?.tag ?? '' }) : t('Update')}</button>
    </span>
  </header>
  {#if st?.auto && session.can('admin')}
    <div class="channel" role="radiogroup" aria-label={t('Update from')}>
      <label class:on={st.auto.channel !== 'releases'}>
        <input type="radio" name="channel" value="main" checked={st.auto.channel !== 'releases'} onchange={() => setChannel('main')} disabled={savingAuto} />
        <strong>{t('Every change on main')}</strong>
        <span>{t('Built and signed by GitHub for every push to main.')}</span>
      </label>
      <label class:on={st.auto.channel === 'releases'}>
        <input type="radio" name="channel" value="releases" checked={st.auto.channel === 'releases'} onchange={() => setChannel('releases')} disabled={savingAuto} />
        <strong>{t('Releases only')}</strong>
        <span>{t('Only tagged versions (v1.2.0, …).')}</span>
      </label>
    </div>
  {/if}
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if st}
    <p class="line">{t('This host runs')} <b class="mono">{st.current}</b>.
      {#if st.latest}
        {st.auto?.channel === 'releases' ? t('Newest release{v}:', { v: st.repo ? ` on ${st.repo}` : '' }) : t('Newest build of main:')} <b class="mono">{st.latest.tag}</b>{st.latest.published ? ` (${new Date(st.latest.published).toLocaleString()})` : ''}.
      {/if}
    </p>
    {#if st.error}
      <p class="muted">{st.error}</p>
    {:else if st.available}
      <div class="avail">
        <strong>{st.latest?.name || st.latest?.tag} is available.</strong>
        {#if st.latest?.notes}<pre class="notes">{st.latest.notes.slice(0, 1500)}</pre>{/if}
        {#if st.latest?.url}<div class="acts"><a class="ui-btn ghost" href={st.latest.url} target="_blank" rel="noopener"><ExternalLink size={13} /> {t('Release notes')}</a></div>{/if}
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
              <span>{t('Install updates by themselves and restart VirtoMir (waits for running jobs)')}</span>
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
  header { display: flex; flex-wrap: wrap; gap: 8px; justify-content: space-between; align-items: center; }
  .head-acts { display: flex; gap: 6px; }
  .lit { box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 35%, transparent); animation: lit 1.6s ease-in-out infinite; }
  @keyframes lit { 50% { box-shadow: 0 0 0 6px color-mix(in srgb, var(--accent) 15%, transparent); } }
  @media (prefers-reduced-motion: reduce) { .lit { animation: none; } }
  .channel { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 8px; }
  .channel label { display: flex; flex-direction: column; gap: 2px; padding: 8px 10px; border: 1px solid var(--border-strong); border-radius: var(--radius-sm); cursor: pointer; font-size: 12.5px; color: var(--text-2); }
  .channel label.on { border-color: var(--accent); background: var(--surface-2); }
  .channel input { position: absolute; opacity: 0; pointer-events: none; }
  .channel strong { color: var(--text); font-size: 13px; }
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
