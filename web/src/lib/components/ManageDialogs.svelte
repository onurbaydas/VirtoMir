<script lang="ts" module>
  export type ManageDialog = 'rename' | 'clone' | 'delete' | 'disk' | 'nic' | 'media' | 'resize' | 'template' | 'move' | 'hostdev' | null
</script>

<script lang="ts">
  // The machine page's dialogs: rename, clone, delete, add disk, add adapter, change media.
  import { api, errorText, type DomainDetail, type Device, type Pool, type VNetwork } from '../api'
  import { track } from '../jobs'
  import { activePools, allVolumes, isISO, type PoolVolume } from '../media'
  import { formatBytes } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { toasts } from '../toast.svelte'
  import { cluster } from '../cluster.svelte'
  import Dialog from './Dialog.svelte'
  import type { HostHardware } from '../api'
  import DestructiveGuardrailModal from './DestructiveGuardrailModal.svelte'

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
  let nicVlan = $state<number | null>(null)
  let bridge = $state('br0')
  let growGiB = $state(0)
  let hw = $state<HostHardware | null>(null)
  let hdKind = $state<'pci' | 'usb'>('pci')
  let hdPick = $state('')
  let moveHost = $state('')
  let movePool = $state('')
  let targetPools = $state<Pool[] | null>(null)
  let targets = $derived(cluster.members.filter((m) => m.id !== cluster.current?.id && m.reachable))

  $effect(() => {
    if (!which) return
    error = ''
    busy = false
    newName = which === 'clone' ? `${detail.name}-clone` : detail.name
    removeStorage = false
    diskMode = 'new'
    existing = ''
    isoPath = ''
    growGiB = drive?.capacityBytes ? Math.ceil(drive.capacityBytes / 2 ** 30) + 10 : 20
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
    if (which === 'hostdev') {
      hw = null
      hdPick = ''
      api
        .hostDevices()
        .then((h) => (hw = h))
        .catch((e) => (error = errorText(e)))
    }
    if (which === 'move') {
      moveHost = targets[0]?.id ?? ''
      targetPools = null
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

  const makeTemplate = () =>
    run(async () => {
      await api.setTemplate(detail.uuid, true)
      toasts.push({ kind: 'info', title: `${detail.name} is now a template`, detail: 'Use "New machine" to create machines from it.' })
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

  // Pools of the chosen target host (for copying a stopped machine's disks).
  $effect(() => {
    if (which !== 'move' || !moveHost) return
    const host = moveHost
    targetPools = null
    api
      .poolsOn(host)
      .then((p) => {
        if (host !== moveHost) return
        targetPools = p.filter((x) => x.active && ['dir', 'netfs', 'fs'].includes(x.type))
        movePool = (targetPools.find((x) => x.name === 'default') ?? targetPools[0])?.name ?? ''
      })
      .catch((e) => (error = errorText(e)))
  })

  const move = () =>
    run(async () => {
      const target = cluster.members.find((m) => m.id === moveHost)!
      const job = await api.move(detail.uuid, moveHost, movePool)
      onclose()
      track(job, {
        success: `${detail.name} is now on ${target.name}`,
        onSuccess: () => router.go(target.self ? null : target.id, `/machines/${detail.uuid}`),
      })
    })

  const usbKey = (v: string, p: string) => `${v.replace('0x', '')}:${p.replace('0x', '')}`
  const attachHostdev = () =>
    run(async () => {
      const spec =
        hdKind === 'pci'
          ? { type: 'pci' as const, address: hdPick }
          : { type: 'usb' as const, vendorId: hdPick.split(':')[0], productId: hdPick.split(':')[1] }
      const r = await api.attachHostdev(detail.uuid, spec)
      toasts.push({
        kind: r.liveError ? 'warn' : 'info',
        title: 'Host device added',
        detail: r.liveError
          ? `Saved for the next start; it could not be added live: ${r.liveError}`
          : active
            ? 'Added live and for the next start.'
            : 'The machine gets it from its next start.',
      })
      onchange()
      onclose()
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
      const base = nicChoice === 'bridge' ? { bridge, model: nicModel } : { network: nicChoice.slice(8), model: nicModel }
      const spec = nicVlan ? { ...base, vlan: Number(nicVlan) } : base
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

  let currentGiB = $derived(drive?.capacityBytes ? drive.capacityBytes / 2 ** 30 : 0)
  const grow = () =>
    run(async () => {
      if (!drive?.target) return
      await api.resizeDisk(detail.uuid, drive.target, growGiB)
      toasts.push({
        kind: 'info',
        title: `${drive.title} is now ${growGiB} GiB`,
        detail: 'Grow the partition and file system inside the guest to use the space (e.g. growpart + resize2fs, or Disk Management on Windows).',
      })
      onchange()
      onclose()
    })

  let nameOK = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(newName))
</script>

<DestructiveGuardrailModal
  open={which === 'delete'}
  targetName={detail.name}
  targetUuid={detail.uuid}
  onconfirm={(opts) => {
    removeStorage = opts.purgeDisks
    remove()
  }}
  {onclose}
/>

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

<Dialog open={which === 'template'} title="Make {detail.name} a template" subtitle="A master copy that new machines are made from" {onclose}>
  <div class="form">
    <ul class="facts">
      <li>It will not start anymore: its <b>Start</b> button becomes <b>New machine</b>.</li>
      <li>New machines can be <b>linked clones</b> (ready in seconds, store only their own changes) or full copies.</li>
      <li>You can turn it back into a normal machine as long as no linked clone depends on it.</li>
    </ul>
    <div class="prep">
      <span class="prep-title">Before you do: make the system anonymous, then shut it down</span>
      <span>Otherwise every copy shares its identity (machine ID, SSH host keys).</span>
      <span>Linux: <code class="mono">sudo cloud-init clean --logs --machine-id</code> — or <code class="mono">sudo truncate -s0 /etc/machine-id</code> without cloud-init.</span>
      <span>Windows: <code class="mono">sysprep /generalize /oobe /shutdown</code></span>
    </div>
    {#if active}<div class="ui-error-box">Shut {detail.name} down first.</div>{/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" disabled={busy || active} onclick={makeTemplate}>Make template</button>
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

<Dialog open={which === 'move'} title="Move {detail.name}" subtitle={active ? 'Live migration: it keeps running' : 'To another host of this cluster'} {onclose}>
  <form id="move" class="form" onsubmit={(e) => (e.preventDefault(), move())}>
    {#if targets.length === 0}
      <div class="ui-error-box">No other host of the cluster is reachable right now.</div>
    {:else}
      <label class="ui-field">
        <span class="label">Target host</span>
        <select class="ui-input" bind:value={moveHost}>
          {#each targets as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
        </select>
      </label>
      {#if !active}
        <label class="ui-field">
          <span class="label">Storage pool there</span>
          <select class="ui-input" bind:value={movePool} disabled={!targetPools?.length}>
            {#each targetPools ?? [] as p (p.name)}<option value={p.name}>{p.name} · {formatBytes(p.availableBytes)} free</option>{/each}
          </select>
          {#if targetPools && !targetPools.length}<span class="hint">That host has no started storage pool for image files.</span>{/if}
        </label>
        <ul class="facts">
          <li>Its disks are copied there ({ownDisks.length} disk{ownDisks.length === 1 ? '' : 's'}); you can keep working meanwhile.</li>
          <li>Only when it is defined there is it removed here — a failed move changes nothing on this host.</li>
          <li>CD/DVD media is ejected. Snapshots cannot travel: delete them first.</li>
        </ul>
      {:else}
        <ul class="facts">
          <li>Memory is copied while the machine keeps running; it pauses only for a moment at the very end.</li>
          <li>Disks on storage both hosts share (an NFS pool with the same path) are not copied. Other disks are copied live into a pool with the same path there.</li>
          <li>Both hosts should have the same CPU type, or the guest may not survive the move.</li>
        </ul>
      {/if}
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="move" disabled={busy || !moveHost || (!active && !movePool)}>{active ? 'Migrate live' : 'Move'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'hostdev'} title="Add a host device" subtitle="Give {detail.name} a PCI or USB device of this host" {onclose} width={620}>
  <div class="form">
    <div class="seg" role="radiogroup" aria-label="Kind">
      <button type="button" role="radio" aria-checked={hdKind === 'pci'} class:on={hdKind === 'pci'} onclick={() => ((hdKind = 'pci'), (hdPick = ''))}>PCI (GPU, NIC, controller)</button>
      <button type="button" role="radio" aria-checked={hdKind === 'usb'} class:on={hdKind === 'usb'} onclick={() => ((hdKind = 'usb'), (hdPick = ''))}>USB</button>
    </div>
    {#if !hw}
      <p class="text">Looking at the host's hardware…</p>
    {:else}
      {#if hdKind === 'pci' && !hw.iommu}
        <div class="ui-error-box">The IOMMU is off on this host, so no PCI device can be passed through. Turn on VT-d/AMD-Vi in the firmware and add <span class="mono">intel_iommu=on</span> (or <span class="mono">amd_iommu=on</span>) to the kernel command line.</div>
      {/if}
      <div class="hd-list" role="radiogroup">
        {#if hdKind === 'pci'}
          {#each hw.pci.filter((p) => !p.class.toLowerCase().startsWith('0x06')) as p (p.address)}
            <label class="hd" class:off={!!p.blocked || p.usedBy.length > 0}>
              <input type="radio" name="hd" value={p.address} bind:group={hdPick} disabled={!!p.blocked || p.usedBy.length > 0} />
              <span class="hd-text">
                <span><b>{p.product || p.productId}</b> <span class="muted">{p.vendor}</span></span>
                <span class="hd-meta mono">{p.address} · {p.className || p.class}{p.driver ? ' · ' + p.driver : ''}</span>
                {#if p.blocked}<span class="hd-why">{p.blocked}</span>{:else if p.usedBy.length}<span class="hd-why">Given to {p.usedBy.join(', ')}</span>{/if}
              </span>
            </label>
          {/each}
        {:else}
          {#each hw.usb as u (u.name)}
            <label class="hd" class:off={!!u.blocked || u.usedBy.length > 0}>
              <input type="radio" name="hd" value={usbKey(u.vendorId, u.productId)} bind:group={hdPick} disabled={!!u.blocked || u.usedBy.length > 0} />
              <span class="hd-text">
                <span><b>{u.product || usbKey(u.vendorId, u.productId)}</b> <span class="muted">{u.vendor}</span></span>
                <span class="hd-meta mono">bus {u.bus} · device {u.device} · {usbKey(u.vendorId, u.productId)}</span>
                {#if u.blocked}<span class="hd-why">{u.blocked}</span>{:else if u.usedBy.length}<span class="hd-why">Given to {u.usedBy.join(', ')}</span>{/if}
              </span>
            </label>
          {:else}
            <p class="text">No USB devices are connected to this host.</p>
          {/each}
        {/if}
      </div>
      {#if hdKind === 'pci'}<p class="text muted">The device is taken away from the host while the machine has it; all devices of its IOMMU group go together.</p>{/if}
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" disabled={busy || !hdPick} onclick={attachHostdev}>Add device</button>
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
    <label class="ui-field">
      <span class="label">VLAN <span class="muted">(optional)</span></span>
      <input class="ui-input mono" type="number" min="1" max="4094" bind:value={nicVlan} placeholder="as the network says" />
      <span class="hint">Works on Open vSwitch networks, macvtap passthrough and SR-IOV adapters.</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="nic" disabled={busy || !nicChoice}>{busy ? 'Adding…' : 'Add adapter'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'resize'} title="Grow {drive?.title ?? 'disk'}" subtitle={drive?.summary} {onclose}>
  <form id="grow" class="form" onsubmit={(e) => (e.preventDefault(), grow())}>
    <label class="ui-field">
      <span class="label">New size (GiB)</span>
      <input class="ui-input" type="number" min={Math.floor(currentGiB) + 1} max="65536" bind:value={growGiB} />
      <span class="hint">Currently {currentGiB ? `${currentGiB.toFixed(1)} GiB` : 'unknown'}. Disks can only grow — shrinking would destroy data.{active ? ' The running machine sees the new size immediately.' : ''}</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="grow" disabled={busy || growGiB <= currentGiB}>{busy ? 'Growing…' : 'Grow disk'}</button>
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
  .seg {
    display: flex;
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
  }
  .hd-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 46vh;
    overflow-y: auto;
  }
  .hd {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .hd.off {
    opacity: 0.65;
    cursor: not-allowed;
  }
  .hd input {
    margin-top: 3px;
  }
  .hd-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .hd-meta {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .hd-why {
    font-size: 12px;
    color: var(--warn);
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
  .text {
    margin: 0;
    color: var(--text-2);
    font-size: 13.5px;
  }
  .prep {
    display: grid;
    gap: 4px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
  }
  .prep-title {
    color: var(--text);
    font-weight: 600;
  }
  .prep code {
    user-select: all;
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
    border-radius: var(--radius-sm);
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
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
  }
  .seg label {
    flex: 1;
    display: grid;
    place-items: center;
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
  .eject {
    margin-right: auto;
  }
</style>
