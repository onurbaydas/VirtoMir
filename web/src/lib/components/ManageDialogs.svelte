<script lang="ts" module>
  export type ManageDialog = 'rename' | 'clone' | 'delete' | 'disk' | 'nic' | 'media' | null
</script>

<script lang="ts">
  // The machine page's dialogs: rename, clone, delete, add disk, add adapter, change media.
  import { api, errorText, type DomainDetail, type Device, type Pool, type VNetwork } from '../api'
  import { track } from '../jobs'
  import { activePools, allVolumes, isISO, type PoolVolume } from '../media'
  import { formatBytes } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    which: ManageDialog
    detail: DomainDetail
    /** Drive for the media dialog. */
    drive?: Device
    active: boolean
    onclose: () => void
    /** Called after a change so the page reloads its data. */
    onchange: () => void
  }
  let { which, detail, drive, active, onclose, onchange }: Props = $props()

  let busy = $state(false)
  let error = $state('')
  let pools = $state<Pool[]>([])
  let volumes = $state<PoolVolume[]>([])
  let networks = $state<VNetwork[]>([])

  // Form state, reset whenever a dialog opens.
  let newName = $state('')
  let removeStorage = $state(false)
  let diskMode = $state<'new' | 'existing' | 'cdrom'>('new')
  let diskGiB = $state(20)
  let diskPool = $state('default')
  let diskBus = $state('virtio')
  let existing = $state('')
  let isoPath = $state('')
  let nicChoice = $state('')
  let nicModel = $state('virtio')
  let bridge = $state('br0')

  $effect(() => {
    if (!which) return
    error = ''
    busy = false
    newName = which === 'clone' ? `${detail.name}-clone` : detail.name
    removeStorage = false
    diskMode = 'new'
    existing = ''
    isoPath = ''
    if (which === 'disk' || which === 'media') {
      void (async () => {
        try {
          pools = await activePools()
          if (!pools.some((p) => p.name === diskPool) && pools[0]) diskPool = pools[0].name
          volumes = await allVolumes(pools)
        } catch (e) {
          error = errorText(e)
        }
      })()
    }
    if (which === 'nic') {
      void api
        .networks()
        .then((n) => {
          networks = n
          nicChoice = n.some((x) => x.name === 'default') ? 'network:default' : n[0] ? `network:${n[0].name}` : 'bridge'
        })
        .catch((e) => (error = errorText(e)))
    }
  })

  let ownDisks = $derived(detail.devices.filter((d) => d.kind === 'disk'))
  let isos = $derived(volumes.filter((v) => isISO(v.vol)))
  let freeDisks = $derived(volumes.filter((v) => !isISO(v.vol) && v.vol.usedBy.length === 0))

  async function run(fn: () => Promise<void>) {
    busy = true
    error = ''
    try {
      await fn()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  const rename = () =>
    run(async () => {
      await api.rename(detail.uuid, newName)
      toasts.push({ kind: 'info', title: `Renamed to ${newName}` })
      onchange()
      onclose()
    })

  const clone = () =>
    run(async () => {
      const job = await api.clone(detail.uuid, newName)
      onclose()
      track(job, {
        success: `${newName} is ready`,
        onSuccess: (j) => j.result && router.navigate(machineHref(j.result)),
      })
    })

  const remove = () =>
    run(async () => {
      const res = await api.remove(detail.uuid, removeStorage)
      const kept = res.keptVolumes.map((k) => `${k.path} (${k.reason})`).join(', ')
      toasts.push({
        kind: kept ? 'warn' : 'info',
        title: `${detail.name} deleted`,
        detail: res.removedVolumes.length
          ? `Removed ${res.removedVolumes.length} disk${res.removedVolumes.length > 1 ? 's' : ''}.${kept ? ' Kept: ' + kept : ''}`
          : kept
            ? 'Kept: ' + kept
            : undefined,
      })
      onclose()
      router.navigate('/')
    })

  const addDisk = () =>
    run(async () => {
      let res: { target: string }
      if (diskMode === 'new') res = await api.attachDisk(detail.uuid, { pool: diskPool, sizeGiB: diskGiB, bus: diskBus })
      else if (diskMode === 'existing') res = await api.attachDisk(detail.uuid, { path: existing, bus: diskBus })
      else res = await api.attachDisk(detail.uuid, { device: 'cdrom', path: isoPath || undefined })
      toasts.push({
        kind: 'info',
        title: `Added ${res.target}`,
        detail: active ? 'Hot-plugged into the running machine.' : undefined,
      })
      onchange()
      onclose()
    })

  const addNIC = () =>
    run(async () => {
      const spec = nicChoice === 'bridge' ? { bridge, model: nicModel } : { network: nicChoice.slice(8), model: nicModel }
      await api.attachNIC(detail.uuid, spec)
      toasts.push({ kind: 'info', title: 'Network adapter added' })
      onchange()
      onclose()
    })

  const changeMedia = (source: string) =>
    run(async () => {
      if (!drive?.target) return
      await api.changeMedia(detail.uuid, drive.target, source)
      toasts.push({ kind: 'info', title: source ? `Inserted ${source.split('/').pop()}` : `Ejected ${drive.title}` })
      onchange()
      onclose()
    })

  let nameOK = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(newName))
</script>

<Dialog open={which === 'rename'} title="Rename {detail.name}" {onclose}>
  <form id="rename" class="form" onsubmit={(e) => (e.preventDefault(), rename())}>
    <label class="ui-field">
      <span class="label">New name</span>
      <input class="ui-input" bind:value={newName} aria-invalid={!nameOK} spellcheck="false" />
      {#if active}<span class="error">Shut the machine down first: libvirt renames stopped machines only.</span>{/if}
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="rename" disabled={busy || !nameOK || newName === detail.name || active}>Rename</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'clone'} title="Clone {detail.name}" subtitle="A full, independent copy with new identity" {onclose}>
  <form id="clone" class="form" onsubmit={(e) => (e.preventDefault(), clone())}>
    <label class="ui-field">
      <span class="label">Name of the copy</span>
      <input class="ui-input" bind:value={newName} aria-invalid={!nameOK} spellcheck="false" />
    </label>
    <ul class="facts">
      <li>Every disk is copied in full ({ownDisks.length} disk{ownDisks.length === 1 ? '' : 's'}, {formatBytes(ownDisks.reduce((n, d) => n + (d.capacityBytes ?? 0), 0))} virtual size). This can take a while; you can keep working.</li>
      <li>The copy gets a new UUID, new MAC addresses, fresh UEFI variables and TPM.</li>
      <li>CD/DVD media and shared or read-only disks are not copied, just referenced.</li>
    </ul>
    {#if active}<div class="ui-error-box">Shut {detail.name} down first: a running machine's disks cannot be copied consistently.</div>{/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="clone" disabled={busy || !nameOK || active}>Start cloning</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'delete'} title="Delete {detail.name}?" {onclose}>
  <div class="form">
    {#if active}
      <div class="ui-error-box">Stop the machine first.</div>
    {:else}
      <p class="text">The machine's definition, snapshots, UEFI variables and TPM state are removed. This cannot be undone.</p>
      {#if ownDisks.length}
        <label class="ui-switch danger-switch"><input type="checkbox" bind:checked={removeStorage} /> Also delete its disks</label>
        <ul class="disks">
          {#each ownDisks as d (d.title)}
            <li class:gone={removeStorage}>
              <span class="mono">{d.props.find((p) => p.label === 'Source')?.value ?? d.title}</span>
              {#if d.capacityBytes}<span class="muted">{formatBytes(d.capacityBytes)}</span>{/if}
            </li>
          {/each}
        </ul>
        <p class="hint">{removeStorage ? 'Disks used by other machines or outside a storage pool are kept.' : 'The disk images stay in their pool and can be attached to another machine.'}</p>
      {/if}
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn danger" onclick={remove} disabled={busy || active}>
      {busy ? 'Deleting…' : removeStorage ? 'Delete machine and disks' : 'Delete machine'}
    </button>
  {/snippet}
</Dialog>

<Dialog open={which === 'disk'} title="Add storage" subtitle={active ? 'Hot-plugged into the running machine and kept for the next boot' : undefined} {onclose} width={520}>
  <form id="disk" class="form" onsubmit={(e) => (e.preventDefault(), addDisk())}>
    <div class="seg" role="radiogroup" aria-label="What to add">
      <label class:on={diskMode === 'new'}><input type="radio" bind:group={diskMode} value="new" /> New disk</label>
      <label class:on={diskMode === 'existing'}><input type="radio" bind:group={diskMode} value="existing" /> Existing volume</label>
      <label class:on={diskMode === 'cdrom'}><input type="radio" bind:group={diskMode} value="cdrom" /> CD/DVD drive</label>
    </div>
    {#if diskMode === 'new'}
      <div class="two">
        <label class="ui-field">
          <span class="label">Size (GiB)</span>
          <input class="ui-input" type="number" min="1" max="65536" bind:value={diskGiB} />
        </label>
        <label class="ui-field">
          <span class="label">Pool</span>
          <select class="ui-input" bind:value={diskPool}>
            {#each pools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
          </select>
        </label>
      </div>
    {:else if diskMode === 'existing'}
      <label class="ui-field">
        <span class="label">Volume</span>
        <select class="ui-input" bind:value={existing} required>
          <option value="" disabled>Choose an unused volume…</option>
          {#each freeDisks as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}, {formatBytes(v.vol.capacityBytes)}</option>{/each}
        </select>
        {#if freeDisks.length === 0}<span class="hint">No unused disk volumes. Volumes attached to a machine are not listed.</span>{/if}
      </label>
    {:else}
      <label class="ui-field">
        <span class="label">Insert</span>
        <select class="ui-input" bind:value={isoPath}>
          <option value="">Nothing (empty drive)</option>
          {#each isos as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}</option>{/each}
        </select>
        {#if active}<span class="hint">SATA CD/DVD drives usually cannot be hot-plugged; add it while the machine is off.</span>{/if}
      </label>
    {/if}
    {#if diskMode !== 'cdrom'}
      <label class="ui-field">
        <span class="label">Bus</span>
        <select class="ui-input" bind:value={diskBus}>
          <option value="virtio">VirtIO (fastest, needs drivers on Windows)</option>
          <option value="sata">SATA (works everywhere)</option>
          <option value="scsi">SCSI</option>
          <option value="usb">USB</option>
        </select>
      </label>
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="disk" disabled={busy || (diskMode === 'existing' && !existing)}>{busy ? 'Adding…' : 'Add'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'nic'} title="Add network adapter" {onclose}>
  <form id="nic" class="form" onsubmit={(e) => (e.preventDefault(), addNIC())}>
    <label class="ui-field">
      <span class="label">Connect to</span>
      <select class="ui-input" bind:value={nicChoice}>
        {#each networks as n (n.name)}<option value="network:{n.name}">{n.name} ({n.mode}{n.active ? '' : ', stopped'})</option>{/each}
        <option value="bridge">A host bridge</option>
      </select>
    </label>
    {#if nicChoice === 'bridge'}
      <label class="ui-field"><span class="label">Bridge</span><input class="ui-input mono" bind:value={bridge} /></label>
    {/if}
    <label class="ui-field">
      <span class="label">Adapter model</span>
      <select class="ui-input" bind:value={nicModel}>
        <option value="virtio">VirtIO (fastest)</option>
        <option value="e1000e">Intel e1000e (works without drivers)</option>
        <option value="rtl8139">Realtek RTL8139 (very old guests)</option>
      </select>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="nic" disabled={busy || !nicChoice}>{busy ? 'Adding…' : 'Add adapter'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'media'} title="{drive?.title ?? 'Drive'}: media" subtitle={drive?.summary} {onclose}>
  <div class="form">
    <label class="ui-field">
      <span class="label">Insert image</span>
      <select class="ui-input" bind:value={isoPath}>
        <option value="" disabled>Choose an ISO…</option>
        {#each isos as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}</option>{/each}
      </select>
      {#if isos.length === 0}<span class="hint">No ISO images. <a href="/storage" onclick={router.link}>Upload one</a> in Storage.</span>{/if}
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    {#if drive?.summary && !drive.summary.endsWith('Empty')}
      <button class="ui-btn ghost eject" onclick={() => changeMedia('')} disabled={busy}>Eject</button>
    {/if}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" onclick={() => changeMedia(isoPath)} disabled={busy || !isoPath}>Insert</button>
  {/snippet}
</Dialog>

<style>
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
  .text {
    margin: 0;
    color: var(--text-2);
    font-size: 13.5px;
  }
  .facts {
    margin: 0;
    padding-left: 18px;
    color: var(--text-2);
    font-size: 13px;
    display: grid;
    gap: 6px;
  }
  .disks {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 4px;
  }
  .disks li {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 10px;
    border: 1px solid var(--border);
    border-radius: 7px;
    font-size: 12.5px;
    transition:
      border-color var(--dur-fast),
      background var(--dur-fast);
  }
  .disks li.gone {
    border-color: color-mix(in srgb, var(--danger) 45%, transparent);
    background: var(--danger-soft);
    text-decoration: line-through;
  }
  .danger-switch :global(input:checked) {
    background: var(--danger);
  }
  .muted {
    color: var(--text-3);
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 12px;
  }
  .hint a {
    color: var(--accent);
  }
  .error {
    color: var(--warn);
    font-size: 12px;
  }
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
  }
  .seg label {
    flex: 1;
    display: grid;
    place-items: center;
    height: 30px;
    border-radius: 7px;
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
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .seg label:focus-within {
    outline: 2px solid var(--accent);
  }
  .eject {
    margin-right: auto;
  }
</style>
