<script lang="ts">
  import { t } from '../i18n.svelte'
  // Off-site copies of backups with restic (ADR-0043): one encrypted, deduplicated repository
  // on S3 (or anything S3-compatible), B2, Azure, Google Cloud Storage, SFTP, a REST server or a
  // directory. Scheduled backups can be copied right after they are made.
  import CloudUpload from '@lucide/svelte/icons/cloud-upload'
  import CloudDownload from '@lucide/svelte/icons/cloud-download'
  import Plus from '@lucide/svelte/icons/plus'
  import X from '@lucide/svelte/icons/x'
  import { api, errorText, OFFSITE_SECRET, type OffsiteConfig, type OffsiteSnapshot } from '../api'
  import { formatBytes } from '../format'
  import { track } from '../jobs'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let { localIds, onchange, onready }: { localIds: Set<string>; onchange: () => void; onready?: (ready: boolean) => void } = $props()

  let admin = $derived(session.can('admin'))
  let cfg = $state<OffsiteConfig | null>(null)
  let restic = $state<string | undefined>(undefined)
  let configured = $state(false)
  let error = $state('')
  let snaps = $state<OffsiteSnapshot[] | null>(null)
  let snapErr = $state('')
  let editing = $state(false)

  // Credentials as rows, so keys can be added and removed.
  let env = $state<{ k: string; v: string }[]>([])

  async function load() {
    try {
      const r = await api.offsite()
      cfg = r.config
      restic = r.status.restic
      configured = r.status.configured
      env = Object.entries(r.config.env ?? {}).map(([k, v]) => ({ k, v }))
      error = ''
      onready?.(configured && !!restic)
      if (configured && restic) void loadSnaps()
    } catch (e) {
      error = errorText(e)
      onready?.(false)
    }
  }
  async function loadSnaps() {
    try {
      snaps = await api.offsiteSnapshots()
      snapErr = ''
    } catch (e) {
      snaps = null
      snapErr = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  // Which credentials a repository type needs.
  const CREDS: Record<string, string[]> = {
    s3: ['AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY'],
    b2: ['B2_ACCOUNT_ID', 'B2_ACCOUNT_KEY'],
    azure: ['AZURE_ACCOUNT_NAME', 'AZURE_ACCOUNT_KEY'],
    gs: ['GOOGLE_PROJECT_ID', 'GOOGLE_APPLICATION_CREDENTIALS'],
  }
  let scheme = $derived(cfg?.repository.match(/^([a-z0-9]+):/)?.[1] ?? '')
  $effect(() => {
    for (const k of CREDS[scheme] ?? []) if (!env.some((e) => e.k === k)) env.push({ k, v: '' })
  })
  const EXAMPLES = [
    ['S3 / MinIO / Wasabi', 's3:https://s3.eu-central-1.amazonaws.com/my-bucket/virtomir'],
    ['Backblaze B2', 'b2:my-bucket:virtomir'],
    ['SFTP', 'sftp:backup@nas.lan:/srv/restic/virtomir'],
    ['REST server', 'rest:https://backup.lan:8000/virtomir'],
    ['Disk', '/mnt/usb/restic'],
  ]

  let busy = $state('')
  async function save(e?: SubmitEvent) {
    e?.preventDefault()
    if (!cfg) return
    busy = 'save'
    try {
      const body: OffsiteConfig = { ...$state.snapshot(cfg), env: Object.fromEntries(env.filter((x) => x.k.trim()).map((x) => [x.k.trim(), x.v])) }
      const r = await api.saveOffsite(body)
      cfg = r.config
      configured = r.status.configured
      restic = r.status.restic
      env = Object.entries(r.config.env ?? {}).map(([k, v]) => ({ k, v }))
      toasts.push({ kind: 'info', title: t('Off-site settings saved') })
      editing = false
      onready?.(configured && !!restic)
      if (configured && restic) void loadSnaps()
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(err) })
    } finally {
      busy = ''
    }
  }
  async function check(init: boolean) {
    busy = init ? 'init' : 'check'
    try {
      const r = await api.checkOffsite(init)
      toasts.push({ kind: 'info', title: r.message })
      void loadSnaps()
    } catch (err) {
      toasts.push({ kind: 'error', title: init ? 'Could not create the repository' : t('Repository not reachable'), detail: errorText(err) })
    } finally {
      busy = ''
    }
  }
  async function bringBack(s: OffsiteSnapshot) {
    try {
      const job = await api.offsiteRestore(s.id)
      track(job, { success: t('{v} is back in the backup location', { v: s.machine || s.backup }), onSuccess: onchange })
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Could not bring it back'), detail: errorText(err) })
    }
  }
  const when = (t: string) => new Date(t).toLocaleString()
  let groups = $derived.by(() => {
    const m = new Map<string, OffsiteSnapshot[]>()
    for (const s of snaps ?? []) m.set(s.domainUuid, [...(m.get(s.domainUuid) ?? []), s])
    return [...m.values()]
  })
</script>

<section class="ui-panel card">
  <header class="head">
    <h2><CloudUpload size={17} /> {t('Off-site copies')}</h2>
    {#if admin && cfg && !editing}<button class="ui-btn sm" onclick={() => (editing = true)}>{configured ? 'Settings' : 'Set up'}</button>{/if}
  </header>
  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if cfg}
    {#if !restic}
      <p class="warn">{t('restic is not installed on this host. Install it (')}<span class="mono">{t('apt install restic')}</span> {t('or')} <span class="mono">{t('dnf install restic')}</span>{t(') to copy backups off-site.')}</p>
    {/if}
    {#if !editing}
      {#if configured}
        <p class="muted">
          {t('To')} <span class="mono">{cfg.repository}</span>{t('{v}{v2}. Encrypted with the repository password — without it the copies cannot be read.', { v: cfg.afterScheduled ? ' · every scheduled backup is copied' : ' · copies on request', v2: cfg.keepLast || cfg.keepDaily || cfg.keepWeekly || cfg.keepMonthly
            ? ` · keeps ${[cfg.keepLast && `last ${cfg.keepLast}`, cfg.keepDaily && `${cfg.keepDaily} daily`, cfg.keepWeekly && `${cfg.keepWeekly} weekly`, cfg.keepMonthly && `${cfg.keepMonthly} monthly`].filter(Boolean).join(', ')} per machine`
            : ' · keeps everything' })}
        </p>
      {:else}
        <p class="muted">{t('Not set up. A backup next to the machine does not survive a fire or a stolen server: copy backups to S3, B2, SFTP or a disk elsewhere.')}</p>
      {/if}
    {:else}
      <form class="form" onsubmit={save}>
        <label class="ui-field">
          <span class="label">{t('Repository')}</span>
          <input class="ui-input mono" bind:value={cfg.repository} placeholder={t('s3:https://s3.example.com/bucket/virtomir')} required />
          <span class="hint">
            {#each EXAMPLES as [l, v] (l)}<button type="button" class="ex" onclick={() => cfg && (cfg.repository = v)}>{l}</button>{/each}
          </span>
        </label>
        <label class="ui-field">
          <span class="label">{t('Repository password')}</span>
          <input class="ui-input mono" type="password" bind:value={cfg.password} autocomplete="new-password" required />
          <span class="hint">{t('Encrypts every copy. Keep it somewhere else too — without it nothing can be restored.')}</span>
        </label>
        <fieldset class="env">
          <legend class="label">{t('Credentials')} <span class="muted">{t('(stay on this host)')}</span></legend>
          {#each env as row, i (i)}
            <div class="env-row">
              <input class="ui-input mono" bind:value={row.k} placeholder={t('AWS_ACCESS_KEY_ID')} />
              <input class="ui-input mono" type="password" bind:value={row.v} placeholder={row.v === OFFSITE_SECRET ? '' : 'value'} autocomplete="off" />
              <button type="button" class="ui-btn sm ghost" aria-label={t('Remove')} onclick={() => env.splice(i, 1)}><X size={13} /></button>
            </div>
          {/each}
          <button type="button" class="ui-btn sm ghost add" onclick={() => env.push({ k: '', v: '' })}><Plus size={13} /> {t('Variable')}</button>
        </fieldset>
        <label class="check"><input type="checkbox" bind:checked={cfg.afterScheduled} /> {t('Copy every scheduled backup off-site right after it is made')}</label>
        <div class="keep">
          <span class="label">{t('Keep per machine')}</span>
          <label>{t('last')} <input class="ui-input mono" type="number" min="0" bind:value={cfg.keepLast} /></label>
          <label>{t('daily')} <input class="ui-input mono" type="number" min="0" bind:value={cfg.keepDaily} /></label>
          <label>{t('weekly')} <input class="ui-input mono" type="number" min="0" bind:value={cfg.keepWeekly} /></label>
          <label>{t('monthly')} <input class="ui-input mono" type="number" min="0" bind:value={cfg.keepMonthly} /></label>
          <span class="hint">{t('All 0: keep every copy.')}</span>
        </div>
        <label class="ui-field narrow">
          <span class="label">{t('Upload limit')} <span class="muted">{t('KiB/s, 0 = none')}</span></span>
          <input class="ui-input mono" type="number" min="0" bind:value={cfg.limitUploadKiB} />
        </label>
        <div class="actions">
          <button class="ui-btn primary" disabled={!!busy}>{busy === 'save' ? 'Saving…' : 'Save'}</button>
          <button type="button" class="ui-btn ghost" onclick={() => { editing = false; void load() }}>{t('Cancel')}</button>
        </div>
      </form>
    {/if}
    {#if configured && admin && !editing && restic}
      <div class="actions">
        <button class="ui-btn sm" disabled={!!busy} onclick={() => check(false)}>{busy === 'check' ? 'Testing…' : t('Test connection')}</button>
        <button class="ui-btn sm ghost" disabled={!!busy} onclick={() => check(true)}>{busy === 'init' ? 'Creating…' : t('Create repository')}</button>
      </div>
    {/if}
    {#if snapErr}
      <div class="ui-error-box">{snapErr}</div>
    {:else if snaps && snaps.length === 0}
      <p class="muted">{t('No copies yet. Use "Copy off-site" on a backup below{v}.', { v: cfg.afterScheduled ? ', or wait for the next scheduled backup' : '' })}</p>
    {:else if snaps}
      {#each groups as g (g[0].domainUuid)}
        <div class="grp">
          <div class="g-head"><b>{g[0].machine || g[0].domainUuid}</b> <span class="muted">{g.length} {g.length === 1 ? 'copy' : 'copies'}</span></div>
          {#each g as s (s.id)}
            <div class="snap">
              <span class="mono">{s.shortId}</span>
              <span>{when(s.time)}</span>
              <span class="muted">{s.sizeBytes ? formatBytes(s.sizeBytes) : ''}{s.host ? ` · from ${s.host}` : ''}</span>
              {#if localIds.has(s.backup)}
                <span class="muted small">{t('also here')}</span>
              {:else}
                <button class="ui-btn sm" disabled={!admin} title={session.why('admin') || t('Download it into the backup location; then restore it as a machine')} onclick={() => bringBack(s)}>
                  <CloudDownload size={13} /> {t('Bring back')}
                </button>
              {/if}
            </div>
          {/each}
        </div>
      {/each}
    {/if}
  {/if}
</section>

<style>
  .card { padding: 14px 16px; margin-bottom: 14px; }
  .head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
  h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .muted { color: var(--text-3); font-size: 12.5px; }
  .small { font-size: 12px; }
  .warn {
    padding: 10px 12px;
    background: var(--surface-2);
    border-left: 3px solid var(--warn);
    color: var(--text-2);
    font-size: 12.5px;
  }
  .form { display: flex; flex-direction: column; gap: 12px; max-width: 640px; margin: 8px 0; }
  .ex { background: none; border: 0; padding: 0 8px 0 0; color: var(--accent); cursor: pointer; font: inherit; font-size: 12px; text-decoration: underline; }
  .env { border: 0; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .env-row { display: grid; grid-template-columns: 1fr 1fr auto; gap: 6px; }
  .add { align-self: flex-start; }
  .check { display: flex; gap: 8px; align-items: center; font-size: 13px; }
  .keep { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; font-size: 13px; }
  .keep input { width: 64px; }
  .narrow { max-width: 220px; }
  .actions { display: flex; gap: 8px; margin: 8px 0; }
  .grp { margin-top: 10px; }
  .g-head { font-size: 13px; margin-bottom: 4px; }
  .snap { display: grid; grid-template-columns: 80px 1fr 1fr auto; gap: 10px; align-items: center; font-size: 12.5px; padding: 4px 0; border-top: 1px solid var(--border); }
  @media (max-width: 560px) {
    .snap { grid-template-columns: 1fr auto; }
    .env-row { grid-template-columns: 1fr; }
  }
</style>
