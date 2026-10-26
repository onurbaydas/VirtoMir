<script lang="ts">
  import { t } from '../i18n.svelte'
  // Fetch an image (installer ISO, cloud image) from a URL straight into a storage pool. The
  // host downloads it, so nothing passes through the browser; with a SHA-256 checksum a
  // corrupt or tampered download is deleted again.
  import Dialog from './Dialog.svelte'
  import Download from '@lucide/svelte/icons/download'
  import { api, errorText, type Pool } from '../api'
  import { ent } from '../enterprise'
  import { track } from '../jobs'

  let { open, poolName, onclose, onsuccess }: { open: boolean; poolName: string; onclose: () => void; onsuccess: () => void } = $props()

  let pools = $state<Pool[]>([])
  let pool = $state('')
  let url = $state('')
  let name = $state('')
  let sha256 = $state('')
  let error = $state('')
  let busy = $state(false)

  $effect(() => {
    if (!open) return
    error = ''
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
    if (named) return
    try {
      name = decodeURIComponent(new URL(url).pathname.split('/').pop() ?? '')
    } catch {
      name = ''
    }
  })

  let shaOK = $derived(sha256.trim() === '' || /^[0-9a-fA-F]{64}$/.test(sha256.trim()))

  async function start() {
    busy = true
    error = ''
    try {
      const job = await ent.download(pool, url.trim(), name.trim(), sha256.trim())
      track(job, { success: `${name} is in ${pool}`, onSuccess: onsuccess })
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
</script>

<Dialog {open} title={t('Download an image into a pool')} subtitle={t('The host fetches it; progress shows under Tasks')} width={560} {onclose}>
  <div class="form">
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <label class="ui-field"><span class="label">{t('Address')}</span>
      <input class="ui-input mono" placeholder={t('https://cdimage.debian.org/…/debian-13-netinst.iso')} bind:value={url} /></label>
    <label class="ui-field"><span class="label">{t('Save as')}</span>
      <input class="ui-input mono" bind:value={name} oninput={() => (named = true)} /></label>
    <label class="ui-field"><span class="label">{t('Storage pool')}</span>
      <select class="ui-input" bind:value={pool}>{#each pools as p (p.name)}<option value={p.name}>{p.name} ({p.type})</option>{/each}</select></label>
    <label class="ui-field"><span class="label">{t('SHA-256 checksum (optional, recommended)')}</span>
      <input class="ui-input mono" placeholder="from the download page's SHA256SUMS" bind:value={sha256} />
      {#if !shaOK}<span class="error">{t('64 hex characters')}</span>{/if}</label>
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={busy || !url.trim() || !name.trim() || !pool || !shaOK} onclick={start}><Download size={14} /> {t('Download')}</button>
  {/snippet}
</Dialog>

<style>
  .form { display: flex; flex-direction: column; gap: 10px; }
  .error { color: var(--danger); font-size: 12px; }
</style>
