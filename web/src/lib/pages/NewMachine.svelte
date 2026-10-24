<script lang="ts">
  import { t } from '../i18n.svelte'
  import { session } from '../session.svelte'
  import ArrowLeft from '@lucide/svelte/icons/arrow-left'
  import Disc from '@lucide/svelte/icons/disc'
  import Cpu from '@lucide/svelte/icons/cpu'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Network from '@lucide/svelte/icons/network'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Rocket from '@lucide/svelte/icons/rocket'
  import LayoutTemplate from '@lucide/svelte/icons/layout-template'
  import BookOpen from '@lucide/svelte/icons/book-open'
  import { openDeploy } from '../deploy.svelte'
  import { api, errorText, type ContainerImage, type CreateSpec, type Pool, type VNetwork, type Volume } from '../api'
  import { policies, type StoragePolicy } from '../ceph'
  import { physnet, type IPPool } from '../physnet'
  import { sdn, type SecurityGroup, type Subnet, type VPC } from '../sdn'
  import { track } from '../jobs'
  import { live } from '../live.svelte'
  import { formatBytes, formatKiB } from '../format'
  import { machineHref, router } from '../router.svelte'
  import { toasts } from '../toast.svelte'
  import IaCPanel from '../components/IaCPanel.svelte'
  import { ent, type DRSPlacement, type GPUCandidate, type GPUPoolView, type PoolView } from '../enterprise'
  import { cluster } from '../cluster.svelte'
  import { projects } from '../projects.svelte'

  type OS = CreateSpec['os']
  const OSES: { key: OS; label: string; hint: string }[] = [
    { key: 'linux', label: t('Linux'), hint: t('Debian, Ubuntu, Fedora, Arch… Fast VirtIO devices.') },
    { key: 'windows', label: t('Windows'), hint: t('Windows 10/11 and Server. UEFI, Secure Boot and TPM 2.0.') },
    { key: 'other', label: t('Other'), hint: t('BSD, older systems. Widely compatible devices.') },
  ]
  const DEFAULTS: Record<OS, { vcpus: number; memGiB: number; diskGiB: number; secureBoot: boolean; tpm: boolean }> = {
    linux: { vcpus: 2, memGiB: 2, diskGiB: 20, secureBoot: false, tpm: false },
    windows: { vcpus: 4, memGiB: 8, diskGiB: 64, secureBoot: true, tpm: true },
    other: { vcpus: 2, memGiB: 2, diskGiB: 20, secureBoot: false, tpm: false },
  }

  let name = $state('')
  let nameTouched = $state(false)
  let kind = $state<'vm' | 'container'>('vm')
  let os = $state<OS>('linux')
  let vcpus = $state(2)
  let memGiB = $state(2)
  let withDisk = $state(true)
  let diskGiB = $state(20)
  let provisioning = $state<'thin' | 'thick'>('thin')
  let pool = $state('default')
  let iso = $state('')
  // Windows: a second CD with the VirtIO drivers and guest agent, picked when one is in a pool.
  let driverIso = $state('')
  const VIRTIO_WIN = /virtio-win/i
  const VIRTIO_WIN_URL = 'https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso'
  // Where the system comes from: an installer ISO, a ready cloud image, or an existing disk.
  let source = $state<'iso' | 'cloud' | 'disk'>('iso')
  let baseImage = $state('')
  let existingDisk = $state('')
  let ciUser = $state('admin')
  let ciPassword = $state('')
  let ciKeys = $state('')
  let netChoice = $state('network:default') // "network:<name>" | "sdn:<subnet>" | "bridge" | "none"
  let bridge = $state('br0')
  let firmware = $state<'uefi' | 'bios'>('uefi')
  let secureBoot = $state(false)
  let tpm = $state(false)
  let confidential = $state<'' | 'sev' | 'sev-snp' | 'tdx'>('')
  let start = $state(true)
  let advanced = $state(false)

  function pickOS(k: OS) {
    os = k
    const d = DEFAULTS[k]
    vcpus = Math.min(d.vcpus, maxCPUs)
    // Never suggest more than ¾ of the host's memory.
    memGiB = Math.max(1, Math.min(d.memGiB, Math.floor(maxMemGiB * 0.75)))
    diskGiB = d.diskGiB
    secureBoot = d.secureBoot
    tpm = d.tpm
    if (k === 'windows') firmware = 'uefi'
  }

  // ---- Host resources and choices -----------------------------------------------------
  let maxCPUs = $derived(Math.max(1, live.host?.cpus ?? 8))
  let maxMemGiB = $derived(Math.max(1, Math.floor((live.host?.memoryKiB ?? 8 << 20) / (1 << 20))))

  // Projects (ADR-0060): an optional home for the new machine. Only the ones the user may
  // change things in (operator or better, admins get them all) are offered, and its pools
  // and networks must be on the project's allowed lists.
  let project = $state('')
  $effect(() => {
    if (!projects.loaded) void projects.load().catch(() => {})
  })
  let myProjects = $derived(
    session.can('admin') ? projects.list : projects.operable(session.info?.user ?? ''),
  )
  let proj = $derived(projects.list.find((p) => p.id === project))

  let pools = $state<Pool[]>([])
  let isos = $state<{ pool: string; vol: Volume }[]>([])
  /** Unused disk images: candidates for cloud images and "use existing disk". */
  let images = $state<{ pool: string; vol: Volume }[]>([])
  let networks = $state<VNetwork[]>([])
  let loadErr = $state('')

  $effect(() => {
    void (async () => {
      try {
        const [p, n] = await Promise.all([api.pools(), api.networks()])
        pools = p.filter((x) => x.active)
        const candidatePools = pools.filter((p) => ['dir', 'netfs', 'fs', 'logical', 'zfs', 'rbd'].includes(p.type))
        if (!candidatePools.some((x) => x.name === pool) && candidatePools[0]) pool = candidatePools[0].name
        storagePolicies = (await policies.list().catch(() => [])).filter((x) => candidatePools.some((p) => p.name === x.pool))
        ipPools = await physnet.pools().catch(() => [])
        const def = storagePolicies.find((x) => x.default)
        if (def) policy = def.id
        if (!networks.some((x) => x.name === 'default')) netChoice = networks[0] ? `network:${networks[0].name}` : 'none'
        const lists = await Promise.all(pools.map(async (p) => ({ p: p.name, v: await api.volumes(p.name).catch(() => []) })))
        isos = lists.flatMap(({ p, v }) => v.filter((x) => x.format === 'iso').map((vol) => ({ pool: p, vol })))
        driverIso = isos.find((i) => VIRTIO_WIN.test(i.vol.name))?.vol.path ?? ''
        images = lists.flatMap(({ p, v }) =>
          v.filter((x) => x.format !== 'iso' && x.usedBy.length === 0 && !x.name.endsWith('-cidata.iso')).map((vol) => ({ pool: p, vol })),
        )
      } catch (e) {
        loadErr = errorText(e)
      }
    })()
  })

  // Suggest a name from the image ("debian-13.1.0-amd64-netinst.iso" → "debian-13").
  $effect(() => {
    const from = source === 'iso' ? iso : source === 'cloud' ? baseImage : existingDisk
    if (nameTouched || !from) return
    const base = from.split('/').pop() ?? ''
    const m = /^([a-z]+)[-_]?(\d+)?/i.exec(base)
    if (m) name = uniqueName(m[2] ? `${m[1]}-${m[2]}`.toLowerCase() : m[1].toLowerCase())
  })
  function uniqueName(base: string) {
    const taken = new Set((live.snapshot?.domains ?? []).map((d) => d.name))
    if (!taken.has(base)) return base
    for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
  }

  let templates = $derived((live.snapshot?.domains ?? []).filter((d) => d.template).sort((a, b) => a.name.localeCompare(b.name)))
  // File pools for cloud images and seeds:
  let filePools = $derived(pools.filter((p) => ['dir', 'netfs', 'fs'].includes(p.type) && (!proj || proj.storagePools.includes(p.name))))
  // Disk pools for empty root disks: includes native LVM, ZFS and Ceph RBD block pools
  let diskPools = $derived(pools.filter((p) => ['dir', 'netfs', 'fs', 'logical', 'zfs', 'rbd'].includes(p.type) && (!proj || proj.storagePools.includes(p.name))))
  // IP pools (ADR-0075): a cloud-image machine can get a fixed address and a DNS name.
  let ipPools = $state<IPPool[]>([])
  let ipPool = $state('')
  let fixedIP = $state('')
  let netName = $derived(netChoice.startsWith('network:') ? netChoice.slice(8) : '')
  let poolsHere = $derived(ipPools.filter((p) => !p.network || p.network === netName))
  $effect(() => {
    if (ipPool && !poolsHere.some((p) => p.id === ipPool)) ipPool = ''
  })
  // Storage policies (ADR-0073) name a pool by intent; picking one picks its pool for a new
  // empty disk (cloud images keep their file pool picker).
  let storagePolicies = $state<StoragePolicy[]>([])
  let policy = $state('')
  $effect(() => {
    const p = storagePolicies.find((x) => x.id === policy)
    if (p && source === 'iso') pool = p.pool
  })
  // Networks the project allows ("" while no project is chosen: every network).
  let projNets = $derived(proj ? networks.filter((n) => proj.networks.includes(n.name)) : networks)

  // Keep the choices inside the project's allowed lists.
  $effect(() => {
    void proj
    if (!proj) return
    if (!proj.storagePools.includes(pool)) {
      const ok = diskPools[0]?.name ?? filePools[0]?.name
      if (ok) pool = ok
    }
    const net = netChoice.startsWith('network:') ? netChoice.slice(8) : ''
    if (net && !proj.networks.includes(net)) netChoice = projNets[0] ? `network:${projNets[0].name}` : 'none'
  })
  // SDN subnets (ADR-0072) of the project's VPCs (all of them for a global machine).
  let sdnVPCs = $state<VPC[]>([])
  let sdnSubnets = $state<Subnet[]>([])
  let sdnGroups = $state<SecurityGroup[]>([])
  $effect(() => {
    void Promise.all([sdn.vpcs(), sdn.subnets(), sdn.securityGroups()])
      .then(([v, s, g]) => ([sdnVPCs, sdnSubnets, sdnGroups] = [v, s, g]))
      .catch(() => {})
  })
  let projSubnets = $derived(sdnSubnets.filter((s) => !proj || sdnVPCs.find((v) => v.id === s.vpcId)?.projectId === proj.id))
  let ovnSubnet = $derived(netChoice.startsWith('sdn:') ? sdnSubnets.find((s) => s.id === netChoice.slice(4)) : undefined)
  let ovnGroups = $state<string[]>([])
  let ovnIP = $state('')
  $effect(() => {
    if (netChoice.startsWith('sdn:') && (kind === 'container' || !projSubnets.some((s) => s.id === netChoice.slice(4)))) netChoice = 'none'
  })
  const vpcLabel = (id: string) => sdnVPCs.find((v) => v.id === id)?.name ?? id
  let selectedPool = $derived(pools.find((p) => p.name === pool))
  let isBlockPool = $derived(selectedPool ? ['logical', 'zfs', 'rbd'].includes(selectedPool.type) : false)
  let diskTooBig = $derived(!!selectedPool && withDisk && diskGiB * 2 ** 30 > selectedPool.availableBytes)
  let nameValid = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(name))
  let nameTaken = $derived((live.snapshot?.domains ?? []).some((d) => d.name === name))
  let baseCapGiB = $derived(Math.ceil((images.find((i) => i.vol.path === baseImage)?.vol.capacityBytes ?? 0) / 2 ** 30))
  let keys = $derived(ciKeys.split('\n').map((k) => k.trim()).filter(Boolean))
  let ciProblem = $derived(
    source !== 'cloud'
      ? ''
      : !/^[a-z_][a-z0-9_-]{0,31}$/.test(ciUser)
        ? 'User name: lowercase letters, digits, "_" or "-".'
        : !ciPassword && keys.length === 0
          ? 'Set a password or an SSH key to be able to sign in.'
          : '',
  )
  let bootable = $derived(source === 'iso' ? withDisk || !!iso : source === 'cloud' ? !!baseImage : !!existingDisk)
  // DRS (partially or fully automated) suggests the member the machine fits best.
  let placement = $state<DRSPlacement | null>(null)
  $effect(() => {
    const mib = Math.round(memGiB * 1024)
    if (!cluster.clustered) {
      placement = null
      return
    }
    const timer = setTimeout(() => {
      ent.drsPlacement(mib).then((p) => (placement = p ?? null)).catch(() => (placement = null))
    }, 400)
    return () => clearTimeout(timer)
  })
  let suggested = $derived(placement && cluster.current && placement.hostId !== cluster.current.id ? placement : null)

  // GPUs from a GPU pool (ADR-0071): the machine is created where they are free.
  let gpuPools = $state<GPUPoolView[]>([])
  let withGPU = $state(false)
  let gpuPool = $state('')
  let gpuCount = $state(1)
  let gpuWhere = $state<{ best: GPUCandidate } | null | undefined>(undefined)
  $effect(() => {
    ent.gpuPools()
      .then((p) => {
        gpuPools = p.filter((x) => x.kind !== 'mig')
        if (!gpuPool && gpuPools.length) gpuPool = gpuPools[0].id
      })
      .catch(() => (gpuPools = []))
  })
  $effect(() => {
    if (!withGPU || !gpuPool || kind !== 'vm') {
      gpuWhere = undefined
      return
    }
    const count = Math.max(1, gpuCount || 1)
    const timer = setTimeout(() => {
      ent.gpuPlacement(gpuPool, count).then((r) => (gpuWhere = r)).catch(() => (gpuWhere = null))
    }, 300)
    return () => clearTimeout(timer)
  })
  let gpuElsewhere = $derived(gpuWhere && cluster.current && gpuWhere.best.hostId !== cluster.current.id ? gpuWhere.best : null)

  // Memory encryption the host offers (libvirt's domain capabilities); usually none.
  let ccKinds = $state<string[]>([])
  $effect(() => {
    void api.confidentialKinds().then((k) => (ccKinds = k)).catch(() => (ccKinds = []))
  })

  // System containers (ADR-0055): an image from the Linux Containers project and root's password.
  let ctImages = $state<ContainerImage[]>([])
  let ctAvailable = $state(true)
  let ctReason = $state('')
  let ctImage = $state('debian-13')
  let rootPassword = $state('')
  let rootPasswordOK = $derived(rootPassword.length >= 8 && !/[:\u0000-\u001f]/.test(rootPassword))
  $effect(() => {
    if (kind !== 'container' || ctImages.length) return
    api
      .containerImages()
      .then((r) => {
        ctImages = r.images
        ctAvailable = r.available
        ctReason = r.reason ?? ''
      })
      .catch((e) => {
        ctAvailable = false
        ctReason = errorText(e)
      })
  })

  let canCreate = $derived(
    kind === 'container'
      ? nameValid && !nameTaken && ctAvailable && rootPasswordOK && vcpus >= 1 && memGiB > 0
      : nameValid && !nameTaken && bootable && !ciProblem && vcpus >= 1 && memGiB > 0,
  )

  let busy = $state(false)
  let error = $state('')

  // Resource pools of this host (ADR-0032): a new machine may go into one, within its limits.
  let respools = $state<PoolView[]>([])
  let respool = $state('')
  $effect(() => {
    void ent.respools().then((p) => (respools = p)).catch(() => {})
  })

  let network = $derived<CreateSpec['network']>(
    netChoice.startsWith('network:') ? { network: netChoice.slice(8) }
      : ovnSubnet ? { ovnSubnet: ovnSubnet.id, securityGroups: ovnGroups.length ? ovnGroups : undefined, ipAddress: ovnIP.trim() || undefined }
      : netChoice === 'bridge' ? { bridge } : undefined,
  )
  let netLabel = $derived(netChoice.startsWith('network:') ? netChoice.slice(8) : ovnSubnet ? `${vpcLabel(ovnSubnet.vpcId)} / ${ovnSubnet.name}` : netChoice === 'bridge' ? bridge : 'None')
  // The request exactly as sent (also shown as code next to the form).
  let spec = $derived<CreateSpec>(kind === 'container' ? {
    name,
    kind,
    os: 'linux',
    vcpus,
    memoryMiB: Math.round(memGiB * 1024),
    firmware: 'uefi',
    secureBoot: false,
    tpm: false,
    pool: '',
    diskGiB: 0,
    image: ctImage,
    rootPassword,
    network,
    start,
    project: project || undefined,
  } : {
    name,
    kind,
    os,
    vcpus,
    memoryMiB: Math.round(memGiB * 1024),
    firmware,
    secureBoot: kind === 'vm' && firmware === 'uefi' && secureBoot,
    tpm: kind === 'vm' && tpm,
    confidential: kind === 'vm' ? confidential || undefined : undefined,
    pool,
    diskGiB: source === 'cloud' ? Math.max(diskGiB, baseCapGiB) : source === 'iso' && withDisk ? diskGiB : 0,
    provisioning: (source === 'iso' && withDisk) || source === 'cloud' ? provisioning : undefined,
    iso: source === 'iso' ? iso || undefined : undefined,
    driverIso: source === 'iso' && os === 'windows' ? driverIso || undefined : undefined,
    baseImage: source === 'cloud' ? baseImage : undefined,
    diskPath: source === 'disk' ? existingDisk : undefined,
    cloudInit: source === 'cloud' ? { user: ciUser, password: ciPassword || undefined, sshKeys: keys } : undefined,
    network,
    start,
    project: project || undefined,
    gpu: kind === 'vm' && withGPU && gpuPool ? { pool: gpuPool, count: Math.max(1, gpuCount || 1) } : undefined,
    ip: source === 'cloud' && netName && ipPool ? { pool: ipPool, ip: fixedIP.trim() || undefined } : undefined,
  })

  async function create(e: SubmitEvent) {
    e.preventDefault()
    if (!canCreate) return
    busy = true
    error = ''
    try {
      if (kind === 'container') {
        const job = await api.createContainer(spec, respool || undefined)
        track(job, {
          success: t('{name} is ready', { name }),
          onSuccess: (j) => j.result && router.navigate(machineHref(j.result)),
        })
        router.navigate('/')
        return
      }
      const res = await api.create(spec, respool || undefined)
      if (res.startError) toasts.push({ kind: 'warn', title: t('{name} was created but did not start', { name: name }), detail: res.startError })
      else toasts.push({ kind: 'info', title: start ? `${name} is starting` : `${name} created` })
      router.navigate(machineHref(res.uuid))
    } catch (err) {
      error = errorText(err)
    } finally {
      busy = false
    }
  }
</script>

<svelte:head><title>{t('New machine · VirtoMir')}</title></svelte:head>

<a class="back" href="/" onclick={router.link}><ArrowLeft size={15} /> {t('Overview')}</a>
<header class="page-head">
  <div>
    <h1>{t('New machine')}</h1>
    <p class="lead">{t('Pick an operating system and installation media; everything else has a sensible default.')}</p>
  </div>
  <div class="head-right">
    <a class="ui-btn" href={router.scoped('/library')} onclick={router.link}><BookOpen size={15} /> {t('From Content Library')}</a>
    <a class="ui-btn" href={router.scoped('/machines/import')} onclick={router.link}>{t('Import OVA or XML…')}</a>
  </div>
</header>

{#if loadErr}<div class="ui-error-box">{loadErr}</div>{/if}

{#if suggested}
  <section class="ui-panel drs-hint">
    <span>{t('DRS suggests {host}: the least loaded host with room for this machine ({cpu} % CPU, {mem} % memory in use).', { host: suggested.hostName, cpu: Math.round(suggested.cpuPct), mem: Math.round(suggested.memPct) })}</span>
    <button type="button" class="ui-btn" onclick={() => suggested && cluster.open(suggested.hostId)}>{t('Create it on {host}', { host: suggested.hostName })}</button>
  </section>
{/if}

{#if templates.length}
  <section class="ui-panel templates">
    <span class="t-label"><LayoutTemplate size={15} /> {t('Start from a template — ready in seconds')}</span>
    <div class="t-list">
      {#each templates as t (t.uuid)}
        <button type="button" class="t-card" disabled={!session.canCreate} onclick={() => openDeploy(t)}>
          <strong>{t.name}</strong>
          <span>{t.vcpus} vCPU · {formatKiB(t.maxMemoryKiB)}</span>
        </button>
      {/each}
    </div>
  </section>
{/if}

<form class="layout" onsubmit={create}>
  <div class="steps">
    <section class="ui-panel step">
      <h2><span class="n">1</span> {t('Name and system')}</h2>
      <label class="ui-field">
        <span class="label">{t('Name')}</span>
        <input
          class="ui-input"
          bind:value={name}
          oninput={() => (nameTouched = true)}
          placeholder={t('web-server')}
          aria-invalid={name !== '' && (!nameValid || nameTaken)}
          spellcheck="false"
        />
        {#if name && !nameValid}<span class="error">{t('Letters, digits, \'.\', \'_\', \'+\' and \'-\', starting with a letter or digit.')}</span>
        {:else if nameTaken}<span class="error">{t('A machine with this name already exists.')}</span>{/if}
      </label>
      <div class="seg" role="radiogroup" aria-label={t('Instance type')}>
        <label class:on={kind === 'vm'}><input type="radio" bind:group={kind} value="vm" /> {t('Virtual Machine (KVM)')}</label>
        <label class:on={kind === 'container'}><input type="radio" bind:group={kind} value="container" /> {t('System Container (LXC)')}</label>
      </div>
      {#if kind === 'container'}
        <p class="hint-note">
          {t('A Linux system that shares the host’s kernel (libvirt LXC): it starts in a second and uses only the memory it needs. Its root user is an unprivileged user on the host.')}
        </p>
      {:else}
        <div class="choices" role="radiogroup" aria-label={t('Operating system')}>
          {#each OSES as o (o.key)}
            <label class="choice" class:on={os === o.key}>
              <input type="radio" name="os" checked={os === o.key} onchange={() => pickOS(o.key)} />
              <strong>{o.label}</strong>
              <span>{o.hint}</span>
            </label>
          {/each}
        </div>
      {/if}
    </section>

    {#if kind === 'container'}
    <section class="ui-panel step">
      <h2><span class="n">2</span> <Disc size={16} /> {t('Image')}</h2>
      {#if !ctAvailable}
        <div class="ui-error-box">{ctReason}</div>
      {:else}
        <div class="two">
          <label class="ui-field">
            <span class="label">{t('System')}</span>
            <select class="ui-input" bind:value={ctImage}>
              {#each ctImages as i (i.key)}<option value={i.key}>{i.label}</option>{/each}
            </select>
            <span class="hint">{t('From images.linuxcontainers.org, checked against its SHA-256 and kept for the next container.')}</span>
          </label>
          <label class="ui-field">
            <span class="label">{t('Root password')}</span>
            <input class="ui-input" type="password" bind:value={rootPassword} autocomplete="new-password" minlength="8" />
            <span class="hint" class:warn={rootPassword !== '' && !rootPasswordOK}>{t('At least 8 characters, no colon. Sign in as root on the console.')}</span>
          </label>
        </div>
      {/if}
    </section>
    {:else}
    <section class="ui-panel step">
      <h2><span class="n">2</span> <Disc size={16} /> {t('Source')}</h2>
      <div class="seg" role="radiogroup" aria-label={t('Where the system comes from')}>
        <label class:on={source === 'iso'}><input type="radio" bind:group={source} value="iso" /> {t('Install from ISO')}</label>
        <label class:on={source === 'cloud'}><input type="radio" bind:group={source} value="cloud" /> {t('Cloud image')}</label>
        <label class:on={source === 'disk'}><input type="radio" bind:group={source} value="disk" /> {t('Existing disk')}</label>
      </div>
      {#if source === 'iso'}
        <label class="ui-field">
          <span class="label">{t('Boot from')}</span>
          <select class="ui-input" bind:value={iso}>
            <option value="">{t('No installation media')}</option>
            {#each isos as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">
            {#if isos.length === 0}{t('No ISO images found.')}{/if}
            <a href="/storage" onclick={router.link}>{t('Upload an ISO')}</a> {t('in Storage.')}
          </span>
        </label>
        {#if os === 'windows'}
          <label class="ui-field">
            <span class="label">{t('Drivers CD (virtio-win)')}</span>
            <select class="ui-input" bind:value={driverIso}>
              <option value="">{t('None')}</option>
              {#each isos as i (i.vol.path)}
                <option value={i.vol.path}>{i.vol.name} — {i.pool}</option>
              {/each}
            </select>
            <span class="hint">
              {t('Windows installs without extra drivers; with virtio-win attached you can switch disks and adapters to the faster VirtIO later and install the guest agent.')}
              {#if !isos.some((i) => VIRTIO_WIN.test(i.vol.name))}{t('Get it into a pool with Storage → Download from URL:')} <span class="mono">{VIRTIO_WIN_URL}</span>{/if}
            </span>
          </label>
        {/if}
      {:else if source === 'cloud'}
        <label class="ui-field">
          <span class="label">{t('Cloud image')}</span>
          <select class="ui-input" bind:value={baseImage}>
            <option value="" disabled>{t('Choose an image…')}</option>
            {#each images as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">
            {t('Copied, so the original stays clean.')}
            <a href="/storage" onclick={router.link}>{t('Upload one')}</a> {t('in Storage.')}
          </span>
        </label>
        <div class="ci">
          <div class="two">
            <label class="ui-field">
              <span class="label">{t('User')}</span>
              <input class="ui-input mono" bind:value={ciUser} autocapitalize="off" spellcheck="false" />
            </label>
            <label class="ui-field">
              <span class="label">{t('Password')}</span>
              <input class="ui-input" type="password" bind:value={ciPassword} autocomplete="new-password" placeholder={t('optional with an SSH key')} />
            </label>
          </div>
          <label class="ui-field">
            <span class="label">{t('SSH public keys (one per line)')}</span>
            <textarea class="ui-input mono" rows="2" bind:value={ciKeys} placeholder={t('ssh-ed25519 AAAA… you@laptop')} spellcheck="false"></textarea>
            <span class="hint">{t('cloud-init creates the user with sudo, sets the hostname to the machine name and installs the QEMU guest agent.')}</span>
          </label>
          {#if ciProblem}<p class="error">{ciProblem}</p>{/if}
        </div>
      {:else}
        <label class="ui-field">
          <span class="label">{t('Disk image')}</span>
          <select class="ui-input" bind:value={existingDisk}>
            <option value="" disabled>{t('Choose an unused volume…')}</option>
            {#each images as i (i.vol.path)}
              <option value={i.vol.path}>{i.vol.name} — {i.pool}, {formatBytes(i.vol.capacityBytes)}</option>
            {/each}
          </select>
          <span class="hint">{t('The machine boots this image directly (not a copy) — e.g. a disk imported from another host.')}</span>
        </label>
      {/if}
    </section>
    {/if}

    <section class="ui-panel step">
      <h2><span class="n">3</span> <Cpu size={16} /> {t('Processor and memory')}</h2>
      <div class="slider">
        <label for="vcpus">{t('Virtual CPUs')}</label>
        <input id="vcpus" type="range" min="1" max={maxCPUs} bind:value={vcpus} />
        <input class="ui-input num-in" type="number" min="1" max="512" bind:value={vcpus} aria-label={t('Virtual CPUs')} />
        <span class="hint">{t('The host has {maxCPUs} threads; machines may share them.', { maxCPUs: maxCPUs })}</span>
      </div>
      <div class="slider">
        <label for="mem">{t('Memory (GiB)')}</label>
        <input id="mem" type="range" min="1" max={maxMemGiB} step="1" bind:value={memGiB} />
        <input class="ui-input num-in" type="number" min="0.25" step="0.25" bind:value={memGiB} aria-label={t('Memory in GiB')} />
        <span class="hint" class:warn={memGiB > maxMemGiB * 0.8}>
          {t('{v}The host has {memoryKiB}.', { v: memGiB > maxMemGiB * 0.8 ? 'Close to all host memory — the host may start swapping. ' : '', memoryKiB: formatKiB(live.host?.memoryKiB ?? 0) })}
        </span>
      </div>
      {#if kind === 'vm' && gpuPools.length}
        <div class="gpu">
          <label class="ui-switch"><input type="checkbox" bind:checked={withGPU} /> {t('Give it GPUs')}</label>
          {#if withGPU}
            <select class="ui-input" bind:value={gpuPool} aria-label={t('GPU pool')}>
              {#each gpuPools as p (p.id)}<option value={p.id}>{p.name} ({t('{free} free', { free: p.free })})</option>{/each}
            </select>
            <input class="ui-input num-in" type="number" min="1" max="16" bind:value={gpuCount} aria-label={t('Number of GPUs')} />
            <span class="hint" class:warn={gpuWhere === null || !!gpuElsewhere}>
              {#if gpuWhere === null}
                {t('No member has that many free GPUs of this pool.')}
              {:else if gpuElsewhere}
                {t('This host has not enough free; {host} has {n}.', { host: gpuElsewhere.hostName, n: gpuElsewhere.available })}
                <button type="button" class="ui-btn sm" onclick={() => gpuElsewhere && cluster.open(gpuElsewhere.hostId)}>{t('Create it on {host}', { host: gpuElsewhere.hostName })}</button>
              {:else if gpuWhere}
                {t('This host has {n} free.', { n: gpuWhere.best.available })}
              {/if}
            </span>
          {/if}
        </div>
      {/if}
    </section>

    {#if kind === 'vm'}
    <section class="ui-panel step">
      <h2><span class="n">4</span> <HardDrive size={16} /> {t('Disk')}</h2>
      {#if source === 'disk'}
        <p class="hint">{t('Uses the selected disk image as it is.')}</p>
      {:else if source === 'cloud'}
        <div class="two">
          <label class="ui-field">
            <span class="label">{t('Grow to (GiB)')}</span>
            <input class="ui-input" type="number" min={baseCapGiB || 1} max="65536" bind:value={diskGiB} />
            <span class="hint">{t('The image is {v} GiB; cloud-init grows the file system on first boot.', { v: baseCapGiB || '?' })}</span>
          </label>
          <label class="ui-field">
            <span class="label">{t('Storage pool')}</span>
            <select class="ui-input" bind:value={pool}>
              {#each filePools as p (p.name)}<option value={p.name}>{p.name} — {formatBytes(p.availableBytes)} free</option>{/each}
            </select>
          </label>
        </div>
      {:else}
      <label class="ui-switch"><input type="checkbox" bind:checked={withDisk} /> {t('Create a new disk')}</label>
      {/if}
      {#if source === 'iso' && withDisk}
        <div class="two">
          <label class="ui-field">
            <span class="label">{t('Size (GiB)')}</span>
            <input class="ui-input" type="number" min="1" max="65536" bind:value={diskGiB} aria-invalid={diskTooBig} />
            <span class="hint" class:warn={diskTooBig}>
              {provisioning === 'thin' ? t('Thin provisioned: it only uses what the guest writes.{v}', { v: diskTooBig ? ' Larger than the free space in the pool.' : '' }) : t('Thick provisioned: preallocated blocks on storage.')}
            </span>
          </label>
          {#if storagePolicies.length}
            <label class="ui-field">
              <span class="label">{t('Storage policy')}</span>
              <select class="ui-input" bind:value={policy}>
                <option value="">{t('— choose the pool myself —')}</option>
                {#each storagePolicies as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
              </select>
            </label>
          {/if}
          <label class="ui-field">
            <span class="label">{t('Storage pool')}</span>
            <select class="ui-input" bind:value={pool} disabled={!!policy}>
              {#each diskPools as p (p.name)}
                <option value={p.name}>{p.name}{p.type === 'logical' ? ' (LVM)' : p.type === 'zfs' ? ' (ZFS)' : p.type === 'rbd' ? ' (Ceph)' : ''} — {formatBytes(p.availableBytes)} free</option>
              {/each}
            </select>
            {#if isBlockPool}
              <span class="hint">{t('Block volume: raw disk device created directly in pool.')}</span>
            {/if}
          </label>
        </div>
        <label class="ui-field">
          <span class="label">{t('Provisioning Policy')}</span>
          <select class="ui-input" bind:value={provisioning}>
            <option value="thin">{t('Thin (sparse, allocate on demand)')}</option>
            <option value="thick">{t('Thick (fully preallocated, highest sustained performance)')}</option>
          </select>
        </label>
      {:else if source === 'iso' && !iso}
        <p class="error">{t('Without a disk the machine needs installation media to boot from.')}</p>
      {/if}
    </section>
    {/if}

    <section class="ui-panel step">
      <h2><span class="n">{kind === 'vm' ? 5 : 4}</span> <Network size={16} /> {t('Network')}</h2>
      <label class="ui-field">
        <span class="label">{t('Connect to')}</span>
        <select class="ui-input" bind:value={netChoice}>
          {#each projNets as n (n.name)}
            <option value="network:{n.name}">{n.name} ({n.mode === 'nat' ? 'NAT, internet through the host' : n.mode})</option>
          {/each}
          {#if kind === 'vm'}
            {#each projSubnets as sn (sn.id)}
              <option value="sdn:{sn.id}">{t('SDN: {vpc} / {subnet} ({cidr})', { vpc: vpcLabel(sn.vpcId), subnet: sn.name, cidr: sn.cidr })}</option>
            {/each}
          {/if}
          <option value="bridge">{t('A host bridge (machine appears on your LAN)')}</option>
          <option value="none">{t('No network')}</option>
        </select>
      </label>
      {#if ovnSubnet}
        <label class="ui-field">
          <span class="label">{t('Address')}</span>
          <input class="ui-input mono" bind:value={ovnIP} placeholder={t('from DHCP')} />
        </label>
        {#if sdnGroups.some((g) => g.vpcId === ovnSubnet.vpcId)}
          <fieldset class="sgs">
            <legend class="label">{t('Security groups')}</legend>
            {#each sdnGroups.filter((g) => g.vpcId === ovnSubnet.vpcId) as g (g.id)}
              <label class="ui-switch"><input type="checkbox" bind:group={ovnGroups} value={g.id} /> {g.name}</label>
            {/each}
          </fieldset>
        {/if}
      {/if}
      {#if netChoice === 'bridge'}
        <label class="ui-field">
          <span class="label">{t('Bridge')}</span>
          <input class="ui-input mono" bind:value={bridge} placeholder="br0" />
        </label>
      {/if}
      {#if source === 'cloud' && netName && poolsHere.length}
        <div class="two">
          <label class="ui-field">
            <span class="label">{t('Address')}</span>
            <select class="ui-input" bind:value={ipPool}>
              <option value="">{t('from DHCP')}</option>
              {#each poolsHere as p (p.id)}<option value={p.id}>{t('fixed, from {pool} ({cidr})', { pool: p.name, cidr: p.cidr })}</option>{/each}
            </select>
          </label>
          {#if ipPool}
            <label class="ui-field">
              <span class="label">{t('Which one')}</span>
              <input class="ui-input mono" bind:value={fixedIP} placeholder={t('the first free one')} />
            </label>
          {/if}
        </div>
        {#if ipPool}<span class="hint">{t('cloud-init sets the address; the machine’s name goes into DNS when dynamic DNS is on.')}</span>{/if}
      {/if}
    </section>

    {#if kind === 'vm'}
      <section class="ui-panel step">
        <button type="button" class="adv" aria-expanded={advanced} onclick={() => (advanced = !advanced)}>
          <ShieldCheck size={16} /> {t('Firmware and security')}
          <span class="adv-sum">{firmware === 'uefi' ? 'UEFI' : 'BIOS'}{secureBoot && firmware === 'uefi' ? ' · Secure Boot' : ''}{tpm ? ' · TPM 2.0' : ''}{confidential ? ` · ${confidential.toUpperCase()}` : ''}</span>
          <span class="chev" class:up={advanced}><ChevronDown size={16} /></span>
        </button>
        {#if advanced}
          <div class="adv-body">
            <div class="two">
              <label class="ui-field">
                <span class="label">{t('Firmware')}</span>
                <select class="ui-input" bind:value={firmware}>
                  <option value="uefi">{t('UEFI (modern, recommended)')}</option>
                  <option value="bios">{t('BIOS (legacy)')}</option>
                </select>
              </label>
              <label class="ui-field">
                <span class="label">{t('Confidential Computing')}</span>
                <select class="ui-input" bind:value={confidential} disabled={!ccKinds.length}>
                  <option value="">{t('None (standard VM)')}</option>
                  {#if ccKinds.includes('sev')}<option value="sev">{t('AMD SEV (Secure Encrypted Virtualization)')}</option>{/if}
                  {#if ccKinds.includes('sev-snp')}<option value="sev-snp">{t('AMD SEV-SNP (Secure Nested Paging)')}</option>{/if}
                  {#if ccKinds.includes('tdx')}<option value="tdx">{t('Intel TDX (Trust Domain Extensions)')}</option>{/if}
                </select>
                {#if !ccKinds.length}<span class="hint">{t('This host offers no memory encryption (AMD SEV or Intel TDX).')}</span>{/if}
              </label>
            </div>
            <label class="ui-switch"><input type="checkbox" bind:checked={secureBoot} disabled={firmware === 'bios'} /> {t('Secure Boot')}</label>
            <label class="ui-switch"><input type="checkbox" bind:checked={tpm} /> {t('TPM 2.0 (required by Windows 11)')}</label>
          </div>
        {/if}
      </section>
    {/if}
  </div>

  <aside class="summary ui-panel">
    <h2>{t('Summary')}</h2>
    <dl>
      <dt>{t('Type')}</dt><dd>{kind === 'container' ? t('System Container (LXC)') : t('Virtual Machine (KVM)')}</dd>
      <dt>{t('Name')}</dt><dd>{name || '—'}</dd>
      {#if kind === 'container'}
      <dt>{t('System')}</dt><dd>{ctImages.find((i) => i.key === ctImage)?.label ?? '—'}</dd>
      <dt>{t('CPU / RAM')}</dt><dd>{t('{vcpus} vCPU · {memGiB} GiB', { vcpus: vcpus, memGiB: memGiB })}</dd>
      <dt>{t('Network')}</dt><dd>{netLabel}</dd>
      {:else}
      <dt>{t('System')}</dt><dd>{OSES.find((o) => o.key === os)?.label}</dd>
      <dt>{t('Source')}</dt><dd class="mono small">{(source === 'iso' ? iso : source === 'cloud' ? baseImage : existingDisk).split('/').pop() || (source === 'iso' && withDisk ? 'Empty disk' : '—')}</dd>
      <dt>{t('CPU / RAM')}</dt><dd>{t('{vcpus} vCPU · {memGiB} GiB', { vcpus: vcpus, memGiB: memGiB })}</dd>
      <dt>{t('Disk')}</dt><dd>{source === 'disk' ? 'Existing image' : source === 'cloud' ? t('{v} GiB copy in {pool}', { v: Math.max(diskGiB, baseCapGiB), pool: pool }) : withDisk ? `${diskGiB} GiB in ${pool}` : 'None'}</dd>
      {#if source === 'cloud'}<dt>{t('Sign-in')}</dt><dd class="mono small">{ciUser}{ciPassword ? ' · password' : ''}{keys.length ? ` · ${keys.length} key${keys.length > 1 ? 's' : ''}` : ''}</dd>{/if}
      <dt>{t('Network')}</dt><dd>{netLabel}</dd>
      <dt>{t('Firmware')}</dt><dd>{firmware === 'uefi' ? 'UEFI' : 'BIOS'}{secureBoot && firmware === 'uefi' ? ', Secure Boot' : ''}{tpm ? ', TPM' : ''}</dd>
      {/if}
    </dl>
    {#if myProjects.length}
      <label class="ui-field">
        <span class="label">{t('Project')}</span>
        <select class="ui-input" bind:value={project}>
          <option value="">{t('No project (global)')}</option>
          {#each myProjects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
        <span class="hint">{t('Only the projects you may change things in; their pools and networks are the choices above.')}</span>
      </label>
    {/if}
    {#if respools.length}
      <label class="ui-field">
        <span class="label">{t('Resource pool')}</span>
        <select class="ui-input" bind:value={respool}>
          <option value="">{t('None')}</option>
          {#each respools as rp (rp.id)}<option value={rp.id}>{rp.name}</option>{/each}
        </select>
      </label>
    {/if}
    <label class="ui-switch"><input type="checkbox" bind:checked={start} /> {t('Start it right away')}</label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    {#if !session.canCreate}<p class="error">{t('{v} Operators can create machines when an admin gives them a quota.', { v: session.why('admin') })}</p>{/if}
    {#if session.quota?.rule && session.quota.used}
      <p class="hint">{t('Your quota: {machines} of {machines2} machines, {vcpus} of {vcpus2} vCPUs, {memoryMiB} of {memoryMiB2} MiB{v} in use.', { machines: session.quota.used.machines, machines2: session.quota.rule.machines, vcpus: session.quota.used.vcpus, vcpus2: session.quota.rule.vcpus, memoryMiB: session.quota.used.memoryMiB, memoryMiB2: session.quota.rule.memoryMiB, v: session.quota.rule.diskGiB != null ? `, ${session.quota.used.diskGiB ?? 0} of ${session.quota.rule.diskGiB} GiB disk` : '' })}</p>
    {/if}
    <button class="ui-btn primary create" disabled={!canCreate || busy || !session.canCreate}>
      <Rocket size={15} />
      {busy ? 'Creating…' : start ? 'Create and start' : 'Create'}
    </button>
    {#if kind === 'vm' && !bootable}<span class="error">{t('Add a disk or installation media.')}</span>{/if}

    <IaCPanel {spec} />
  </aside>
</form>

<style>
  .sgs {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
  }
  .drs-hint {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 14px;
    padding: 12px 14px;
    border-left: 3px solid var(--accent);
    font-size: 13px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 14px;
    color: var(--text-3);
    font-size: 13px;
    text-decoration: none;
  }
  .back:hover {
    color: var(--text);
  }
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: 16px;
    align-items: start;
  }
  .steps {
    display: grid;
    gap: 14px;
  }
  .templates {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px 18px;
    margin-bottom: 16px;
  }
  .t-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 13.5px;
  }
  .t-list {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .t-card {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    padding: 10px 14px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font: inherit;
    cursor: pointer;
  }
  .t-card:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .t-card span {
    color: var(--text-3);
    font-size: 12px;
  }
  .step {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px 18px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 14.5px;
    font-weight: 600;
  }
  .n {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 12px;
    font-weight: 700;
  }
  .choices {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }
  .choice {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition:
      border-color var(--dur-fast),
      background var(--dur-fast);
  }
  .choice input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .choice.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .choice:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .choice strong {
    font-size: 14px;
  }
  .choice span {
    color: var(--text-3);
    font-size: 12px;
  }
  .gpu {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
  }
  .gpu select {
    max-width: 260px;
  }
  .slider {
    display: grid;
    grid-template-columns: 130px minmax(0, 1fr) 90px;
    align-items: center;
    gap: 6px 14px;
  }
  .slider label {
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
  }
  .slider input[type='range'] {
    width: 100%;
    accent-color: var(--accent);
  }
  .slider .hint {
    grid-column: 2 / -1;
  }
  .num-in {
    height: 32px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .hint a {
    color: var(--accent);
    text-decoration: none;
  }
  .warn {
    color: var(--warn);
  }
  .error {
    margin: 0;
    color: var(--danger);
    font-size: 12.5px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
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
    height: 32px;
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
  .ci {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius-sm);
  }
  .adv {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 0;
    border: 0;
    background: none;
    font-size: 14.5px;
    font-weight: 600;
    text-align: left;
    cursor: pointer;
  }
  .adv-sum {
    margin-left: auto;
    color: var(--text-3);
    font-size: 12.5px;
    font-weight: 400;
  }
  .chev {
    display: grid;
    color: var(--text-3);
    transition: transform var(--dur) var(--ease-out);
  }
  .chev.up {
    transform: rotate(180deg);
  }
  .adv-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .summary {
    position: sticky;
    top: 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px 18px;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 6px 12px;
    margin: 0;
    font-size: 13px;
  }
  dt {
    color: var(--text-3);
  }
  dd {
    margin: 0;
    text-align: right;
    overflow-wrap: anywhere;
  }
  .small {
    font-size: 11.5px;
  }
  .create {
    height: 40px;
  }
  .head-right {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  @media (max-width: 1000px) {
    .layout {
      grid-template-columns: 1fr;
    }
    .summary {
      position: static;
    }
  }
  @media (max-width: 640px) {
    .choices,
    .two {
      grid-template-columns: 1fr;
    }
    .slider {
      grid-template-columns: 1fr 80px;
    }
    .slider label {
      grid-column: 1 / -1;
    }
    .slider .hint {
      grid-column: 1 / -1;
    }
  }
</style>
