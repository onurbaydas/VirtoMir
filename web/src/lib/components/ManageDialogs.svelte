<script lang="ts" module>
  export type ManageDialog = 'rename' | 'clone' | 'delete' | 'disk' | 'nic' | 'media' | 'resize' | 'diskmove' | 'qos' | 'bandwidth' | 'template' | 'move' | 'hostdev' | null
</script>


<script lang="ts">
  import { untrack } from 'svelte'
  import { t } from '../i18n.svelte'
  // The machine page's dialogs: rename, clone, delete, add disk, add adapter, change media.
  import { api, errorText, type DomainDetail, type Device, type Pool, type VNetwork } from '../api'
  import { track } from '../jobs'
  import { TIERS, eff, type Tier } from '../storageeff'
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
  let bwIn = $state(0)
  let bwOut = $state(0)
  let qosTotalIops = $state(0)
  let qosReadIops = $state(0)
  let qosWriteIops = $state(0)
  let qosTotalMbps = $state(0)
  let qosReadMbps = $state(0)
  let qosWriteMbps = $state(0)
  let hw = $state<HostHardware | null>(null)
  let hdKind = $state<'pci' | 'usb'>('pci')
  let hdPick = $state('')
  let moveHost = $state('')
  let movePool = $state('')
  let targetPools = $state<Pool[] | null>(null)
  let targets = $derived(cluster.members.filter((m) => m.id !== cluster.current?.id && m.reachable))

  // Reset only when a dialog opens: the page reloads `detail` every few seconds, and that
  // must not wipe what the user is typing.
  $effect(() => {
    if (which) untrack(() => reset(which))
  })

  function reset(which: NonNullable<ManageDialog>) {
    error = ''
    busy = false
    newName = which === 'clone' ? `${detail.name}-clone` : detail.name
    removeStorage = false
    diskMode = 'new'
    existing = ''
    isoPath = ''
    growGiB = drive?.capacityBytes ? Math.ceil(drive.capacityBytes / 2 ** 30) + 10 : 20
    if (which === 'qos') {
      qosTotalIops = drive?.iotune?.totalIopsSec ?? 0
      qosReadIops = drive?.iotune?.readIopsSec ?? 0
      qosWriteIops = drive?.iotune?.writeIopsSec ?? 0
      qosTotalMbps = drive?.iotune?.totalBytesSec ? Math.round(drive.iotune.totalBytesSec / (1024 * 1024)) : 0
      qosReadMbps = drive?.iotune?.readBytesSec ? Math.round(drive.iotune.readBytesSec / (1024 * 1024)) : 0
      qosWriteMbps = drive?.iotune?.writeBytesSec ? Math.round(drive.iotune.writeBytesSec / (1024 * 1024)) : 0
    }
    if (which === 'bandwidth') {
      bwIn = drive?.bandwidth?.inMbps ?? 0
      bwOut = drive?.bandwidth?.outMbps ?? 0
    }
    if (which === 'diskmove') {
      moveBy = 'pool'
      void Promise.all([activePools(), eff.tiers().catch(() => ({}) as Record<string, Tier>)])
        .then(([p, tiers]) => {
          pools = p.filter((x) => ['dir', 'netfs', 'fs'].includes(x.type))
          diskPool = pools[0]?.name ?? ''
          poolTiers = tiers
          moveTier = TIERS.find((x) => pools.some((pl) => tiers[pl.name] === x)) ?? 'nvme'
        })
        .catch((e) => (error = errorText(e)))
    }
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
  }

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
      toasts.push({ kind: 'info', title: t('{name} is now a template', { name: detail.name }), detail: t('Use "New machine" to create machines from it.') })
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
        title: t('Host device added'),
        detail: r.liveError
          ? t('Saved for the next start; it could not be added live: {liveError}', { liveError: r.liveError })
          : active
            ? 'Added live and for the next start.'
            : t('The machine gets it from its next start.'),
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
      toasts.push({ kind: 'info', title: t('Network adapter added') })
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

  const saveBandwidth = () =>
    run(async () => {
      if (!drive?.mac) return
      await api.setInterfaceBandwidth(detail.uuid, drive.mac, { inMbps: bwIn || 0, outMbps: bwOut || 0 })
      toasts.push({ kind: 'info', title: t('Adapter limits saved') })
      onchange()
      onclose()
    })

  // Storage tiers (ADR-0074): a disk can go to a tier; the host picks its pool with room.
  let moveBy = $state<'pool' | 'tier'>('pool')
  let moveTier = $state<Tier>('nvme')
  let poolTiers = $state<Record<string, Tier>>({})
  let tiersHere = $derived(TIERS.filter((x) => pools.some((p) => poolTiers[p.name] === x)))
  const moveDisk = () =>
    run(async () => {
      if (!drive?.target) return
      const job = moveBy === 'tier' ? await eff.moveToTier(detail.uuid, drive.target, moveTier) : await api.moveDisk(detail.uuid, drive.target, diskPool)
      onclose()
      track(job, { success: t('{title} moved to {pool}', { title: drive.title, pool: moveBy === 'tier' ? moveTier.toUpperCase() : diskPool }), onSuccess: () => onchange() })
    })

  let currentGiB = $derived(drive?.capacityBytes ? drive.capacityBytes / 2 ** 30 : 0)
  const grow = () =>
    run(async () => {
      if (!drive?.target) return
      await api.resizeDisk(detail.uuid, drive.target, growGiB)
      toasts.push({
        kind: 'info',
        title: t('{title} is now {growGiB} GiB', { title: drive.title, growGiB: growGiB }),
        detail: t('Grow the partition and file system inside the guest to use the space (e.g. growpart + resize2fs, or Disk Management on Windows).'),
      })
      onchange()
      onclose()
    })

  const saveQoS = () =>
    run(async () => {
      if (!drive?.target) return
      await api.setDiskQoS(detail.uuid, drive.target, {
        totalIopsSec: qosTotalIops,
        readIopsSec: qosReadIops,
        writeIopsSec: qosWriteIops,
        totalBytesSec: qosTotalMbps > 0 ? qosTotalMbps * 1024 * 1024 : 0,
        readBytesSec: qosReadMbps > 0 ? qosReadMbps * 1024 * 1024 : 0,
        writeBytesSec: qosWriteMbps > 0 ? qosWriteMbps * 1024 * 1024 : 0,
      })
      toasts.push({
        kind: 'info',
        title: t('Storage QoS updated for {title}', { title: drive.title }),
        detail: t('Limits applied to QEMU block device and written to configuration.'),
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
  {busy}
  {error}
  blocked={active ? t('Stop the machine first.') : ''}
  {onclose}
/>

<Dialog open={which === 'rename'} title={t('Rename {name}', { name: detail.name })} {onclose}>
  <form id="rename" class="form" onsubmit={(e) => (e.preventDefault(), rename())}>
    <label class="ui-field">
      <span class="label">{t('New name')}</span>
      <input class="ui-input" bind:value={newName} aria-invalid={!nameOK} spellcheck="false" />
      {#if active}<span class="error">{t('Shut the machine down first: libvirt renames stopped machines only.')}</span>{/if}
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="rename" disabled={busy || !nameOK || newName === detail.name || active}>{t('Rename')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'template'} title={t('Make {name} a template', { name: detail.name })} subtitle={t('A master copy that new machines are made from')} {onclose}>
  <div class="form">
    <ul class="facts">
      <li>{t('It will not start anymore: its')} <b>{t('Start')}</b> {t('button becomes')} <b>{t('New machine')}</b>.</li>
      <li>{t('New machines can be')} <b>{t('linked clones')}</b> {t('(ready in seconds, store only their own changes) or full copies.')}</li>
      <li>{t('You can turn it back into a normal machine as long as no linked clone depends on it.')}</li>
    </ul>
    <div class="prep">
      <span class="prep-title">{t('Before you do: make the system anonymous, then shut it down')}</span>
      <span>{t('Otherwise every copy shares its identity (machine ID, SSH host keys).')}</span>
      <span>{t('Linux:')} <code class="mono">sudo cloud-init clean --logs --machine-id</code> {t('— or')} <code class="mono">sudo truncate -s0 /etc/machine-id</code> {t('without cloud-init.')}</span>
      <span>{t('Windows:')} <code class="mono">sysprep /generalize /oobe /shutdown</code></span>
    </div>
    {#if active}<div class="ui-error-box">{t('Shut {name} down first.', { name: detail.name })}</div>{/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={busy || active} onclick={makeTemplate}>{t('Make template')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'clone'} title={t('Clone {name}', { name: detail.name })} subtitle={t('A full, independent copy with new identity')} {onclose}>
  <form id="clone" class="form" onsubmit={(e) => (e.preventDefault(), clone())}>
    <label class="ui-field">
      <span class="label">{t('Name of the copy')}</span>
      <input class="ui-input" bind:value={newName} aria-invalid={!nameOK} spellcheck="false" />
    </label>
    <ul class="facts">
      <li>{t('Every disk is copied in full ({length} disk{v}, {capacityBytes} virtual size). This can take a while; you can keep working.', { length: ownDisks.length, v: ownDisks.length === 1 ? '' : 's', capacityBytes: formatBytes(ownDisks.reduce((n, d) => n + (d.capacityBytes ?? 0), 0)) })}</li>
      <li>{t('The copy gets a new UUID, new MAC addresses, fresh UEFI variables and TPM.')}</li>
      <li>{t('CD/DVD media and shared or read-only disks are not copied, just referenced.')}</li>
    </ul>
    {#if active}<div class="ui-error-box">{t('Shut {name} down first: a running machine\'s disks cannot be copied consistently.', { name: detail.name })}</div>{/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="clone" disabled={busy || !nameOK || active}>{t('Start cloning')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'move'} title={t('Move {name}', { name: detail.name })} subtitle={active ? 'Live migration: it keeps running' : t('To another host of this cluster')} {onclose}>
  <form id="move" class="form" onsubmit={(e) => (e.preventDefault(), move())}>
    {#if targets.length === 0}
      <div class="ui-error-box">{t('No other host of the cluster is reachable right now.')}</div>
    {:else}
      <label class="ui-field">
        <span class="label">{t('Target host')}</span>
        <select class="ui-input" bind:value={moveHost}>
          {#each targets as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
        </select>
      </label>
      {#if !active}
        <label class="ui-field">
          <span class="label">{t('Storage pool there')}</span>
          <select class="ui-input" bind:value={movePool} disabled={!targetPools?.length}>
            {#each targetPools ?? [] as p (p.name)}<option value={p.name}>{p.name} · {formatBytes(p.availableBytes)} free</option>{/each}
          </select>
          {#if targetPools && !targetPools.length}<span class="hint">{t('That host has no started storage pool for image files.')}</span>{/if}
        </label>
        <ul class="facts">
          <li>{t('Its disks are copied there ({length} disk{v}); you can keep working meanwhile.', { length: ownDisks.length, v: ownDisks.length === 1 ? '' : 's' })}</li>
          <li>{t('Only when it is defined there is it removed here — a failed move changes nothing on this host.')}</li>
          <li>{t('CD/DVD media is ejected. Snapshots cannot travel: delete them first.')}</li>
        </ul>
      {:else}
        <ul class="facts">
          <li>{t('Memory is copied while the machine keeps running; it pauses only for a moment at the very end.')}</li>
          <li>{t('Disks on storage both hosts share (an NFS pool with the same path) are not copied. Other disks are copied live into a pool with the same path there.')}</li>
          <li>{t('Both hosts should have the same CPU type, or the guest may not survive the move.')}</li>
        </ul>
      {/if}
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="move" disabled={busy || !moveHost || (!active && !movePool)}>{active ? 'Migrate live' : 'Move'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'hostdev'} title={t('Add a host device')} subtitle={t('Give {name} a PCI or USB device of this host', { name: detail.name })} {onclose} width={620}>
  <div class="form">
    <div class="seg" role="radiogroup" aria-label={t('Kind')}>
      <button type="button" role="radio" aria-checked={hdKind === 'pci'} class:on={hdKind === 'pci'} onclick={() => ((hdKind = 'pci'), (hdPick = ''))}>{t('PCI (GPU, NIC, controller)')}</button>
      <button type="button" role="radio" aria-checked={hdKind === 'usb'} class:on={hdKind === 'usb'} onclick={() => ((hdKind = 'usb'), (hdPick = ''))}>{t('USB')}</button>
    </div>
    {#if !hw}
      <p class="text">{t('Looking at the host\'s hardware…')}</p>
    {:else}
      {#if hdKind === 'pci' && !hw.iommu}
        <div class="ui-error-box">{t('The IOMMU is off on this host, so no PCI device can be passed through. Turn on VT-d/AMD-Vi in the firmware and add')} <span class="mono">{t('intel_iommu=on')}</span> {t('(or')} <span class="mono">{t('amd_iommu=on')}</span>{t(') to the kernel command line.')}</div>
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
                <span class="hd-meta mono">{t('bus {bus} · device {device} · {productId}', { bus: u.bus, device: u.device, productId: usbKey(u.vendorId, u.productId) })}</span>
                {#if u.blocked}<span class="hd-why">{u.blocked}</span>{:else if u.usedBy.length}<span class="hd-why">Given to {u.usedBy.join(', ')}</span>{/if}
              </span>
            </label>
          {:else}
            <p class="text">{t('No USB devices are connected to this host.')}</p>
          {/each}
        {/if}
      </div>
      {#if hdKind === 'pci'}<p class="text muted">{t('The device is taken away from the host while the machine has it; all devices of its IOMMU group go together.')}</p>{/if}
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={busy || !hdPick} onclick={attachHostdev}>{t('Add device')}</button>
  {/snippet}
</Dialog>


<Dialog open={which === 'disk'} title={t('Add storage')} subtitle={active ? 'Hot-plugged into the running machine and kept for the next boot' : undefined} {onclose} width={520}>
  <form id="disk" class="form" onsubmit={(e) => (e.preventDefault(), addDisk())}>
    <div class="seg" role="radiogroup" aria-label={t('What to add')}>
      <label class:on={diskMode === 'new'}><input type="radio" bind:group={diskMode} value="new" /> {t('New disk')}</label>
      <label class:on={diskMode === 'existing'}><input type="radio" bind:group={diskMode} value="existing" /> {t('Existing volume')}</label>
      <label class:on={diskMode === 'cdrom'}><input type="radio" bind:group={diskMode} value="cdrom" /> {t('CD/DVD drive')}</label>
    </div>
    {#if diskMode === 'new'}
      <div class="two">
        <label class="ui-field">
          <span class="label">{t('Size (GiB)')}</span>
          <input class="ui-input" type="number" min="1" max="65536" bind:value={diskGiB} />
        </label>
        <label class="ui-field">
          <span class="label">{t('Pool')}</span>
          <select class="ui-input" bind:value={diskPool}>
            {#each pools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
          </select>
        </label>
      </div>
    {:else if diskMode === 'existing'}
      <label class="ui-field">
        <span class="label">{t('Volume')}</span>
        <select class="ui-input" bind:value={existing} required>
          <option value="" disabled>{t('Choose an unused volume…')}</option>
          {#each freeDisks as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}, {formatBytes(v.vol.capacityBytes)}</option>{/each}
        </select>
        {#if freeDisks.length === 0}<span class="hint">{t('No unused disk volumes. Volumes attached to a machine are not listed.')}</span>{/if}
      </label>
    {:else}
      <label class="ui-field">
        <span class="label">{t('Insert')}</span>
        <select class="ui-input" bind:value={isoPath}>
          <option value="">{t('Nothing (empty drive)')}</option>
          {#each isos as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}</option>{/each}
        </select>
        {#if active}<span class="hint">{t('SATA CD/DVD drives usually cannot be hot-plugged; add it while the machine is off.')}</span>{/if}
      </label>
    {/if}
    {#if diskMode !== 'cdrom'}
      <label class="ui-field">
        <span class="label">{t('Bus')}</span>
        <select class="ui-input" bind:value={diskBus}>
          <option value="virtio">{t('VirtIO (fastest, needs drivers on Windows)')}</option>
          <option value="sata">{t('SATA (works everywhere)')}</option>
          <option value="scsi">{t('SCSI')}</option>
          <option value="usb">{t('USB')}</option>
        </select>
      </label>
    {/if}
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="disk" disabled={busy || (diskMode === 'existing' && !existing)}>{busy ? 'Adding…' : 'Add'}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'nic'} title={t('Add network adapter')} {onclose}>
  <form id="nic" class="form" onsubmit={(e) => (e.preventDefault(), addNIC())}>
    <label class="ui-field">
      <span class="label">{t('Connect to')}</span>
      <select class="ui-input" bind:value={nicChoice}>
        {#each networks as n (n.name)}<option value="network:{n.name}">{n.name} ({n.mode}{n.active ? '' : ', stopped'})</option>{/each}
        <option value="bridge">{t('A host bridge')}</option>
      </select>
    </label>
    {#if nicChoice === 'bridge'}
      <label class="ui-field"><span class="label">{t('Bridge')}</span><input class="ui-input mono" bind:value={bridge} /></label>
    {/if}
    <label class="ui-field">
      <span class="label">{t('Adapter model')}</span>
      <select class="ui-input" bind:value={nicModel}>
        <option value="virtio">{t('VirtIO (fastest)')}</option>
        <option value="e1000e">{t('Intel e1000e (works without drivers)')}</option>
        <option value="rtl8139">{t('Realtek RTL8139 (very old guests)')}</option>
      </select>
    </label>
    <label class="ui-field">
      <span class="label">{t('VLAN')} <span class="muted">{t('(optional)')}</span></span>
      <input class="ui-input mono" type="number" min="1" max="4094" bind:value={nicVlan} placeholder={t('as the network says')} />
      <span class="hint">{t('Works on Open vSwitch networks, macvtap passthrough and SR-IOV adapters.')}</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="nic" disabled={busy || !nicChoice}>{busy ? 'Adding…' : t('Add adapter')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'resize'} title={t('Grow {v}', { v: drive?.title ?? 'disk' })} subtitle={drive?.summary} {onclose}>
  <form id="grow" class="form" onsubmit={(e) => (e.preventDefault(), grow())}>
    <label class="ui-field">
      <span class="label">{t('New size (GiB)')}</span>
      <input class="ui-input" type="number" min={Math.floor(currentGiB) + 1} max="65536" bind:value={growGiB} />
      <span class="hint">{t('Currently {v}. Disks can only grow — shrinking would destroy data.{v2}', { v: currentGiB ? `${currentGiB.toFixed(1)} GiB` : 'unknown', v2: active ? ' The running machine sees the new size immediately.' : '' })}</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="grow" disabled={busy || growGiB <= currentGiB}>{busy ? 'Growing…' : t('Grow disk')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'bandwidth'} title={t('Traffic limits: {v}', { v: drive?.mac ?? '' })} subtitle={drive?.summary} {onclose}>
  <form id="bandwidth" class="form" onsubmit={(e) => (e.preventDefault(), saveBandwidth())}>
    <div class="row">
      <label class="ui-field">
        <span class="label">{t('Into the machine (Mbit/s)')}</span>
        <input class="ui-input" type="number" min="0" max="400000" bind:value={bwIn} placeholder="0" />
      </label>
      <label class="ui-field">
        <span class="label">{t('Out of the machine (Mbit/s)')}</span>
        <input class="ui-input" type="number" min="0" max="400000" bind:value={bwOut} placeholder="0" />
      </label>
    </div>
    <span class="hint">{t('Average rate the adapter may use; 0 = no limit.')} {active ? t('Applied at once and kept for the next start.') : ''}</span>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="bandwidth" disabled={busy}>{t('Save limits')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'diskmove'} title={t('Move {v}', { v: drive?.title ?? 'disk' })} subtitle={drive?.summary} {onclose}>
  <form id="diskmove" class="form" onsubmit={(e) => (e.preventDefault(), moveDisk())}>
    {#if tiersHere.length}
      <div class="row">
        <label class="ui-switch"><input type="radio" bind:group={moveBy} value="pool" /> {t('To a pool')}</label>
        <label class="ui-switch"><input type="radio" bind:group={moveBy} value="tier" /> {t('To a storage tier')}</label>
      </div>
    {/if}
    {#if moveBy === 'tier'}
      <label class="ui-field">
        <span class="label">{t('Storage tier')}</span>
        <select class="ui-input" bind:value={moveTier}>{#each tiersHere as x (x)}<option value={x}>{x.toUpperCase()}</option>{/each}</select>
        <span class="hint">{t('The pool of that tier with the most free space takes it.')}</span>
      </label>
    {/if}
    <label class="ui-field" hidden={moveBy === 'tier'}>
      <span class="label">{t('Storage pool')}</span>
      <select class="ui-input" bind:value={diskPool}>
        {#each pools as p (p.name)}<option value={p.name}>{p.name}{poolTiers[p.name] ? ' · ' + poolTiers[p.name].toUpperCase() : ''} ({formatBytes(p.availableBytes)} {t('free')})</option>{/each}
      </select>
      <span class="hint">{active ? t('The machine keeps running: the disk is copied while it is in use, then it switches over.') : t('The disk is copied into the pool as a qcow2 image.')} {t('The old image is deleted afterwards. Machines with snapshots must delete them first.')}</span>
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="diskmove" disabled={busy || !diskPool}>{t('Move disk')}</button>
  {/snippet}
</Dialog>

<Dialog open={which === 'qos'} title={t('Storage QoS & SLA: {v}', { v: drive?.title ?? 'Disk' })} subtitle={t('Limit IOPS and throughput to prevent noisy-neighbor starvation.')} {onclose}>
  <form id="qos" class="form" onsubmit={(e) => (e.preventDefault(), saveQoS())}>
    <div class="row">
      <label class="ui-field">
        <span class="label">{t('Total IOPS Limit')} <span class="muted">{t('(0 = unlimited)')}</span></span>
        <input class="ui-input" type="number" min="0" max="1000000" bind:value={qosTotalIops} placeholder="0" />
      </label>
      <label class="ui-field">
        <span class="label">{t('Total Bandwidth (MB/s)')} <span class="muted">{t('(0 = unlimited)')}</span></span>
        <input class="ui-input" type="number" min="0" max="100000" bind:value={qosTotalMbps} placeholder="0" />
      </label>
    </div>
    <div class="row">
      <label class="ui-field">
        <span class="label">{t('Read IOPS')}</span>
        <input class="ui-input" type="number" min="0" max="1000000" bind:value={qosReadIops} />
      </label>
      <label class="ui-field">
        <span class="label">{t('Write IOPS')}</span>
        <input class="ui-input" type="number" min="0" max="1000000" bind:value={qosWriteIops} />
      </label>
    </div>
    <div class="row">
      <label class="ui-field">
        <span class="label">{t('Read Bandwidth (MB/s)')}</span>
        <input class="ui-input" type="number" min="0" max="100000" bind:value={qosReadMbps} />
      </label>
      <label class="ui-field">
        <span class="label">{t('Write Bandwidth (MB/s)')}</span>
        <input class="ui-input" type="number" min="0" max="100000" bind:value={qosWriteMbps} />
      </label>
    </div>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="qos" disabled={busy}>{busy ? t('Applying…') : t('Apply QoS Limits')}</button>
  {/snippet}
</Dialog>


<Dialog open={which === 'media'} title={t('{v}: media', { v: drive?.title ?? 'Drive' })} subtitle={drive?.summary} {onclose}>
  <div class="form">
    <label class="ui-field">
      <span class="label">{t('Insert image')}</span>
      <select class="ui-input" bind:value={isoPath}>
        <option value="" disabled>{t('Choose an ISO…')}</option>
        {#each isos as v (v.vol.path)}<option value={v.vol.path}>{v.vol.name} — {v.pool}</option>{/each}
      </select>
      {#if isos.length === 0}<span class="hint">{t('No ISO images.')} <a href="/storage" onclick={router.link}>{t('Upload one')}</a> {t('in Storage.')}</span>{/if}
    </label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </div>
  {#snippet footer()}
    {#if drive?.summary && !drive.summary.endsWith('Empty')}
      <button class="ui-btn ghost eject" onclick={() => changeMedia('')} disabled={busy}>{t('Eject')}</button>
    {/if}
    <button class="ui-btn ghost" onclick={onclose}>{t('Cancel')}</button>
    <button class="ui-btn primary" onclick={() => changeMedia(isoPath)} disabled={busy || !isoPath}>{t('Insert')}</button>
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
