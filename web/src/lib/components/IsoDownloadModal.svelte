<script lang="ts">
  import { t } from '../i18n.svelte'
  // "Add image": an installer ISO or a disk image into a storage pool, either uploaded from
  // this computer (the upload keeps going on other pages, see uploads.svelte.ts) or fetched
  // by the host from a URL, so nothing passes through the browser; with a SHA-256 checksum a
  // corrupt or tampered download is deleted again.
  import Dialog from './Dialog.svelte'
  import Download from '@lucide/svelte/icons/download'
  import Upload from '@lucide/svelte/icons/upload'
  import { api, errorText, type Pool } from '../api'
  import { ent } from '../enterprise'
  import { formatBytes } from '../format'
  import { track } from '../jobs'
  import { uploads, volumeName } from '../uploads.svelte'

  let {
    open,
    poolName,
    mode: initialMode = 'file',
    onclose,
    onsuccess,
  }: { open: boolean; poolName: string; mode?: 'file' | 'url'; onclose: () => void; onsuccess: () => void } = $props()

  let mode = $state<'file' | 'url'>('file')
  let pools = $state<Pool[]>([])
  let pool = $state('')
  let url = $state('')
  let name = $state('')
  let sha256 = $state('')
  let file = $state<File | null>(null)
  let error = $state('')
  let busy = $state(false)

  $effect(() => {
    if (!open) return
    error = ''
    mode = initialMode
    file = null
    void api
      .pools()
      .then((p) => {
        pools = p.filter((x) => x.active && ['dir', 'netfs', 'fs'].includes(x.type))
        pool = pools.some((x) => x.name === poolName) ? poolName : (pools[0]?.name ?? '')
        if (!pools.length) error = t('No started directory or NFS pool to download into. Create or start one first.')
      })
      .catch((e) => (error = errorText(e)))
  })

  // Name from the URL until the user types one.
  let named = $state(false)
  $effect(() => {
    if (named || mode !== 'url') return
    try {
      name = decodeURIComponent(new URL(url).pathname.split('/').pop() ?? '')
    } catch {
      name = ''
    }
  })

  let shaOK = $derived(sha256.trim() === '' || /^[0-9a-fA-F]{64}$/.test(sha256.trim()))
  let target = $derived(pools.find((p) => p.name === pool))
  // The upload is refused before it starts when the pool cannot hold the file.
  let tooBig = $derived(!!file && !!target && file.size > target.availableBytes)

  async function download() {
    busy = true
    error = ''
    try {
      const job = await ent.download(pool, url.trim(), name.trim(), sha256.trim())
      track(job, { success: t('{name} is in {pool}', { name, pool }), onSuccess: onsuccess })
      onclose()
      url = ''
      sha256 = ''
      named = false
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  function upload() {
    if (!file || tooBig) return
    const f = file
    const target = pool
    const off = uploads.onDone((p) => {
      if (p === target) {
        off()
        onsuccess()
      }
    })
    void uploads.start(target, f, volumeName(f))
    onclose()
  }
</script>

<Dialog {open} title={t('Add an image')} subtitle={t('An installer ISO or a disk image for new machines')} width={560} {onclose}>
  <div class="form">
    <div class="seg" role="radiogroup" aria-label={t('Where the image comes from')}>
      <label class:on={mode === 'file'}><input type="radio" bind:group={mode} value="file" /> <Upload size={14} /> {t('From this computer')}</label>
      <label class:on={mode === 'url'}><input type="radio" bind:group={mode} value="url" /> <Download size={14} /> {t('From a URL')}</label>
    </div>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <label class="ui-field"><span class="label">{t('Storage pool')}</span>
      <select class="ui-input" bind:value={pool}>{#each pools as p (p.name)}<option value={p.name}>{t('{name} — {free} free', { name: p.name, free: formatBytes(p.availableBytes) })}</option>{/each}</select></label>
    {#if mode === 'file'}
      <label class="ui-field"><span class="label">{t('File')}</span>
        <input class="ui-input" type="file" accept=".iso,.img,.raw,.qcow2" onchange={(e) => (file = e.currentTarget.files?.[0] ?? null)} />
        {#if file}<span class="hint">{t('{name}, {size}. The upload keeps going if you open other pages; closing the tab stops it.', { name: volumeName(file), size: formatBytes(file.size) })}</span>{/if}
        {#if tooBig && target}<span class="error">{t('{pool} has only {free} free; the file needs {size}.', { pool: target.name, free: formatBytes(target.availableBytes), size: formatBytes(file?.size ?? 0) })}</span>{/if}
      </label>
    {:else}
      <p class="hint">{t('The host fetches it; progress shows under Tasks.')}</p>
      <label class="ui-field"><span class="label">{t('Address')}</span>
        <input class="ui-input mono" placeholder={t('https://cdimage.debian.org/…/debian-13-netinst.iso')} bind:value={url} /></label>
      <label class="ui-field"><span class="label">{t('Save as')}</span>
        <input class="ui-input mono" bind:value={name} oninput={() => (named = true)} /></label>
      <label class="ui-field"><span class="label">{t('SHA-256 checksum (optional, recommended)')}</span>
        <input class="ui-input mono" placeholder={t('from the download page\'s SHA256SUMS')} bind:value={sha256} />
        {#if !shaOK}<span class="error">{t('64 hex characters')}</span>{/if}</label>
    {/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    {#if mode === 'file'}
      <button class="ui-btn primary" disabled={!file || !pool || tooBig} onclick={upload}><Upload size={14} /> {t('Upload')}</button>
    {:else}
      <button class="ui-btn primary" disabled={busy || !url.trim() || !name.trim() || !pool || !shaOK} onclick={download}><Download size={14} /> {t('Download')}</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .form { display: flex; flex-direction: column; gap: 10px; }
  .error { color: var(--danger); font-size: 12px; }
  .hint { margin: 0; color: var(--text-3); font-size: 12px; }
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
  }
  .seg label {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    height: 30px;
    border-radius: var(--radius-sm);
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .seg input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .seg label.on {
    background: var(--metal);
    outline: 1px solid var(--border-strong);
    color: var(--text);
    box-shadow: var(--bevel);
  }
  .seg label:focus-within {
    outline: 2px solid var(--accent);
  }
</style>
