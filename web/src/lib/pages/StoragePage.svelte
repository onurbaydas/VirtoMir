<script lang="ts">
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Plus from '@lucide/svelte/icons/plus'
  import Upload from '@lucide/svelte/icons/upload'
  import FolderPlus from '@lucide/svelte/icons/folder-plus'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Trash from '@lucide/svelte/icons/trash'
  import Disc from '@lucide/svelte/icons/disc'
  import { api, errorText, type Pool, type Volume } from '../api'
  import { formatBytes, plural } from '../format'
  import { router } from '../router.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'

  let { pool: selected }: { pool: string } = $props()

  let pools = $state<Pool[] | null>(null)
  let poolsError = $state('')
  let volumes = $state<Volume[] | null>(null)
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
      toasts.push({ kind: 'error', title: `Could not ${action} ${p.name}`, detail: errorText(e) })
    }
  }
  async function toggleAutostart(p: Pool, on: boolean) {
    try {
      await api.poolAutostart(p.name, on)
      await loadPools()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Autostart not changed', detail: errorText(e) })
    }
  }

  // ---- New volume -----------------------------------------------------------------
  let volDlg = $state(false)
  let vName = $state('')
  let vSize = $state(20)
  let vFormat = $state<'qcow2' | 'raw'>('qcow2')
  let vBusy = $state(false)
  let vErr = $state('')
  function openVolume() {
    vName = ''
    vSize = 20
    vFormat = 'qcow2'
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
      await api.createVolume(current.name, name, vSize, vFormat)
      volDlg = false
      toasts.push({ kind: 'info', title: `${name} created` })
      await refresh()
    } catch (err) {
      vErr = errorText(err)
    } finally {
      vBusy = false
    }
  }

  // ---- Upload ---------------------------------------------------------------------
  let fileInput = $state<HTMLInputElement>()
  let uploading = $state<{ name: string; fraction: number; abort: AbortController } | null>(null)
  async function upload(file: File) {
    if (!current) return
    const name = file.name.replace(/[^A-Za-z0-9._+-]/g, '_')
    const abort = new AbortController()
    uploading = { name, fraction: 0, abort }
    try {
      await api.upload(current.name, file, name, (f) => uploading && (uploading.fraction = f), abort.signal)
      toasts.push({ kind: 'info', title: `${name} uploaded`, detail: `${formatBytes(file.size)} to ${current.name}` })
      await refresh()
    } catch (e) {
      toasts.push({ kind: 'error', title: `Upload of ${name} failed`, detail: errorText(e) })
    } finally {
      uploading = null
    }
  }

  // ---- New pool -------------------------------------------------------------------
  let poolDlg = $state(false)
  let pName = $state('')
  let pPath = $state('')
  let pBusy = $state(false)
  let pErr = $state('')
  async function createPool(e: SubmitEvent) {
    e.preventDefault()
    pBusy = true
    pErr = ''
    try {
      await api.createPool(pName, pPath)
      poolDlg = false
      await loadPools()
      router.navigate(`/storage#${encodeURIComponent(pName)}`, { replace: true })
    } catch (err) {
      pErr = errorText(err)
    } finally {
      pBusy = false
    }
  }

  const usedPct = (p: Pool) => (p.capacityBytes ? (p.allocationBytes / p.capacityBytes) * 100 : 0)
  const fmtLabel = (v: Volume) => (v.format === 'iso' ? 'ISO image' : v.format ? v.format.toUpperCase() : v.type)
</script>

<svelte:head><title>Storage · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Storage</h1>
    <p class="lead">Disk images and installation media live in storage pools, usually directories on this host.</p>
  </div>
  <button class="ui-btn" onclick={() => ((pName = ''), (pPath = ''), (pErr = ''), (poolDlg = true))}>
    <FolderPlus size={15} /> New pool
  </button>
</header>

{#if poolsError}
  <div class="ui-error-box">{poolsError}</div>
{:else if pools === null}
  <div class="pools">{#each [0, 1] as i (i)}<div class="pool sk"></div>{/each}</div>
{:else if pools.length === 0}
  <div class="empty ui-panel">
    <HardDrive size={28} strokeWidth={1.5} />
    <strong>No storage pools yet</strong>
    <span>Create one to keep disk images, for example in /var/lib/libvirt/images.</span>
  </div>
{:else}
  <div class="pools" role="tablist" aria-label="Storage pools">
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
          <span class="state" class:on={p.active}>{p.active ? 'active' : 'stopped'}</span>
        </span>
        <span class="p-path mono">{p.path ?? p.type}</span>
        {#if p.active}
          <span class="bar"><span style:width="{usedPct(p)}%" class:full={usedPct(p) > 90}></span></span>
          <span class="p-sub num">{formatBytes(p.availableBytes)} free of {formatBytes(p.capacityBytes)} · {plural(p.volumes, 'volume')}</span>
        {/if}
      </button>
    {/each}
  </div>

  {#if current}
    <section class="ui-panel vols">
      <header class="v-head">
        <div class="v-title">
          <h2>{current.name}</h2>
          <span class="chip mono">{current.type}</span>
          <label class="ui-switch small" title="Start this pool when the host boots">
            <input type="checkbox" checked={current.autostart} onchange={(e) => toggleAutostart(current!, e.currentTarget.checked)} />
            Autostart
          </label>
        </div>
        <div class="v-actions">
          {#if current.active}
            <button class="ui-btn sm ghost" onclick={() => poolAction(current!, 'refresh')} title="Rescan the directory for files added outside VirtoMir">
              <RefreshCw size={14} /> Rescan
            </button>
            <ConfirmButton label="Stop" confirm="Stop the pool?" icon={Square} run={() => poolAction(current!, 'stop')} />
            <button class="ui-btn sm" onclick={() => fileInput?.click()} disabled={!!uploading}><Upload size={14} /> Upload ISO</button>
            <button class="ui-btn sm primary" onclick={openVolume}><Plus size={14} /> New volume</button>
          {:else}
            <ConfirmButton
              label="Remove pool"
              confirm="Forget this pool? (files stay)"
              icon={Trash}
              run={async () => {
                await api.deletePool(current!.name)
                router.navigate('/storage', { replace: true })
                await loadPools()
              }}
            />
            <button class="ui-btn sm primary" onclick={() => poolAction(current!, 'start')}><Play size={14} /> Start</button>
          {/if}
        </div>
      </header>
      <input
        type="file"
        accept=".iso,.img,.raw"
        hidden
        bind:this={fileInput}
        onchange={(e) => {
          const f = e.currentTarget.files?.[0]
          e.currentTarget.value = ''
          if (f) void upload(f)
        }}
      />

      {#if uploading}
        <div class="upload">
          <span class="mono">{uploading.name}</span>
          <span class="bar wide"><span style:width="{uploading.fraction * 100}%"></span></span>
          <span class="num">{Math.round(uploading.fraction * 100)} %</span>
          <button class="ui-btn sm ghost" onclick={() => uploading?.abort.abort()}>Cancel</button>
        </div>
      {/if}

      {#if volError}
        <div class="ui-error-box">{volError}</div>
      {:else if !current.active}
        <p class="muted">Start the pool to see its volumes.</p>
      {:else if volumes === null}
        <p class="muted">Loading…</p>
      {:else if volumes.length === 0}
        <p class="muted">No volumes in this pool yet. Upload an ISO or create an empty disk.</p>
      {:else}
        <div class="table" role="table" aria-label="Volumes in {current.name}">
          <div class="row head" role="row">
            <span role="columnheader">Name</span>
            <span role="columnheader">Format</span>
            <span role="columnheader">Size</span>
            <span role="columnheader">Used by</span>
            <span role="columnheader" class="sr">Actions</span>
          </div>
          {#each volumes as v (v.name)}
            <div class="row" role="row">
              <span class="name" role="cell">
                {#if v.format === 'iso'}<Disc size={15} />{:else}<HardDrive size={15} />{/if}
                <span class="titles">
                  <span class="vn">{v.name}</span>
                  <span class="vp mono" title={v.path}>{v.backing ? `overlay of ${v.backing}` : v.path}</span>
                </span>
              </span>
              <span role="cell" class="muted">{fmtLabel(v)}</span>
              <span role="cell" class="num" title="Allocated on disk / virtual size">
                {v.allocationBytes !== v.capacityBytes ? `${formatBytes(v.allocationBytes)} / ` : ''}{formatBytes(v.capacityBytes)}
              </span>
              <span role="cell" class="used">
                {#if v.usedBy.length}{v.usedBy.join(', ')}{:else}<span class="muted">—</span>{/if}
              </span>
              <span role="cell" class="act">
                <ConfirmButton
                  label="Delete"
                  confirm="Delete for good?"
                  icon={Trash}
                  disabled={v.usedBy.length > 0}
                  title={v.usedBy.length ? 'In use by a machine' : 'Delete this volume'}
                  failure="Could not delete {v.name}"
                  run={async () => {
                    await api.deleteVolume(current!.name, v.name)
                    toasts.push({ kind: 'info', title: `${v.name} deleted` })
                    await refresh()
                  }}
                />
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{/if}

<Dialog open={volDlg} title="New volume" subtitle="An empty disk image in {current?.name}" onclose={() => (volDlg = false)}>
  <form id="vol-form" class="form" onsubmit={createVolume}>
    <label class="ui-field">
      <span class="label">Name</span>
      <input class="ui-input" bind:value={vName} placeholder="data-disk" required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <div class="two">
      <label class="ui-field">
        <span class="label">Size (GiB)</span>
        <input class="ui-input" type="number" min="1" max="65536" bind:value={vSize} required />
      </label>
      <label class="ui-field">
        <span class="label">Format</span>
        <select class="ui-input" bind:value={vFormat}>
          <option value="qcow2">QCOW2 (thin, snapshots)</option>
          <option value="raw">Raw (fastest)</option>
        </select>
      </label>
    </div>
    {#if vErr}<div class="ui-error-box">{vErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (volDlg = false)}>Cancel</button>
    <button class="ui-btn primary" form="vol-form" disabled={vBusy}>{vBusy ? 'Creating…' : 'Create volume'}</button>
  {/snippet}
</Dialog>

<Dialog open={poolDlg} title="New storage pool" subtitle="A directory on this host for disk images and ISOs" onclose={() => (poolDlg = false)}>
  <form id="pool-form" class="form" onsubmit={createPool}>
    <label class="ui-field">
      <span class="label">Name</span>
      <input class="ui-input" bind:value={pName} placeholder="fast-ssd" required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <label class="ui-field">
      <span class="label">Directory</span>
      <input class="ui-input mono" bind:value={pPath} placeholder="/srv/vm-images" required />
      <span class="hint">Created if it does not exist. The pool starts now and on every boot.</span>
    </label>
    {#if pErr}<div class="ui-error-box">{pErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (poolDlg = false)}>Cancel</button>
    <button class="ui-btn primary" form="pool-form" disabled={pBusy}>{pBusy ? 'Creating…' : 'Create pool'}</button>
  {/snippet}
</Dialog>

<style>
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
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--surface);
    text-align: left;
    cursor: pointer;
    transition: border-color var(--dur-fast);
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
    border-radius: 3px;
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
  .bar.wide {
    flex: 1;
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
  .upload {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    margin-bottom: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-elev);
    font-size: 13px;
  }
  .table {
    display: grid;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(200px, 2fr) 110px 150px minmax(120px, 1fr) 150px;
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
    letter-spacing: 0.04em;
    text-transform: uppercase;
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
