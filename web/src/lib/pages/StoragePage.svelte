<script lang="ts">
  import { t } from '../i18n.svelte'
  import ObjectXmlDialog from '../components/ObjectXmlDialog.svelte'
  import FileCode from '@lucide/svelte/icons/file-code'
  let xmlPool = $state<string | null>(null)
  import { session } from '../session.svelte'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Plus from '@lucide/svelte/icons/plus'
  import Upload from '@lucide/svelte/icons/upload'
  import FolderPlus from '@lucide/svelte/icons/folder-plus'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Trash from '@lucide/svelte/icons/trash'
  import Disc from '@lucide/svelte/icons/disc'
  import Maximize2 from '@lucide/svelte/icons/maximize-2'
  import { api, errorText, poolFills, type Pool, type PoolSource, type PoolSpec, type Volume } from '../api'
  import { track } from '../jobs'
  import NetworkStorageFinder from '../components/NetworkStorageFinder.svelte'
  import DatastoreBrowser from '../components/DatastoreBrowser.svelte'
  import { cluster } from '../cluster.svelte'
  import { formatBytes, plural } from '../format'
  import { router } from '../router.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Download from '@lucide/svelte/icons/download'
  import OrphanedDisksModal from '../components/OrphanedDisksModal.svelte'
  import IsoDownloadModal from '../components/IsoDownloadModal.svelte'
  import { uploads } from '../uploads.svelte'
  import ZfsReplicationModal from '../components/ZfsReplicationModal.svelte'
  import BookOpen from '@lucide/svelte/icons/book-open'
  import Cable from '@lucide/svelte/icons/cable'
  import PoolEfficiency from '../components/PoolEfficiency.svelte'
  import NvmeDialog from '../components/NvmeDialog.svelte'
  import ActionMenu, { type MenuItem } from '../components/ActionMenu.svelte'
  import { eff, type Tier } from '../storageeff'

  let { pool: selected }: { pool: string } = $props()

  let pools = $state<Pool[] | null>(null)
  let poolsError = $state('')
  let volumes = $state<Volume[] | null>(null)
  let orphanedOpen = $state(false)
  let isoOpen = $state(false)
  let zfsOpen = $state(false)
  let nvmeOpen = $state(false)
  // Storage tiers (ADR-0074): pool → nvme/ssd/hdd, the same on every member.
  let tiers = $state<Record<string, Tier>>({})
  const loadTiers = () => eff.tiers().then((x) => (tiers = x)).catch(() => {})
  let volError = $state('')

  let current = $derived(pools?.find((p) => p.name === selected) ?? pools?.find((p) => p.name === 'default') ?? pools?.[0])

  async function loadPools() {
    try {
      pools = await api.pools()
      poolsError = ''
    } catch (e) {
      poolsError = errorText(e)
    }
  }
  async function loadVolumes(name: string) {
    try {
      volumes = await api.volumes(name)
      volError = ''
    } catch (e) {
      volumes = []
      volError = errorText(e)
    }
  }
  $effect(() => {
    void loadPools()
    void loadTiers()
  })
  let currentName = $derived(current?.name)
  let currentActive = $derived(current?.active)
  $effect(() => {
    volumes = null
    if (currentName && currentActive) void loadVolumes(currentName)
    else if (currentName) volumes = []
  })
  async function refresh() {
    await loadPools()
    if (current?.active) await loadVolumes(current.name)
  }

  async function poolAction(p: Pool, action: 'start' | 'stop' | 'refresh') {
    try {
      await api.poolAction(p.name, action)
      await refresh()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not {action} {name}', { action: action, name: p.name }), detail: errorText(e) })
    }
  }
  async function toggleAutostart(p: Pool, on: boolean) {
    try {
      await api.poolAutostart(p.name, on)
      await loadPools()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Autostart not changed'), detail: errorText(e) })
    }
  }

  // ---- New volume -----------------------------------------------------------------
  let volDlg = $state(false)
  let vName = $state('')
  let vSize = $state(20)
  let vFormat = $state<'qcow2' | 'raw'>('qcow2')
  let vProvisioning = $state<'thin' | 'thick'>('thin')
  let vBusy = $state(false)
  let vErr = $state('')
  function openVolume() {
    vName = ''
    vSize = 20
    vFormat = 'qcow2'
    vProvisioning = 'thin'
    vErr = ''
    volDlg = true
  }
  async function createVolume(e: SubmitEvent) {
    e.preventDefault()
    if (!current) return
    vBusy = true
    vErr = ''
    try {
      const name = /\.(qcow2|img|raw)$/i.test(vName) ? vName : `${vName}.${vFormat === 'qcow2' ? 'qcow2' : 'img'}`
      await api.createVolume(current.name, name, vSize, vFormat, vProvisioning)
      volDlg = false
      toasts.push({ kind: 'info', title: `${name} created` })
      await refresh()
    } catch (err) {
      vErr = errorText(err)
    } finally {
      vBusy = false
    }
  }

  // ---- Grow -----------------------------------------------------------------------
  let growVol = $state<Volume | null>(null)
  let growGiB = $state(0)
  let growErr = $state('')
  let growBusy = $state(false)
  function openGrow(v: Volume) {
    growVol = v
    growGiB = Math.ceil(v.capacityBytes / 2 ** 30) + 10
    growErr = ''
  }
  async function grow(e: SubmitEvent) {
    e.preventDefault()
    if (!current || !growVol) return
    growBusy = true
    growErr = ''
    try {
      await api.resizeVolume(current.name, growVol.name, growGiB)
      toasts.push({ kind: 'info', title: t('{name} is now {growGiB} GiB', { name: growVol.name, growGiB: growGiB }) })
      growVol = null
      await refresh()
    } catch (err) {
      growErr = errorText(err)
    } finally {
      growBusy = false
    }
  }

  // ---- Upload ---------------------------------------------------------------------
  // Uploads run in the uploads store (they survive leaving this page); a finished one refreshes the list.
  let isoMode = $state<'file' | 'url'>('file')
  const addImage = (mode: 'file' | 'url') => ((isoMode = mode), (isoOpen = true))
  $effect(() => uploads.onDone((p) => p === current?.name && void refresh()))

  // ---- New pool -------------------------------------------------------------------
  let poolDlg = $state(false)
  let pName = $state('')
  let pPath = $state('')
  let pType = $state<'carve' | 'dir' | 'netfs' | 'iscsi' | 'rbd' | 'logical' | 'zfs'>('dir')
  let pSource = $state('')
  let pNewVG = $state(false)
  let pDevices = $state('')
  let pTarget = $state('')
  let pMonitors = $state('')
  let pCephPool = $state('')
  let pCephUser = $state('libvirt')
  let pCephKey = $state('')
  let pHost = $state('')
  let pExport = $state('')
  let pEverywhere = $state(true)
  // Disks named by path are this host's own; never offer to format the same path everywhere.
  let localDisks = $derived((pType === 'logical' && pNewVG) || pType === 'carve')
  // From free space: a pool of a chosen size out of an empty disk, a volume group or a zpool.
  let sources = $state<PoolSource[] | null>(null)
  let pSourceKey = $state('')
  let pSizeGiB = $state(1)
  const sourceKey = (s: PoolSource) => `${s.kind}:${s.id}`
  let carveSrc = $derived(sources?.find((s) => sourceKey(s) === pSourceKey))
  let maxGiB = $derived(carveSrc ? Math.floor(carveSrc.freeBytes / 2 ** 30) : 1)
  function sourceLabel(s: PoolSource): string {
    const v = { id: s.id, detail: s.detail ?? '', size: formatBytes(s.sizeBytes), free: formatBytes(s.freeBytes) }
    if (s.kind === 'disk') return t('Empty disk {id} ({detail}, {size})', v)
    if (s.kind === 'vg') return t('LVM volume group {id}: {free} free of {size}', v)
    return t('ZFS pool {id}: {free} free of {size}', v)
  }
  function pickSource(key: string) {
    pSourceKey = key
    pSizeGiB = maxGiB
  }
  async function openPoolDlg() {
    pName = ''
    pPath = ''
    pErr = ''
    sources = null
    poolDlg = true
    try {
      sources = await api.poolSources()
    } catch {
      sources = []
    }
    if (sources.length) {
      pType = 'carve'
      pickSource(sourceKey(sources[0]))
    }
  }
  async function carvePool() {
    if (!carveSrc) return
    const name = pName
    const job = await api.carvePool({ name, kind: carveSrc.kind, source: carveSrc.id, sizeBytes: pSizeGiB * 2 ** 30 })
    poolDlg = false
    track(job, {
      success: t('{pName} created', { pName: name }),
      onSuccess: () => void loadPools().then(() => router.navigate(`/storage#${encodeURIComponent(name)}`, { replace: true })),
    })
  }
  let pBusy = $state(false)
  let pErr = $state('')
  async function createPool(e: SubmitEvent) {
    e.preventDefault()
    pBusy = true
    pErr = ''
    if (pType === 'carve') {
      try {
        await carvePool()
      } catch (err) {
        pErr = errorText(err)
      } finally {
        pBusy = false
      }
      return
    }
    const spec: PoolSpec =
      pType === 'netfs'
        ? { name: pName, path: pPath, type: 'netfs', host: pHost.trim(), export: pExport.trim() }
        : pType === 'iscsi'
          ? { name: pName, path: '', type: 'iscsi', host: pHost.trim(), target: pTarget.trim() }
          : pType === 'rbd'
            ? {
                name: pName,
                path: '',
                type: 'rbd',
                monitors: pMonitors.split(/[\s,]+/).filter(Boolean),
                cephPool: pCephPool.trim(),
                cephUser: pCephUser.trim(),
                cephKey: pCephKey.trim(),
              }
            : pType === 'logical'
              ? { name: pName, path: '', type: 'logical', source: pSource.trim(), devices: pNewVG ? pDevices.split(/[\s,]+/).filter(Boolean) : [] }
              : pType === 'zfs'
                ? { name: pName, path: '', type: 'zfs', source: pSource.trim() }
                : { name: pName, path: pPath }
    try {
      if (cluster.clustered && pEverywhere && !localDisks) {
        const res = await api.createClusterPool(spec)
        const failed = res.filter((r) => !r.ok)
        if (failed.length === res.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
        toasts.push({
          kind: failed.length ? 'warn' : 'info',
          title: failed.length ? t('{pName} created on {v} of {length} hosts', { pName: pName, v: res.length - failed.length, length: res.length }) : t('{pName} created on every host', { pName: pName }),
          detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') || undefined,
        })
      } else {
        await api.createPool(spec)
      }
      poolDlg = false
      await loadPools()
      router.navigate(`/storage#${encodeURIComponent(pName)}`, { replace: true })
    } catch (err) {
      pErr = errorText(err)
    } finally {
      pBusy = false
    }
  }

  // Tools few single hosts need sit in the "more" menu instead of the header.
  const moreTools: MenuItem[] = [
    { label: t('From Content Library'), icon: BookOpen, hint: t('Browse cluster-wide content library'), run: () => router.navigate(router.scoped('/library')) },
    { label: t('ZFS Replication'), icon: RefreshCw, hint: t('Automated differential block-level storage replication between nodes'), run: () => (zfsOpen = true) },
    { label: t('NVMe/TCP'), icon: Cable, hint: t('Connect NVMe/TCP storage targets'), run: () => (nvmeOpen = true) },
  ]

  // A started pool of files (directory, NFS, SMB, carved) can be browsed folder by folder.
  let view = $state<'volumes' | 'files'>('volumes')
  const browsable = (p: Pool) => p.active && ['dir', 'fs', 'netfs'].includes(p.type)
  const usedPct = (p: Pool) => (p.capacityBytes ? (p.allocationBytes / p.capacityBytes) * 100 : 0)
  const fmtLabel = (v: Volume) => (v.format === 'iso' ? 'ISO image' : v.format ? v.format.toUpperCase() : v.type)
</script>

<ObjectXmlDialog
  open={!!xmlPool}
  title={t('Storage pool {v}', { v: xmlPool ?? '' })}
  kind="storage pool"
  load={() => api.poolXML(xmlPool!)}
  save={(x) => api.savePoolXML(xmlPool!, x)}
  onclose={(saved) => {
    xmlPool = null
    if (saved) void loadPools()
  }}
/>

<svelte:head><title>{t('Storage · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Storage')}</h1>
    <p class="lead">{t('Disk images and installation media live in storage pools, usually directories on this host.')}</p>
  </div>
  <div class="head-right">
    <ActionMenu label={t('More storage tools')} items={moreTools} />
    <button class="ui-btn" onclick={() => (orphanedOpen = true)} title={t('Find disks not attached to any VM')}>
      <HardDrive size={15} /> {t('Unused disks')}
    </button>
    <button class="ui-btn" onclick={() => addImage('url')} title={t('Upload an image from this computer, or let the host download one from a URL')}>
      <Download size={15} /> {t('Add image')}
    </button>
    <button class="ui-btn primary" disabled={!session.can('admin')} title={session.why('admin')} onclick={openPoolDlg}>
      <FolderPlus size={15} /> {t('New pool')}
    </button>
  </div>
</header>

{#if session.can('admin')}
  <NetworkStorageFinder
    onconnected={async (pool) => {
      await loadPools()
      router.navigate(`/storage#${encodeURIComponent(pool)}`, { replace: true })
    }}
  />
{/if}

{#if poolsError}
  <div class="ui-error-box">{poolsError}</div>
{:else if pools === null}
  <div class="pools">{#each [0, 1] as i (i)}<div class="pool sk"></div>{/each}</div>
{:else if pools.length === 0}
  <div class="empty ui-panel">
    <HardDrive size={28} strokeWidth={1.5} />
    <strong>{t('No storage pools yet')}</strong>
    <span>{t('Create one to keep disk images, for example in /var/lib/libvirt/images.')}</span>
  </div>
{:else}
  <div class="pools" role="tablist" aria-label={t('Storage pools')}>
    {#each pools as p (p.name)}
      <button
        class="pool"
        class:active={current?.name === p.name}
        role="tab"
        aria-selected={current?.name === p.name}
        onclick={() => router.navigate(`/storage#${encodeURIComponent(p.name)}`, { replace: true })}
      >
        <span class="p-head">
          <span class="p-name">{p.name}</span>
          {#if tiers[p.name]}<span class="tier mono">{tiers[p.name].toUpperCase()}</span>{/if}
          <span class="state" class:on={p.active}>{p.active ? 'active' : 'stopped'}</span>
        </span>
        <span class="p-path mono">{p.path ?? p.type}</span>
        {#if p.active && poolFills(p)}
          <span class="bar"><span style:width="{usedPct(p)}%" class:full={usedPct(p) > 90}></span></span>
          <span class="p-sub num">{t('{free} free of {size}', { free: formatBytes(p.availableBytes), size: formatBytes(p.capacityBytes) })} · {plural(p.volumes, 'volume')}</span>
        {:else if p.active}
          <span class="p-sub num">{formatBytes(p.capacityBytes)} · {plural(p.volumes, 'LUN')}</span>
        {/if}
      </button>
    {/each}
  </div>

  {#if current}
    <section class="ui-panel vols">
      <header class="v-head">
        <div class="v-title">
          <h2 class="keep-case">{current.name}</h2>
          <span class="chip mono">{current.type}</span>
          <label class="ui-switch small" title={t('Start this pool when the host boots')}>
            <input type="checkbox" checked={current.autostart} disabled={!session.can('admin')} onchange={(e) => toggleAutostart(current!, e.currentTarget.checked)} />
            {t('Autostart')}
          </label>
        </div>
        <div class="v-actions" inert={!session.can('admin')} title={session.why('admin')}>
          <button class="ui-btn sm ghost" onclick={() => (xmlPool = current!.name)}><FileCode size={14} /> {t('XML')}</button>
          {#if current.active}
            <button class="ui-btn sm ghost" onclick={() => poolAction(current!, 'refresh')} title={t('Rescan the directory for files added outside VirtoMir')}>
              <RefreshCw size={14} /> {t('Rescan')}
            </button>
            <ConfirmButton label={t('Stop')} confirm={t('Stop the pool?')} icon={Square} run={() => poolAction(current!, 'stop')} />
            {#if poolFills(current)}
              <button class="ui-btn sm" onclick={() => addImage('file')}><Upload size={14} /> {t('Upload image')}</button>
              <button class="ui-btn sm primary" onclick={openVolume}><Plus size={14} /> {t('New volume')}</button>
            {/if}
          {:else}
            <ConfirmButton
              label={t('Remove pool')}
              confirm={t('Forget this pool? (files stay)')}
              icon={Trash}
              run={async () => {
                await api.deletePool(current!.name)
                router.navigate('/storage', { replace: true })
                await loadPools()
              }}
            />
            {#if current.origin === 'carved'}
              <ConfirmButton
                label={t('Remove and free the space')}
                confirm={t('Delete its volume and give the space back? This cannot be undone.')}
                icon={Trash}
                run={async () => {
                  await api.deletePool(current!.name, true)
                  router.navigate('/storage', { replace: true })
                  await loadPools()
                }}
              />
            {/if}
            <button class="ui-btn sm primary" onclick={() => poolAction(current!, 'start')}><Play size={14} /> {t('Start')}</button>
          {/if}
        </div>
      </header>
      <PoolEfficiency pool={current} tier={tiers[current.name]} ontier={loadTiers} />

      {#if browsable(current)}
        <div class="seg view" role="tablist" aria-label={t('View')}>
          <button role="tab" aria-selected={view === 'volumes'} class:on={view === 'volumes'} onclick={() => (view = 'volumes')}>{t('Volumes')}</button>
          <button role="tab" aria-selected={view === 'files'} class:on={view === 'files'} onclick={() => (view = 'files')}>{t('Files and folders')}</button>
        </div>
      {/if}

      {#if view === 'files' && browsable(current)}
        {#key current.name}<DatastoreBrowser pool={current} pools={pools ?? []} onchange={refresh} />{/key}
      {:else if volError}
        <div class="ui-error-box">{volError}</div>
      {:else if !current.active}
        <p class="muted">{t('Start the pool to see its volumes.')}</p>
      {:else if volumes === null}
        <p class="muted">{t('Loading…')}</p>
      {:else if volumes.length === 0}
        <p class="muted">{t('No volumes in this pool yet. Upload an ISO or create an empty disk.')}</p>
      {:else}
        <div class="table" role="table" aria-label={t('Volumes in {name}', { name: current.name })}>
          <div class="row head" role="row">
            <span role="columnheader">{t('Name')}</span>
            <span role="columnheader">{t('Format')}</span>
            <span role="columnheader">{t('Size')}</span>
            <span role="columnheader">{t('Used by')}</span>
            <span role="columnheader" class="sr">{t('Actions')}</span>
          </div>
          {#each volumes as v (v.name)}
            <div class="row" role="row">
              <span class="name" role="cell">
                {#if v.format === 'iso'}<Disc size={15} />{:else}<HardDrive size={15} />{/if}
                <span class="titles">
                  <span class="vn">{v.name}</span>
                  <span class="vp mono" title={v.path}>{v.backing ? t('overlay of {backing}', { backing: v.backing }) : v.path}</span>
                </span>
              </span>
              <span role="cell" class="muted">{fmtLabel(v)}</span>
              <span role="cell" class="num" title={t('Allocated on disk / virtual size')}>
                {v.allocationBytes !== v.capacityBytes ? `${formatBytes(v.allocationBytes)} / ` : ''}{formatBytes(v.capacityBytes)}
              </span>
              <span role="cell" class="used">
                {#if v.usedBy.length}{v.usedBy.join(', ')}{:else if v.writing}<span class="muted">{t('being written')}</span>{:else}<span class="muted">—</span>{/if}
              </span>
              <span role="cell" class="act" inert={!session.can('admin')}>
                {#if v.format !== 'iso' && v.usedBy.length === 0 && !v.writing && poolFills(current)}
                  <button class="ui-btn sm ghost" onclick={() => openGrow(v)} title={t('Make this volume bigger')}><Maximize2 size={14} /> {t('Grow')}</button>
                {/if}
                {#if !v.writing && poolFills(current)}<ConfirmButton
                  label={t('Delete')}
                  confirm={t('Delete for good?')}
                  icon={Trash}
                  disabled={v.usedBy.length > 0}
                  title={v.usedBy.length ? t('In use by a machine') : t('Delete this volume')}
                  failure={t('Could not delete {name}', { name: v.name })}
                  run={async () => {
                    await api.deleteVolume(current!.name, v.name)
                    toasts.push({ kind: 'info', title: `${v.name} deleted` })
                    await refresh()
                  }}
                />{/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{/if}

<Dialog open={volDlg} title={t('New volume')} subtitle={t('An empty disk image in {name}', { name: current?.name })} onclose={() => (volDlg = false)}>
  <form id="vol-form" class="form" onsubmit={createVolume}>
    <label class="ui-field">
      <span class="label">{t('Name')}</span>
      <input class="ui-input" bind:value={vName} placeholder={t('data-disk')} required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <div class="two">
      <label class="ui-field">
        <span class="label">{t('Size (GiB)')}</span>
        <input class="ui-input" type="number" min="1" max="65536" bind:value={vSize} required />
      </label>
      <label class="ui-field">
        <span class="label">{t('Format')}</span>
        <select class="ui-input" bind:value={vFormat}>
          <option value="qcow2">{t('QCOW2 (snapshots, overlay)')}</option>
          <option value="raw">{t('Raw (fastest)')}</option>
        </select>
      </label>
    </div>
    <label class="ui-field">
      <span class="label">{t('Provisioning Policy')}</span>
      <select class="ui-input" bind:value={vProvisioning}>
        <option value="thin">{t('Thin (sparse, allocate on demand)')}</option>
        <option value="thick">{t('Thick (fully preallocated, zeroed, highest sustained I/O)')}</option>
      </select>
    </label>
    {#if vErr}<div class="ui-error-box">{vErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (volDlg = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="vol-form" disabled={vBusy}>{vBusy ? t('Creating…') : t('Create volume')}</button>
  {/snippet}
</Dialog>

<Dialog open={growVol !== null} title={t('Grow {v}', { v: growVol?.name ?? '' })} onclose={() => (growVol = null)}>
  <form id="grow-form" class="form" onsubmit={grow}>
    <label class="ui-field">
      <span class="label">{t('New size (GiB)')}</span>
      <input class="ui-input" type="number" min={Math.floor((growVol?.capacityBytes ?? 0) / 2 ** 30) + 1} max="65536" bind:value={growGiB} />
      <span class="hint">{t('Currently {capacityBytes}. Volumes can only grow. Disks attached to a machine are grown from its Hardware tab.', { capacityBytes: formatBytes(growVol?.capacityBytes ?? 0) })}</span>
    </label>
    {#if growErr}<div class="ui-error-box">{growErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (growVol = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="grow-form" disabled={growBusy}>{growBusy ? t('Growing…') : t('Grow')}</button>
  {/snippet}
</Dialog>

<Dialog open={poolDlg} title={t('New storage pool')} subtitle={t('Where disk images and ISOs live')} onclose={() => (poolDlg = false)}>
  <form id="pool-form" class="form" onsubmit={createPool}>
    <div class="seg" role="radiogroup" aria-label={t('Kind of storage')}>
      <button type="button" role="radio" aria-checked={pType === 'carve'} class:on={pType === 'carve'} onclick={() => (pType = 'carve')}>{t('From free space')}</button>
      <button type="button" role="radio" aria-checked={pType === 'dir'} class:on={pType === 'dir'} onclick={() => (pType = 'dir')}>{t('Directory on the host')}</button>
      <button type="button" role="radio" aria-checked={pType === 'netfs'} class:on={pType === 'netfs'} onclick={() => (pType = 'netfs')}>{t('NFS share')}</button>
      <button type="button" role="radio" aria-checked={pType === 'iscsi'} class:on={pType === 'iscsi'} onclick={() => (pType = 'iscsi')}>{t('iSCSI')}</button>
      <button type="button" role="radio" aria-checked={pType === 'rbd'} class:on={pType === 'rbd'} onclick={() => (pType = 'rbd')}>{t('Ceph RBD')}</button>
      <button type="button" role="radio" aria-checked={pType === 'logical'} class:on={pType === 'logical'} onclick={() => (pType = 'logical')}>{t('LVM')}</button>
      <button type="button" role="radio" aria-checked={pType === 'zfs'} class:on={pType === 'zfs'} onclick={() => (pType = 'zfs')}>{t('ZFS')}</button>
    </div>
    <label class="ui-field">
      <span class="label">{t('Name')}</span>
      <input class="ui-input" bind:value={pName} placeholder={pType === 'netfs' ? 'shared' : 'fast-ssd'} required pattern={pType === 'carve' ? '[A-Za-z0-9][A-Za-z0-9_.\\-]*' : '[A-Za-z0-9][A-Za-z0-9._+\\-]*'} />
    </label>
    {#if pType === 'carve'}
      {#if sources === null}
        <span class="hint">{t('Looking for free space…')}</span>
      {:else if !sources.length}
        <div class="warn">{t('No free space to make a pool from: this host has no empty disk, no LVM volume group with free space and no ZFS pool. Add a disk, or pick another kind of storage above.')}</div>
      {:else}
        <label class="ui-field">
          <span class="label">{t('Take the space from')}</span>
          <select class="ui-input" value={pSourceKey} onchange={(e) => pickSource((e.currentTarget as HTMLSelectElement).value)}>
            {#each sources as s (sourceKey(s))}<option value={sourceKey(s)}>{sourceLabel(s)}</option>{/each}
          </select>
        </label>
        <label class="ui-field">
          <span class="label">{t('Size')}</span>
          <div class="size">
            <input type="range" min="1" max={maxGiB} step="1" bind:value={pSizeGiB} aria-label={t('Size')} />
            <input class="ui-input mono" type="number" min="1" max={maxGiB} step="1" bind:value={pSizeGiB} required />
            <span>GiB</span>
          </div>
          <span class="hint">{t('{size} of {free} free', { size: formatBytes(pSizeGiB * 2 ** 30), free: formatBytes(carveSrc?.freeBytes ?? 0) })}</span>
        </label>
        <span class="hint">
          {#if carveSrc?.kind === 'disk'}
            {t('The disk becomes an LVM volume group. The pool gets a logical volume of this size with an XFS file system, mounted at /srv/virtomir/{name} on every boot; the rest of the disk stays free for more pools.', { name: pName || '…' })}
          {:else if carveSrc?.kind === 'vg'}
            {t('A logical volume of this size with an XFS file system, mounted at /srv/virtomir/{name} on every boot.', { name: pName || '…' })}
          {:else}
            {t('A dataset with a quota of this size, mounted at /srv/virtomir/{name}.', { name: pName || '…' })}
          {/if}
          {t('Machine disks on it are qcow2 images: thin, with snapshots.')}
        </span>
      {/if}
    {/if}
    {#if pType === 'netfs'}
      <div class="row2">
        <label class="ui-field">
          <span class="label">{t('NFS server')}</span>
          <input class="ui-input mono" bind:value={pHost} placeholder="192.168.1.20" required />
        </label>
        <label class="ui-field">
          <span class="label">{t('Export')}</span>
          <input class="ui-input mono" bind:value={pExport} placeholder="/srv/vms" required />
        </label>
      </div>
    {/if}
    {#if pType === 'iscsi'}
      <div class="row2">
        <label class="ui-field">
          <span class="label">{t('Portal')}</span>
          <input class="ui-input mono" bind:value={pHost} placeholder="192.168.1.30" required />
        </label>
        <label class="ui-field">
          <span class="label">{t('Target IQN')}</span>
          <input class="ui-input mono" bind:value={pTarget} placeholder={t('iqn.2026-01.com.example:vms')} required />
        </label>
      </div>
      <span class="hint">{t('Each LUN of the target becomes a volume you can give to a machine. The host needs the iSCSI initiator (open-iscsi).')}</span>
    {:else if pType === 'rbd'}
      <label class="ui-field">
        <span class="label">{t('Monitors')}</span>
        <input class="ui-input mono" bind:value={pMonitors} placeholder="10.0.0.1, 10.0.0.2, 10.0.0.3:3300" required />
      </label>
      <div class="row2">
        <label class="ui-field">
          <span class="label">{t('Ceph pool')}</span>
          <input class="ui-input mono" bind:value={pCephPool} placeholder="vms" required />
        </label>
        <label class="ui-field">
          <span class="label">{t('User')}</span>
          <input class="ui-input mono" bind:value={pCephUser} placeholder={t('libvirt')} />
        </label>
      </div>
      <label class="ui-field">
        <span class="label">{t('Key')}</span>
        <input class="ui-input mono" type="password" bind:value={pCephKey} placeholder={t('from: ceph auth get-key client.libvirt')} autocomplete="off" />
        <span class="hint">{t('Stored in a libvirt secret on the host, never in the pool definition.')}</span>
      </label>
    {:else if pType === 'logical'}
      <label class="ui-field">
        <span class="label">{t('Volume group')}</span>
        <input class="ui-input mono" bind:value={pSource} placeholder={t('vg-fast')} required />
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={pNewVG} />
        <span>{t('Create this volume group from empty disks')}</span>
      </label>
      {#if pNewVG}
        <label class="ui-field">
          <span class="label">{t('Disks')}</span>
          <input class="ui-input mono" bind:value={pDevices} placeholder={t('/dev/disk/by-id/nvme-…, /dev/sdc')} required />
        </label>
        <div class="warn">{t('The disks become this volume group. Only empty ones are accepted: a disk with a partition table, file system or LVM label is refused, so nothing is overwritten.')}</div>
      {:else}
        <span class="hint">{t('An existing volume group (see')} <span class="mono">vgs</span>{t('). Each logical volume becomes a volume; new disks are raw logical volumes.')}</span>
      {/if}
    {:else if pType === 'zfs'}
      <label class="ui-field">
        <span class="label">{t('Pool or dataset')}</span>
        <input class="ui-input mono" bind:value={pSource} placeholder={t('tank/vms')} required />
        <span class="hint">{t('An existing zpool or dataset (see')} <span class="mono">{t('zfs list')}</span>{t('); new disks are ZFS volumes (zvols). The host needs libvirt\'s ZFS driver (libvirt-daemon-driver-storage-zfs).')}</span>
      </label>
    {/if}
    {#if pType === 'dir' || pType === 'netfs'}
    <label class="ui-field">
      <span class="label">{pType === 'netfs' ? t('Mount at') : t('Directory')}</span>
      <input class="ui-input mono" bind:value={pPath} placeholder={pType === 'netfs' ? '/var/lib/libvirt/shared' : '/srv/vm-images'} required />
      <span class="hint">{pType === 'netfs' ? t('Mounted now and on every boot (the host needs the NFS client, e.g. nfs-common).') : t('Created if it does not exist. The pool starts now and on every boot.')}</span>
    </label>
    {/if}
    {#if cluster.clustered && !localDisks}
      <label class="check">
        <input type="checkbox" bind:checked={pEverywhere} />
        <span>{t('Create it on every host of the cluster{v}', { v: pType === 'netfs' ? t(' — machines on it can then move between hosts without copying their disks') : '' })}</span>
      </label>
    {/if}
    {#if pErr}<div class="ui-error-box">{pErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (poolDlg = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="pool-form" disabled={pBusy || (pType === 'carve' && !carveSrc)}>{pBusy ? t('Creating…') : t('Create pool')}</button>
  {/snippet}
</Dialog>

<OrphanedDisksModal open={orphanedOpen} onclose={() => (orphanedOpen = false)} />
<ZfsReplicationModal open={zfsOpen} onclose={() => (zfsOpen = false)} />
<NvmeDialog
  bind:open={nvmeOpen}
  onpool={(device) => {
    nvmeOpen = false
    ;[pName, pType, pNewVG, pDevices, pSource, pErr, poolDlg] = ['', 'logical', true, device, '', '', true]
  }}
/>
<IsoDownloadModal
  open={isoOpen}
  mode={isoMode}
  poolName={current?.name ?? 'default'}
  onclose={() => (isoOpen = false)}
  onsuccess={refresh}
/>

<style>
  .seg {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 3px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
  }
  .seg button {
    flex: 1;
    height: 32px;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-2);
    font: inherit;
    font-weight: 500;
    cursor: pointer;
  }
  .seg button.on {
    background: var(--metal);
    outline: 1px solid var(--border-strong);
    color: var(--text);
    box-shadow: var(--bevel);
  }
  .seg.view {
    max-width: 360px;
    margin: 6px 0 10px;
  }
  .size {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .size input[type='range'] {
    flex: 1;
  }
  .size .ui-input {
    width: 96px;
  }
  .row2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .warn {
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border-left: 3px solid var(--warn);
    color: var(--text-2);
    font-size: 12.5px;
  }
  .check {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-size: 13px;
    color: var(--text-2);
  }
  .check input {
    margin-top: 3px;
  }
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .pools {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 12px;
    margin-bottom: 16px;
  }
  .pool {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 14px 16px;
    min-height: 104px;
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface);
    text-align: left;
    cursor: pointer;
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .pool:hover {
    border-color: var(--border-strong);
  }
  .pool.active {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }
  .pool.sk {
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  .p-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .p-name {
    font-weight: 600;
    font-size: 14.5px;
  }
  .state {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .tier {
    margin-left: auto;
    padding: 0 6px;
    border: var(--frame) solid var(--ink);
    font-size: 10.5px;
    font-weight: 600;
    color: var(--text-2);
  }
  .state.on {
    color: var(--ok);
  }
  .p-path {
    color: var(--text-3);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .p-sub {
    color: var(--text-3);
    font-size: 12px;
  }
  .bar {
    display: block;
    height: 5px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width var(--dur) var(--ease-out);
  }
  .bar span.full {
    background: var(--warn);
  }
  .vols {
    padding: 14px 16px;
  }
  .v-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 10px;
  }
  .v-title {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
  }
  .small {
    font-size: 12.5px;
    color: var(--text-2);
  }
  .v-actions {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .table {
    display: grid;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(200px, 2fr) 110px 150px minmax(120px, 1fr) 220px;
    gap: 14px;
    align-items: center;
    min-height: 52px;
    padding: 6px 4px;
    border-top: 1px solid var(--border);
    font-size: 13px;
  }
  .row.head {
    min-height: 0;
    padding: 8px 4px;
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    color: var(--text-2);
  }
  .titles {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .vn {
    color: var(--text);
    font-weight: 500;
  }
  .vp {
    color: var(--text-3);
    font-size: 11.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .used {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .act {
    display: flex;
    justify-content: flex-end;
    gap: 4px;
  }
  .muted {
    color: var(--text-3);
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 56px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
  @media (max-width: 900px) {
    .row {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .row > :nth-child(2),
    .row > :nth-child(4) {
      display: none;
    }
    .row > :nth-child(5) {
      grid-column: 1 / -1;
      justify-content: flex-start;
    }
    .row.head {
      display: none;
    }
  }
</style>
