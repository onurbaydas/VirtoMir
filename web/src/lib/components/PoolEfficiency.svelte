<script lang="ts">
  import { t } from '../i18n.svelte'
  // A pool's storage tier and what compression and deduplication save on it (ADR-0074):
  // ZFS settings here; Ceph compression on the member that manages the cluster.
  import '../deck.css'
  import { errorText, type Pool } from '../api'
  import { formatBytes } from '../format'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import { TIERS, eff, ratioText, savedPct, type PoolEfficiency, type Tier } from '../storageeff'

  let { pool, tier, ontier }: { pool: Pool; tier?: Tier; ontier: () => void } = $props()
  let admin = $derived(session.can('admin'))

  let info = $state<PoolEfficiency | null>(null)
  let error = $state('')
  $effect(() => {
    info = null
    error = ''
    if (!pool.active || !['zfs', 'rbd'].includes(pool.type)) return
    eff.pool(pool.name).then((x) => (info = x)).catch((e) => (error = errorText(e)))
  })

  async function setTier(v: string) {
    try {
      await eff.setTier(pool.name, v as Tier | '')
      toasts.push({ kind: 'info', title: v ? t('{pool} is tier {tier}', { pool: pool.name, tier: v.toUpperCase() }) : t('Tier removed') })
      ontier()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Tier not changed'), detail: errorText(e) })
    }
  }

  let zfsForm = $state<{ compression: string; dedup: string; ack: boolean } | null>(null)
  let cephForm = $state<{ mode: string; algorithm: string } | null>(null)
  let saving = $state(false)
  async function saveZFS() {
    if (!zfsForm) return
    saving = true
    try {
      const z = await eff.setZFS(pool.name, { compression: zfsForm.compression, dedup: zfsForm.dedup, acknowledgeDedupRam: zfsForm.ack })
      info = { ...info!, zfs: z }
      zfsForm = null
      toasts.push({ kind: 'info', title: t('Saved: applies to data written from now on') })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }
  async function saveCeph() {
    if (!cephForm || !info?.ceph) return
    saving = true
    try {
      await eff.setCephCompression(info.ceph.name, cephForm.mode, cephForm.algorithm)
      info = await eff.pool(pool.name)
      cephForm = null
      toasts.push({ kind: 'info', title: t('Saved: applies to data written from now on') })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }
  let dedupOn = $derived(zfsForm?.dedup === 'on' || zfsForm?.dedup === 'verify')
</script>

<div class="eff">
  <label class="tier">
    <span class="muted">{t('Tier')}</span>
    <select class="ui-input sm" value={tier ?? ''} disabled={!admin} onchange={(e) => setTier(e.currentTarget.value)}>
      <option value="">—</option>
      {#each TIERS as x (x)}<option value={x}>{x.toUpperCase()}</option>{/each}
    </select>
  </label>
  {#if error}
    <span class="muted small">{error}</span>
  {:else if info?.zfs}
    {@const z = info.zfs}
    <span><span class="muted">{t('Compression')}</span> <span class="mono">{z.compression} · {ratioText(z.compressRatio)}</span></span>
    <span><span class="muted">{t('Saved')}</span> <span class="mono">{formatBytes(z.savedBytes)}</span></span>
    <span><span class="muted">{t('Dedup')}</span> <span class="mono">{z.dedup} · {ratioText(z.dedupRatio)}</span></span>
    {#if admin}<button class="ui-btn sm ghost" onclick={() => (zfsForm = { compression: z.compression, dedup: z.dedup, ack: false })}>{t('Settings')}</button>{/if}
  {:else if info?.ceph}
    {@const c = info.ceph}
    <span><span class="muted">{t('Compression')}</span> <span class="mono">{c.compressionMode}{c.compressionAlgorithm ? ' · ' + c.compressionAlgorithm : ''}</span></span>
    {#if c.compressedFromBytes > 0}
      <span><span class="muted">{t('Saved')}</span> <span class="mono">{formatBytes(c.compressedFromBytes - c.compressedBytes)} ({savedPct(c.compressedBytes, c.compressedFromBytes - c.compressedBytes).toFixed(0)} %)</span></span>
    {/if}
    {#if admin}<button class="ui-btn sm ghost" onclick={() => (cephForm = { mode: c.compressionMode, algorithm: c.compressionAlgorithm || 'zstd' })}>{t('Settings')}</button>{/if}
  {:else if info?.note}
    <span class="muted small">{info.note}</span>
  {/if}
</div>

<Dialog open={!!zfsForm} title={t('ZFS settings of {pool}', { pool: pool.name })} subtitle={info?.zfs?.dataset} width={500} onclose={() => (zfsForm = null)}>
  {#if zfsForm}
    <form id="zfs-eff" class="deck-form" onsubmit={(e) => (e.preventDefault(), saveZFS())}>
      <label class="ui-field">
        <span class="label">{t('Compression')}</span>
        <select class="ui-input" bind:value={zfsForm.compression}>
          {#each ['off', 'lz4', 'zstd', 'gzip', 'zle', 'on'] as x (x)}<option value={x}>{x}</option>{/each}
        </select>
        <span class="hint">{t('lz4 is nearly free on the CPU and the usual choice; zstd saves more for some CPU.')}</span>
      </label>
      <label class="ui-field">
        <span class="label">{t('Deduplication')}</span>
        <select class="ui-input" bind:value={zfsForm.dedup}>
          {#each ['off', 'on', 'verify'] as x (x)}<option value={x}>{x}</option>{/each}
        </select>
      </label>
      {#if dedupOn && info?.zfs?.dedup === 'off'}
        <div class="ui-error-box">{t('ZFS deduplication keeps a table of every block in RAM: plan about 1 to 5 GB of host memory per TB of deduplicated data, or writes slow down sharply. It only saves space when machines store the same blocks (many clones of one image); compression usually saves more for less.')}</div>
        <label class="ui-switch"><input type="checkbox" bind:checked={zfsForm.ack} /> {t('This host has the memory for it')}</label>
      {/if}
      <p class="hint">{t('Both apply to data written from now on; what is stored stays as it is.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (zfsForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="zfs-eff" disabled={saving || (dedupOn && info?.zfs?.dedup === 'off' && !zfsForm?.ack)}>{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!cephForm} title={t('Ceph compression of {pool}', { pool: info?.ceph?.name ?? '' })} width={480} onclose={() => (cephForm = null)}>
  {#if cephForm}
    <form id="ceph-eff" class="deck-form" onsubmit={(e) => (e.preventDefault(), saveCeph())}>
      <label class="ui-field">
        <span class="label">{t('Mode')}</span>
        <select class="ui-input" bind:value={cephForm.mode}>
          <option value="none">{t('none')}</option>
          <option value="passive">{t('passive (only data the client marks compressible)')}</option>
          <option value="aggressive">{t('aggressive (all but data marked incompressible)')}</option>
          <option value="force">{t('force (everything)')}</option>
        </select>
      </label>
      {#if cephForm.mode !== 'none'}
        <label class="ui-field">
          <span class="label">{t('Algorithm')}</span>
          <select class="ui-input" bind:value={cephForm.algorithm}>{#each ['zstd', 'lz4', 'snappy', 'zlib'] as x (x)}<option value={x}>{x}</option>{/each}</select>
        </label>
      {/if}
      <p class="hint">{t('BlueStore compresses on the OSDs; it applies to data written from now on.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (cephForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="ceph-eff" disabled={saving}>{t('Save')}</button>
  {/snippet}
</Dialog>

<style>
  .eff {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 18px;
    padding: 8px 0 12px;
    font-size: 13px;
  }
  .tier {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .tier select {
    height: 28px;
    width: auto;
    padding: 0 6px;
  }
  .muted {
    color: var(--text-3);
  }
  .small {
    font-size: 12px;
  }
</style>
