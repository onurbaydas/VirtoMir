<script lang="ts">
  // The datastore browser: folders and files of a file-based pool, with download, upload,
  // new folder, rename, copy or move to any pool of the host, and delete. A machine's disk
  // cannot be renamed, moved or deleted (the server refuses too).
  import Folder from '@lucide/svelte/icons/folder'
  import FileIcon from '@lucide/svelte/icons/file'
  import FolderPlus from '@lucide/svelte/icons/folder-plus'
  import Upload from '@lucide/svelte/icons/upload'
  import Download from '@lucide/svelte/icons/download'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Copy from '@lucide/svelte/icons/copy'
  import Trash from '@lucide/svelte/icons/trash'
  import ChevronRight from '@lucide/svelte/icons/chevron-right'
  import ClipboardCopy from '@lucide/svelte/icons/clipboard-copy'
  import ArrowUp from '@lucide/svelte/icons/arrow-up'
  import ArrowDown from '@lucide/svelte/icons/arrow-down'
  import { api, errorText, type FileEntry, type Pool } from '../api'
  import { formatBytes } from '../format'
  import { hostPath, nameMatches, nextSort, sortEntries, type FileSort, type FileSortKey } from '../files'
  import { t } from '../i18n.svelte'
  import { track } from '../jobs'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'

  let { pool, pools, onchange }: { pool: Pool; pools: Pool[]; onchange: () => void } = $props()

  let dir = $state('')
  let entries = $state<FileEntry[] | null>(null)
  let error = $state('')
  let admin = $derived(session.can('admin'))
  let crumbs = $derived(dir ? dir.split('/') : [])
  const join = (a: string, b: string) => (a ? `${a}/${b}` : b)

  async function load() {
    error = ''
    try {
      entries = await api.files(pool.name, dir)
    } catch (e) {
      error = errorText(e)
      entries = []
    }
  }
  $effect(() => {
    void dir
    void load()
  })

  // ---- Sort and filter --------------------------------------------------------------------
  // The sort is kept per browser; the filter is for the folder that is open.
  const SORT_KEY = 'virtomir.files.sort'
  function savedSort(): FileSort {
    try {
      const v = JSON.parse(localStorage.getItem(SORT_KEY) ?? '')
      if (['name', 'size', 'modified'].includes(v?.key)) return { key: v.key, desc: !!v.desc }
    } catch {
      // nothing saved, or no storage
    }
    return { key: 'name', desc: false }
  }
  let sort = $state<FileSort>(savedSort())
  function sortBy(key: FileSortKey) {
    sort = nextSort(sort, key)
    try {
      localStorage.setItem(SORT_KEY, JSON.stringify(sort))
    } catch {
      // the sort just is not remembered
    }
  }
  let filter = $state('')
  $effect(() => {
    void dir
    void pool.name
    filter = ''
  })
  let shown = $derived(sortEntries((entries ?? []).filter((e) => nameMatches(e.name, filter)), sort))
  const when = (iso: string) => (iso && !iso.startsWith('0001') ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '')

  // ---- Copy path --------------------------------------------------------------------------
  async function copyPath(e: FileEntry) {
    if (!pool.path) return
    const p = hostPath(pool.path, dir, e.name)
    try {
      await navigator.clipboard.writeText(p)
      toasts.push({ kind: 'info', title: t('Copied'), detail: p })
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Could not copy'), detail: errorText(err) })
    }
  }
  function changed() {
    void load()
    onchange()
  }

  // ---- New folder -----------------------------------------------------------------------
  let folderOpen = $state(false)
  let folderName = $state('')
  async function makeFolder(e: SubmitEvent) {
    e.preventDefault()
    try {
      await api.makeFolder(pool.name, join(dir, folderName.trim()))
      folderOpen = false
      folderName = ''
      await load()
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Folder not made'), detail: errorText(err) })
    }
  }

  // ---- Upload ---------------------------------------------------------------------------
  let picker = $state<HTMLInputElement>()
  let uploading = $state<{ name: string; pct: number } | null>(null)
  // uploadFiles uploads into the open folder, one file after the other (the button and drops).
  async function uploadFiles(files: File[]) {
    if (!admin || uploading) return
    const into = dir
    for (const f of files) {
      uploading = { name: f.name, pct: 0 }
      try {
        await api.uploadFile(pool.name, join(into, f.name), f, (x) => uploading && (uploading.pct = Math.round(x * 100)))
        toasts.push({ kind: 'info', title: t('{name} uploaded', { name: f.name }) })
      } catch (err) {
        toasts.push({ kind: 'error', title: t('Upload failed'), detail: errorText(err) })
      }
    }
    uploading = null
    changed()
  }
  async function uploadPicked() {
    const files = [...(picker?.files ?? [])]
    if (picker) picker.value = ''
    if (files.length) await uploadFiles(files)
  }
  // Files dropped from the desktop onto the list.
  let dragging = $state(false)
  const hasFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files')
  function dragOver(e: DragEvent) {
    if (!admin || !hasFiles(e)) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
    dragging = true
  }
  function drop(e: DragEvent) {
    if (!admin || !hasFiles(e)) return
    e.preventDefault()
    dragging = false
    const files = [...(e.dataTransfer?.files ?? [])]
    if (files.length) void uploadFiles(files)
  }

  // ---- Rename ---------------------------------------------------------------------------
  let renaming = $state<FileEntry | null>(null)
  let newName = $state('')
  async function rename(e: SubmitEvent) {
    e.preventDefault()
    if (!renaming) return
    try {
      await api.renameFile(pool.name, join(dir, renaming.name), join(dir, newName.trim()))
      renaming = null
      changed()
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Not renamed'), detail: errorText(err) })
    }
  }

  // ---- Copy or move ---------------------------------------------------------------------
  let filePools = $derived(pools.filter((p) => p.active && ['dir', 'fs', 'netfs'].includes(p.type)))
  let copying = $state<FileEntry | null>(null)
  let toPool = $state('')
  let toName = $state('')
  let move = $state(false)
  function openCopy(e: FileEntry) {
    copying = e
    toPool = filePools.find((p) => p.name !== pool.name)?.name ?? pool.name
    toName = e.name
    move = false
  }
  async function copy(ev: SubmitEvent) {
    ev.preventDefault()
    if (!copying) return
    const name = copying.name
    try {
      const job = await api.copyFile(pool.name, { path: join(dir, name), toPool, toPath: toName.trim(), move })
      copying = null
      track(job, {
        success: move ? t('{name} moved to {pool}', { name, pool: toPool }) : t('{name} copied to {pool}', { name, pool: toPool }),
        onSuccess: changed,
      })
    } catch (err) {
      toasts.push({ kind: 'error', title: move ? t('Not moved') : t('Not copied'), detail: errorText(err) })
    }
  }
</script>

<div class="bar">
  <nav class="crumbs" aria-label={t('Folder')}>
    <button class="crumb" onclick={() => (dir = '')}>{pool.name}</button>
    {#each crumbs as c, i (i)}
      <ChevronRight size={14} />
      <button class="crumb" onclick={() => (dir = crumbs.slice(0, i + 1).join('/'))}>{c}</button>
    {/each}
  </nav>
  {#if admin}
    <span class="tools">
      <button class="ui-btn sm" onclick={() => ((folderOpen = !folderOpen), (folderName = ''))}><FolderPlus size={14} /> {t('New folder')}</button>
      <button class="ui-btn sm" disabled={!!uploading} onclick={() => picker?.click()}><Upload size={14} /> {t('Upload here')}</button>
      <input type="file" multiple bind:this={picker} onchange={uploadPicked} hidden />
    </span>
  {/if}
</div>
{#if folderOpen}
  <form class="inline" onsubmit={makeFolder}>
    <input class="ui-input" bind:value={folderName} placeholder={t('Folder name')} required aria-label={t('Folder name')} />
    <button class="ui-btn sm primary">{t('Create')}</button>
  </form>
{/if}
{#if uploading}
  <div class="progress" role="status">
    <span>{t('Uploading {name}… {pct} %', { name: uploading.name, pct: uploading.pct })}</span>
    <span class="pbar"><span style:width="{uploading.pct}%"></span></span>
  </div>
{/if}
{#if error}<div class="ui-error-box">{error}</div>{/if}
{#if (entries ?? []).length > 0}
  <input class="ui-input filter" type="search" bind:value={filter} placeholder={t('Filter by name')} aria-label={t('Filter by name')} />
{/if}

{#snippet sortHead(key: FileSortKey, label: string)}
  <button class="sort" role="columnheader" aria-sort={sort.key === key ? (sort.desc ? 'descending' : 'ascending') : 'none'} onclick={() => sortBy(key)}>
    {label}
    {#if sort.key === key}{#if sort.desc}<ArrowDown size={12} />{:else}<ArrowUp size={12} />{/if}{/if}
  </button>
{/snippet}

<div
  class="table"
  class:drop={dragging}
  role="table"
  tabindex="-1"
  aria-label={t('Files in {name}', { name: pool.name })}
  ondragover={dragOver}
  ondragleave={() => (dragging = false)}
  ondrop={drop}
>
  <div class="row head" role="row">
    {@render sortHead('name', t('Name'))}
    <span role="columnheader">{t('Format')}</span>
    {@render sortHead('size', t('Size'))}
    {@render sortHead('modified', t('Modified'))}
    <span role="columnheader">{t('Used by')}</span>
    <span role="columnheader"></span>
  </div>
  {#if dir}
    <div class="row" role="row">
      <button class="name" role="cell" onclick={() => (dir = crumbs.slice(0, -1).join('/'))}><Folder size={14} /> ..</button>
    </div>
  {/if}
  {#each shown as e (e.name)}
    {@const used = (e.usedBy ?? []).length > 0}
    <div class="row" role="row">
      {#if e.dir}
        <button class="name" role="cell" onclick={() => (dir = join(dir, e.name))}><Folder size={14} /> {e.name}</button>
      {:else}
        <span class="name" role="cell"><FileIcon size={14} /> {e.name}</span>
      {/if}
      <span role="cell" class="muted">{e.format ?? ''}</span>
      <span role="cell" class="num" title={t('Allocated on disk / virtual size')}>
        {#if !e.dir}{e.allocatedBytes < e.sizeBytes ? `${formatBytes(e.allocatedBytes)} / ` : ''}{formatBytes(e.sizeBytes)}{/if}
      </span>
      <span role="cell" class="muted when">{e.dir ? '' : when(e.modified)}</span>
      <span role="cell" class="used">{(e.usedBy ?? []).join(', ')}</span>
      <span role="cell" class="act">
        {#if admin}
          {#if !e.dir}
            {#if pool.path}
              <button class="ui-btn sm ghost" title={t('Copy the path on the host')} aria-label={t('Copy the path on the host')} onclick={() => copyPath(e)}><ClipboardCopy size={14} /></button>
            {/if}
            <a class="ui-btn sm ghost" href={api.fileDownloadURL(pool.name, join(dir, e.name))} download title={t('Download to this computer')}><Download size={14} /></a>
            <button class="ui-btn sm ghost" disabled={used} title={used ? t('In use by a machine') : t('Copy or move')} onclick={() => openCopy(e)}><Copy size={14} /></button>
          {/if}
          <button class="ui-btn sm ghost" disabled={used} title={used ? t('In use by a machine') : t('Rename')} onclick={() => ((renaming = e), (newName = e.name))}><Pencil size={14} /></button>
          <ConfirmButton
            label=""
            confirm={e.dir ? t('Delete the empty folder?') : t('Delete for good?')}
            icon={Trash}
            disabled={used}
            title={used ? t('In use by a machine') : t('Delete')}
            failure={t('Could not delete {name}', { name: e.name })}
            run={async () => {
              await api.deleteFile(pool.name, join(dir, e.name))
              changed()
            }}
          />
        {/if}
      </span>
    </div>
  {/each}
  {#if entries && entries.length === 0 && !error}
    <p class="muted empty">{admin ? t('This folder is empty. Drop files here to upload them.') : t('This folder is empty.')}</p>
  {:else if entries && shown.length === 0 && !error}
    <p class="muted empty">{t('No file matches')}</p>
  {/if}
</div>

<Dialog open={!!renaming} title={t('Rename')} subtitle={renaming?.name} onclose={() => (renaming = null)}>
  <form id="rename-form" onsubmit={rename}>
    <input class="ui-input" bind:value={newName} required aria-label={t('New name')} />
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (renaming = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="rename-form">{t('Rename')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!copying} title={t('Copy or move')} subtitle={copying?.name} onclose={() => (copying = null)}>
  <form id="copy-form" class="form" onsubmit={copy}>
    <label class="ui-field">
      <span class="label">{t('To pool')}</span>
      <select class="ui-input" bind:value={toPool}>
        {#each filePools as p (p.name)}<option value={p.name}>{p.name} ({formatBytes(p.availableBytes)} {t('free')})</option>{/each}
      </select>
    </label>
    <label class="ui-field">
      <span class="label">{t('As')}</span>
      <input class="ui-input mono" bind:value={toName} required />
      <span class="hint">{t('A path inside that pool, e.g. templates/web.qcow2. Thin images stay thin.')}</span>
    </label>
    <label class="check"><input type="checkbox" bind:checked={move} /> {t('Move (delete it here afterwards)')}</label>
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (copying = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="copy-form">{move ? t('Move') : t('Copy')}</button>
  {/snippet}
</Dialog>

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: 4px 0 10px;
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px;
    min-width: 0;
  }
  .crumb {
    border: none;
    background: none;
    font: inherit;
    font-weight: 600;
    color: var(--accent);
    cursor: pointer;
    padding: 2px 4px;
    overflow-wrap: anywhere;
  }
  .tools {
    display: flex;
    gap: 6px;
  }
  .inline {
    display: flex;
    gap: 6px;
    margin-bottom: 10px;
    max-width: 420px;
  }
  .progress {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-bottom: 10px;
    font-size: 12.5px;
    color: var(--text-2);
  }
  .pbar {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .pbar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .table {
    display: flex;
    flex-direction: column;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 1fr) minmax(0, 1.4fr) minmax(0, 1.5fr) minmax(0, 1.4fr) 180px;
    align-items: center;
    gap: 10px;
    padding: 6px 4px;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
  }
  .sort {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .sort:hover {
    color: var(--text-1);
  }
  .filter {
    max-width: 280px;
    margin-bottom: 8px;
  }
  .when {
    font-size: 12px;
  }
  .table.drop {
    outline: 2px dashed var(--accent);
    outline-offset: 2px;
    background: var(--accent-soft);
  }
  .row.head {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-3);
  }
  .name {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    overflow-wrap: anywhere;
    border: none;
    background: none;
    font: inherit;
    color: inherit;
    text-align: left;
    padding: 0;
  }
  button.name {
    cursor: pointer;
    font-weight: 600;
  }
  .act {
    display: flex;
    gap: 2px;
    justify-content: flex-end;
  }
  .muted,
  .used {
    color: var(--text-2);
  }
  .empty {
    padding: 10px 4px;
  }
  .check {
    display: flex;
    gap: 8px;
    font-size: 13px;
    color: var(--text-2);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  @media (max-width: 640px) {
    .row {
      grid-template-columns: minmax(0, 1fr) 180px;
    }
    .row > :nth-child(2),
    .row > :nth-child(3),
    .row > :nth-child(4),
    .row > :nth-child(5) {
      display: none;
    }
  }
</style>
