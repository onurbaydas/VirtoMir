<script lang="ts">
  import { t } from '../i18n.svelte'
  import { session } from '../session.svelte'
  import ArrowLeft from '@lucide/svelte/icons/arrow-left'
  import Monitor from '@lucide/svelte/icons/monitor'
  import Cpu from '@lucide/svelte/icons/cpu'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import EthernetPort from '@lucide/svelte/icons/ethernet-port'
  import SearchX from '@lucide/svelte/icons/search-x'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import { api, ApiError, type DetachKind, type Device, type DeviceGroup, type DomainDetail, type ExtraDeviceKind, type Prop } from '../api'
  import { ACTIONS, isActive, live } from '../live.svelte'
  import { formatBytes, formatKiB, formatPercent, plural } from '../format'
  import { copy } from '../machine'
  import { router } from '../router.svelte'
  import StatePill from '../components/StatePill.svelte'
  import StatCard from '../components/StatCard.svelte'
  import Sparkline from '../components/Sparkline.svelte'
  import MachineActions from '../components/MachineActions.svelte'
  import PropList from '../components/PropList.svelte'
  import DeviceCard from '../components/DeviceCard.svelte'
  import XmlView from '../components/XmlView.svelte'
  import PerformancePanel from '../components/PerformancePanel.svelte'
  import SnapshotsPanel from '../components/SnapshotsPanel.svelte'
  import SettingsPanel from '../components/SettingsPanel.svelte'
  import ConsolePanel from '../components/ConsolePanel.svelte'
  import BackupsPanel from '../components/BackupsPanel.svelte'
  import Maximize2 from '@lucide/svelte/icons/maximize-2'
  import GuestPanel from '../components/GuestPanel.svelte'
  import Gauge from '@lucide/svelte/icons/gauge'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ManageDialogs, { type ManageDialog } from '../components/ManageDialogs.svelte'
  import ChangesPanel from '../components/ChangesPanel.svelte'
  import QemuArgsCard from '../components/QemuArgsCard.svelte'
  import FirewallPanel from '../components/FirewallPanel.svelte'
  import type { MenuItem } from '../components/ActionMenu.svelte'
  import Pencil from '@lucide/svelte/icons/pencil'
  import CopyPlus from '@lucide/svelte/icons/copy-plus'
  import Power from '@lucide/svelte/icons/power'
  import Trash from '@lucide/svelte/icons/trash'
  import Plus from '@lucide/svelte/icons/plus'
  import Unplug from '@lucide/svelte/icons/unplug'
  import Disc from '@lucide/svelte/icons/disc'
  import LayoutTemplate from '@lucide/svelte/icons/layout-template'
  import ArrowRightLeft from '@lucide/svelte/icons/arrow-right-left'
  import Shrink from '@lucide/svelte/icons/shrink'
  import { track } from '../jobs'
  import { eff } from '../storageeff'
  import FolderKanban from '@lucide/svelte/icons/folder-kanban'
  import { projects } from '../projects.svelte'
  import SDNPortsPanel from '../components/SDNPortsPanel.svelte'
  import { cluster } from '../cluster.svelte'
  import { machineHref } from '../router.svelte'
  import { errorText } from '../api'
  import { toasts } from '../toast.svelte'

  let { uuid }: { uuid: string } = $props()

  let detail = $state.raw<DomainDetail | null>(null)
  let error = $state<{ status: number; message: string } | null>(null)

  /** The live list entry: state, CPU % and balloon size update every second. */
  let liveDom = $derived(live.snapshot?.domains.find((d) => d.uuid === uuid))
  let dom = $derived(liveDom ?? (detail ? { ...detail, cpuPercent: 0 } : null))
  let active = $derived(dom ? isActive(dom.state) : false)
  let pending = $derived(live.pending[uuid])
  let gone = $derived(!!detail && !!live.snapshot?.connected && !liveDom)

  /** A failed refresh after the first load: the page keeps the last data and says so. */
  let reloadErr = $state('')
  // Only the newest request may update the page: a slow older answer (or one for the
  // machine shown before) must not overwrite it.
  let seq = 0
  async function load() {
    const my = ++seq
    try {
      const d = await api.domain(uuid)
      if (my !== seq) return
      detail = d
      error = null
      reloadErr = ''
    } catch (e) {
      if (my !== seq) return
      if (detail) reloadErr = errorText(e)
      else error = e instanceof ApiError ? { status: e.status, message: e.message } : { status: 0, message: String(e) }
    }
  }

  // Another machine opened in the same page: forget the previous one at once.
  let shown = ''
  $effect.pre(() => {
    if (uuid === shown) return
    shown = uuid
    detail = null
    error = null
    reloadErr = ''
  })

  // Reload the definition whenever the machine changes state (devices, ports and addresses
  // change on start/stop). Running machines get a slow refresh for DHCP leases.
  let stateKey = $derived(liveDom?.state ?? 'unknown')
  $effect(() => {
    void stateKey
    void load()
    if (!active) return
    const t = setInterval(load, 15_000)
    return () => clearInterval(t)
  })

  // Six everyday tabs; firewall, change history and raw XML live one level deeper under
  // "Advanced". Their old links (#firewall, #changes, #xml) still open them there.
  type Tab = 'overview' | 'console' | 'settings' | 'hardware' | 'snapshots' | 'backups' | 'advanced'
  type Sub = 'firewall' | 'changes' | 'xml'
  const TABS: Tab[] = ['overview', 'console', 'settings', 'hardware', 'snapshots', 'backups', 'advanced']
  const SUBS: Sub[] = ['changes', 'firewall', 'xml']
  // A system container (ADR-0055) has no VM settings, firewall, snapshots or backups.
  const VM_ONLY: Tab[] = ['settings', 'snapshots', 'backups']
  let container = $derived(detail?.kind === 'container')
  let hash = $derived(router.route.name === 'machine' ? router.route.tab : '')
  let tab = $derived.by<Tab>(() => {
    const want: Tab = SUBS.includes(hash as Sub) ? 'advanced' : TABS.includes(hash as Tab) ? (hash as Tab) : 'overview'
    return container && VM_ONLY.includes(want) ? 'overview' : want
  })
  let sub = $derived.by<Sub>(() => {
    const want: Sub = SUBS.includes(hash as Sub) ? (hash as Sub) : 'changes'
    return container && want === 'firewall' ? 'changes' : want
  })

  // ---- Management ---------------------------------------------------------------------
  let dlg = $state<ManageDialog>(null)
  let mediaDrive = $state<Device | undefined>()
  let addOpen = $state(false)
  let addKind = $state<ExtraDeviceKind | null>(null)
  let projectDlg = $state(false)
  let targetProject = $state('')

  $effect(() => {
    if (!projects.loaded) void projects.load().catch(() => {})
  })

  let manageItems = $derived.by((): MenuItem[] => {
    if (!detail) return []
    const admin = session.why('admin') || undefined
    const items: MenuItem[] = [
      { label: t('Rename…'), icon: Pencil, run: () => (dlg = 'rename'), disabled: session.whyManage(dom) || undefined },
      { label: t('Clone…'), icon: CopyPlus, run: () => (dlg = 'clone'), disabled: admin },
    ]
    if (cluster.clustered && detail.persistent)
      items.push({ label: t('Move to another host…'), icon: ArrowRightLeft, run: () => (dlg = 'move'), disabled: admin })
    if (detail.persistent)
      items.push(
        dom?.template
          ? { label: t('Turn into a normal machine'), icon: LayoutTemplate, run: () => void setTemplate(false), disabled: admin }
          : { label: t('Make template…'), icon: LayoutTemplate, run: () => (dlg = 'template'), disabled: admin },
      )
    if (detail.persistent && (projects.list.length > 0 || !!dom?.project))
      items.push({
        label: t('Move to project…'),
        icon: FolderKanban,
        run: () => ((targetProject = dom?.project ?? ''), (projectDlg = true)),
        disabled: session.why('operator') || undefined,
      })
    if (detail.persistent)
      items.push({
        label: liveDom?.autostart ?? detail.autostart ? 'Don’t start with the host' : t('Start with the host'),
        icon: Power,
        run: () => void setAutostart(!(liveDom?.autostart ?? detail!.autostart)),
        disabled: admin,
      })
    items.push({ label: t('Delete…'), icon: Trash, run: () => (dlg = 'delete'), disabled: session.whyManage(dom) || undefined })
    return items
  })

  async function setTemplate(on: boolean) {
    try {
      await api.setTemplate(uuid, on)
      toasts.push({ kind: 'info', title: on ? 'Now a template' : t('{name} is a normal machine again', { name: detail?.name }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not changed'), detail: errorText(e) })
    }
  }

  async function setAutostart(on: boolean) {
    try {
      await api.setAutostart(uuid, on)
      toasts.push({ kind: 'info', title: on ? 'Starts with the host' : t('No longer starts with the host') })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Autostart not changed'), detail: errorText(e) })
    }
  }

  async function saveProject() {
    try {
      await api.setMachineProject(uuid, targetProject)
      projectDlg = false
      toasts.push({
        kind: 'info',
        title: targetProject ? t('{name} moved to {project}', { name: detail?.name, project: projects.name(targetProject) }) : t('{name} is global again', { name: detail?.name }),
      })
      await load()
      await projects.load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Project not changed'), detail: errorText(e) })
    }
  }

  /** Devices the Remove button handles, with its question. The rest: the XML tab. */
  const REMOVE: Partial<Record<string, string>> = {
    disk: t('Detach from machine?'),
    cdrom: t('Detach from machine?'),
    'hostdev-pci': t('Give it back to the host?'),
    'hostdev-usb': t('Give it back to the host?'),
    'hostdev-mdev': t('Give the slice back?'),
    interface: t('Unplug this adapter?'),
    filesystem: t('Stop sharing this folder?'),
    watchdog: t('Remove the watchdog?'),
    tpm: t('Remove the TPM and its keys?'),
    rng: 'Remove it?',
    clipboard: t('Turn clipboard sharing off?'),
  }
  const NO_TARGET = new Set(['tpm', 'rng', 'clipboard'])
  const removable = (d: Device) => !!REMOVE[d.kind] && (d.kind === 'interface' ? !!d.mac : NO_TARGET.has(d.kind) || !!d.target)

  async function detachDevice(d: Device) {
    const r =
      d.kind === 'interface'
        ? await api.detach(uuid, { kind: 'interface', mac: d.mac })
        : await api.detach(uuid, { kind: (d.kind === 'cdrom' ? 'disk' : d.kind) as DetachKind, target: d.target })
    toasts.push({
      kind: r.liveError ? 'warn' : 'info',
      title: `${d.title} removed`,
      detail: r.liveError
        ? t('Gone from the saved definition; the running machine keeps it until it stops: {liveError}', { liveError: r.liveError })
        : r.appliesOnNextBoot
          ? 'Gone from the next start; the running machine keeps it until then.'
          : d.kind === 'disk'
            ? 'The disk image stays in its pool.'
            : undefined,
    })
    await load()
  }
  // Unsaved settings: ask before leaving the tab (the panel also guards reloads).
  let settingsDirty = $state(false)
  function go(to: string) {
    if (settingsDirty && tab === 'settings' && to !== 'settings' && !confirm(t('Leave without saving your changes to the settings?'))) return
    router.navigate(`/machines/${uuid}${to && to !== 'overview' ? '#' + to : ''}`, { replace: true })
  }
  const setTab = (to: Tab) => go(to)
  const setSub = (to: Sub) => go(to)

  // ---- Overview cards -------------------------------------------------------------

  const CPU_MODE: Record<string, string> = {
    'host-passthrough': t('Same as host (passthrough)'),
    'host-model': t('Closest to the host CPU'),
    maximum: t('Everything the hypervisor supports'),
    custom: t('Named CPU model'),
  }
  const POWER_EVENT: Record<string, string> = {
    destroy: t('Power off'),
    restart: 'Restart',
    preserve: t('Keep for debugging'),
    'rename-restart': t('Rename and restart'),
    'coredump-destroy': t('Dump memory, then power off'),
    'coredump-restart': t('Dump memory, then restart'),
  }
  const FEATURE: Record<string, string> = {
    acpi: 'ACPI',
    apic: 'APIC',
    pae: 'PAE',
    hyperv: t('Hyper-V enlightenments'),
    kvm: t('KVM tweaks'),
    smm: 'SMM',
    vmport: t('VMware port'),
    ioapic: 'IOAPIC',
    vmcoreinfo: 'vmcoreinfo',
    pvspinlock: 'PV spinlocks',
    pmu: 'PMU',
  }

  const p = (label: string, value: string | number | undefined | null, tech?: string): Prop | null =>
    value === undefined || value === null || value === '' ? null : { label, value: String(value), tech }
  const clean = (xs: (Prop | null)[]) => xs.filter((x): x is Prop => x !== null)

  // Everyday facts first; the plumbing (firmware files, emulator, power events) is folded
  // away under "Technical details".
  let systemProps = $derived.by(() => {
    if (!detail) return []
    const s = detail.system
    const fw = s.firmware === 'uefi' ? (s.secureBoot ? t('UEFI with Secure Boot') : t('UEFI')) : t('BIOS (legacy)')
    return clean([
      p(t('Firmware'), fw, s.firmware === 'uefi' ? 'os@firmware / os/loader' : 'os/loader'),
      p(t('Boot menu'), s.bootMenu ? t('shown') : null, 'os/bootmenu@enable'),
      s.hypervisor === 'qemu' ? p(t('Hypervisor'), t('QEMU emulation (no acceleration, slow)'), 'domain@type') : null,
    ])
  })
  let techProps = $derived.by(() => {
    if (!detail) return []
    const s = detail.system
    const hv = s.hypervisor === 'kvm' ? t('KVM (hardware accelerated)') : s.hypervisor === 'qemu' ? t('QEMU emulation (no acceleration, slow)') : s.hypervisor
    return clean([
      p(t('Hypervisor'), hv, 'domain@type'),
      p(t('Architecture'), s.arch, 'os/type@arch'),
      p(t('Chipset / machine type'), s.machine, 'os/type@machine'),
      p(t('Firmware image'), s.loader, 'os/loader'),
      p(t('UEFI variables'), s.nvram, 'os/nvram'),
      p(t('Kernel (direct boot)'), s.kernel, 'os/kernel'),
      p(t('Kernel command line'), s.cmdline, 'os/cmdline'),
      p(t('Clock'), s.clock, 'clock@offset'),
      p(t('When the guest powers off'), POWER_EVENT[s.onPoweroff ?? ''] ?? s.onPoweroff, 'on_poweroff'),
      p(t('When the guest reboots'), POWER_EVENT[s.onReboot ?? ''] ?? s.onReboot, 'on_reboot'),
      p(t('When the guest crashes'), POWER_EVENT[s.onCrash ?? ''] ?? s.onCrash, 'on_crash'),
      p(t('Emulator'), s.emulator, 'devices/emulator'),
    ])
  })
  let techOpen = $state(false)

  let cpuProps = $derived.by(() => {
    if (!detail) return []
    const c = detail.cpu
    const topo = `${plural(c.sockets, 'socket')} × ${plural(c.cores, 'core')} × ${plural(c.threads, 'thread')}`
    return clean([
      p( t('Virtual CPUs'), c.vcpus === c.maxVcpus ? c.vcpus : t('{vcpus} active of {maxVcpus} (hot-pluggable)', { vcpus: c.vcpus, maxVcpus: c.maxVcpus }), 'vcpu'),
      p('Topology', topo, 'cpu/topology'),
      p( t('CPU model'), CPU_MODE[c.mode] ? CPU_MODE[c.mode] + (c.model ? ` · ${c.model}` : '') : c.mode, 'cpu@mode / cpu/model'),
      p( t('Feature overrides'), c.features ? plural(c.features, 'feature') : null, 'cpu/feature'),
      p( t('Pinned to host CPUs'), c.pinned ? 'yes' : null, 'cputune/vcpupin'),
      p( t('Allowed host CPUs'), c.cpuset, 'vcpu@cpuset'),
      p( t('Guest NUMA nodes'), c.numaNodes || null, 'cpu/numa/cell'),
    ])
  })

  let memoryProps = $derived.by(() => {
    if (!detail) return []
    const m = detail.memory
    return clean([
      p('Assigned', formatKiB(m.maxKiB), 'memory'),
      p( t('Current (balloon target)'), m.currentKiB !== m.maxKiB ? formatKiB(m.currentKiB) : null, 'currentMemory'),
      p( t('Hot-plug maximum'), m.hotplugKiB ? formatKiB(m.hotplugKiB) : null, 'maxMemory'),
      p( t('Huge pages'), m.hugepages ? 'yes' : null, 'memoryBacking/hugepages'),
      p( t('Balloon device'), m.balloon ? (m.balloon === 'virtio' ? 'VirtIO' : m.balloon) : 'none', 'devices/memballoon@model'),
    ])
  })

  // ---- Hardware --------------------------------------------------------------------

  const GROUPS: { key: DeviceGroup; label: string; advanced?: boolean }[] = [
    { key: 'storage', label: t('Storage') },
    { key: 'network', label: t('Network') },
    { key: 'display', label: t('Display') },
    { key: 'console', label: t('Consoles & channels') },
    { key: 'passthrough', label: t('Passthrough') },
    { key: 'input', label: t('Input & sound') },
    { key: 'system', label: t('System devices'), advanced: true },
    { key: 'controller', label: t('Controllers'), advanced: true },
  ]
  let showAdvanced = $state(false)
  let grouped = $derived(
    GROUPS.map((g) => ({ ...g, devices: (detail?.devices ?? []).filter((d) => d.group === g.key) })).filter(
      (g) => g.devices.length > 0,
    ),
  )
  let advancedCount = $derived(grouped.filter((g) => g.advanced).reduce((n, g) => n + g.devices.length, 0))

  // ---- Stat cards ------------------------------------------------------------------

  let disks = $derived((detail?.devices ?? []).filter((d) => d.kind === 'disk'))
  let diskTotal = $derived(disks.reduce((n, d) => n + (d.capacityBytes ?? 0), 0))
  let nics = $derived((detail?.devices ?? []).filter((d: Device) => d.kind === 'interface'))
  let addresses = $derived(nics.flatMap((n) => n.addresses ?? []))
  let ipv4 = $derived(addresses.filter((a) => !a.includes(':')))

  let memCurrent = $derived(liveDom?.memoryKiB ?? detail?.memory.currentKiB ?? 0)
  let memMax = $derived(liveDom?.maxMemoryKiB ?? detail?.memory.maxKiB ?? 0)

  let title = $derived(dom ? `${dom.name} · VirtoMir` : t('Machine · VirtoMir'))
</script>

<svelte:head><title>{title}</title></svelte:head>

<a class="back" href={router.scoped('/machines')} onclick={router.link}><ArrowLeft size={15} /> {t('Machines')}</a>

{#if error && !detail}
  <div class="state-msg">
    {#if error.status === 404}
      <SearchX size={30} strokeWidth={1.5} />
      <strong>{t('This machine does not exist')}</strong>
      <span>{t('It may have been deleted, or the link is wrong.')}</span>
    {:else}
      <TriangleAlert size={30} strokeWidth={1.5} />
      <strong>{t('Could not load this machine')}</strong>
      <span class="mono">{error.message}</span>
      <button class="link" onclick={load}>{t('Try again')}</button>
    {/if}
  </div>
{:else if !dom || !detail}
  <div class="skeleton">
    <span class="sk" style:width="220px" style:height="28px"></span>
    <span class="sk" style:width="340px"></span>
    <div class="sk-cards">{#each [0, 1, 2, 3] as i (i)}<span class="sk card"></span>{/each}</div>
  </div>
{:else}
  {#if gone}
    <div class="gone" role="status">
      <TriangleAlert size={16} />
      {t('This machine is no longer on the host. What you see is its last known configuration.')}
    </div>
  {:else if reloadErr}
    <div class="gone" role="status">
      <TriangleAlert size={16} />
      <span>{t('Could not refresh this page; showing the last known state.')} <span class="mono">{reloadErr}</span></span>
      <button class="link" onclick={load}>{t('Try again')}</button>
    </div>
  {/if}

  <header class="page-head">
    <div class="ident">
      <span class="avatar" class:on={active}><Monitor size={20} /></span>
      <div>
        <h1>
          {dom.name}
          <StatePill state={dom.state} progress={pending ? ACTIONS[pending.action].progress : undefined} />
        </h1>
        <div class="chips">
          {#if detail.title}<span class="chip">{detail.title}</span>{/if}
          <button class="chip mono copyable" title={t('Copy UUID')} onclick={() => copy(uuid, 'UUID')}>{uuid}</button>
          <span class="chip" title={dom.persistent ? 'Defined: survives a host reboot' : t('Exists only while running')}>
            {dom.persistent ? 'persistent' : 'transient'}
          </span>
          {#if dom.project}<span class="chip" title={t('The project this machine belongs to')}>{projects.name(dom.project) || dom.project}</span>{/if}
          {#if dom.autostart}<span class="chip" title={t('Starts when the host boots')}>{t('autostart')}</span>{/if}
          {#if dom.template}<span class="chip accent" title={t('Never starts; new machines are made from it')}><LayoutTemplate size={12} /> {t('template')}</span>{/if}
          {#if dom.linkedFrom}
            {@const t = live.snapshot?.domains.find((d) => d.uuid === dom.linkedFrom)}
            <a class="chip link" href={machineHref(dom.linkedFrom)} onclick={router.link} title="Its disks only store changes on top of this template's disks">linked to {t?.name ?? 'template'}</a>
          {/if}
          <span class="chip mono" title={t('Firmware')}>{detail.system.firmware === 'uefi' ? 'UEFI' : 'BIOS'}{detail.system.secureBoot ? ' · Secure Boot' : ''}</span>
          {#if detail.system.machine}<span class="chip mono" title={t('Machine type')}>{detail.system.machine}</span>{/if}
        </div>
      </div>
    </div>
    <div class="head-right">
      {#if live.streamOpen && live.snapshot?.connected && active}
        <span class="live" title={t('Updated every second')}><span class="live-dot"></span>{t('Live')}</span>
      {/if}
      {#if !gone && liveDom}<MachineActions d={liveDom} size="lg" extra={manageItems} />{/if}
    </div>
  </header>

  {#if detail.description}<p class="desc">{detail.description}</p>{/if}

  <section class="stats">
    <StatCard
      label={t('CPU')}
      icon={Cpu}
      value={active ? formatPercent(liveDom?.cpuPercent ?? 0) : '—'}
      unit={active ? '%' : ''}
      sub="{plural(detail.cpu.vcpus, 'vCPU')} · {plural(detail.cpu.sockets, 'socket')} × {plural(detail.cpu.cores, 'core')} × {plural(detail.cpu.threads, 'thread')}"
    >
      {#snippet chart()}<Sparkline data={live.domainCpu(uuid)} tick={live.tick} height={56} />{/snippet}
    </StatCard>
    <StatCard
      label={t('Memory')}
      icon={MemoryStick}
      value={formatKiB(memCurrent).split(' ')[0]}
      unit={formatKiB(memCurrent).split(' ')[1] + (memCurrent !== memMax ? ` of ${formatKiB(memMax)}` : '')}
      sub={liveDom?.guestMemTotalKiB
        ? t('Guest uses {guestMemUsedKiB} ({guestMemTotalKiB} %)', { guestMemUsedKiB: formatKiB(liveDom.guestMemUsedKiB ?? 0), guestMemTotalKiB: formatPercent(((liveDom.guestMemUsedKiB ?? 0) / liveDom.guestMemTotalKiB) * 100) })
        : memCurrent !== memMax
          ? 'Balloon is holding memory back'
          : detail.memory.balloon
            ? 'Balloon can return memory to the host'
            : t('Fixed size')}
    />
    <StatCard
      label={t('Storage')}
      icon={HardDrive}
      value={diskTotal ? formatBytes(diskTotal).split(' ')[0] : String(disks.length)}
      unit={diskTotal ? formatBytes(diskTotal).split(' ')[1] : disks.length === 1 ? 'disk' : 'disks'}
      sub={diskTotal ? t('{disk} · virtual size', { disk: plural(disks.length, 'disk') }) : t('Size unknown')}
    />
    <StatCard
      label={t('Network')}
      icon={EthernetPort}
      value={ipv4[0]?.split('/')[0] ?? (nics.length ? String(nics.length) : 'None')}
      unit={ipv4[0] ? '' : nics.length ? (nics.length === 1 ? 'adapter' : 'adapters') : ''}
      sub={ipv4.length > 1
        ? `+${ipv4.length - 1} more address${ipv4.length > 2 ? 'es' : ''}`
        : ipv4.length
          ? nics[0]?.summary
          : active
            ? 'No address reported yet'
            : t('Addresses appear while running')}
    />
  </section>

  <div class="tabs" role="tablist" aria-label={t('Machine sections')}>
    <button role="tab" aria-selected={tab === 'overview'} class:on={tab === 'overview'} onclick={() => setTab('overview')}>
      {t('Overview')}
    </button>
    {#if session.can('operator')}
      <button role="tab" aria-selected={tab === 'console'} class:on={tab === 'console'} onclick={() => setTab('console')}>
        {t('Console')}
      </button>
    {/if}
    {#if !container}
    <button role="tab" aria-selected={tab === 'settings'} class:on={tab === 'settings'} onclick={() => setTab('settings')}>
      {t('Settings')}
    </button>
    {/if}
    <button role="tab" aria-selected={tab === 'hardware'} class:on={tab === 'hardware'} onclick={() => setTab('hardware')}>
      {t('Hardware')} <span class="count num">{detail.devices.length}</span>
    </button>
    {#if !container}
    <button role="tab" aria-selected={tab === 'snapshots'} class:on={tab === 'snapshots'} onclick={() => setTab('snapshots')}>
      {t('Snapshots')}
    </button>
    <button role="tab" aria-selected={tab === 'backups'} class:on={tab === 'backups'} onclick={() => setTab('backups')}>
      {t('Backups')}
    </button>
    {/if}
    <button role="tab" aria-selected={tab === 'advanced'} class:on={tab === 'advanced'} onclick={() => setTab('advanced')}>{t('Advanced')}</button>
  </div>

  {#if tab === 'overview'}
    {#if liveDom}<PerformancePanel d={liveDom} devices={detail.devices} />{/if}
    <div class="grid" role="tabpanel">
      <section class="panel">
        <h2>{t('System')}</h2>
        <PropList props={systemProps} />
        {#if detail.system.bootOrder.length}
          <div class="sub-block">
            <h3>{t('Boot order')}</h3>
            <ol class="boot">
              {#each detail.system.bootOrder as b, i (b + i)}<li>{b}</li>{/each}
            </ol>
          </div>
        {/if}
        <button class="more" aria-expanded={techOpen} onclick={() => (techOpen = !techOpen)}>
          <span class="chev" class:up={techOpen}><ChevronDown size={15} /></span>
          {t('Technical details')}
        </button>
        {#if techOpen}
          <PropList props={techProps} />
          {#if detail.system.features.length}
            <div class="sub-block">
              <h3>{t('Platform features')}</h3>
              <div class="chips">
                {#each detail.system.features as f (f)}<span class="chip" title="features/{f}">{FEATURE[f] ?? f}</span>{/each}
              </div>
            </div>
          {/if}
        {/if}
      </section>
      <div class="col">
        <section class="panel">
          <h2>{t('Processor')}</h2>
          <PropList props={cpuProps} />
        </section>
        <section class="panel">
          <h2>{t('Memory')}</h2>
          <PropList props={memoryProps} />
        </section>
        <section class="panel">
          <GuestPanel uuid={detail.uuid} running={active} />
        </section>
      </div>
    </div>
  {:else if tab === 'hardware'}
    <div class="hw" role="tabpanel">
      <div class="hw-tools">
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'disk')}><Plus size={14} /> {t('Add storage')}</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'nic')}><Plus size={14} /> {t('Add network adapter')}</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'hostdev')}><Plus size={14} /> {t('Add host device')}</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((addKind = null), (addOpen = true))}><Plus size={14} /> {t('Add other device…')}</button>
        <span class="hw-hint">{active ? 'Hot-plugged where the hardware allows; the rest from the next start.' : t('Anything else: the XML tab.')}</span>
      </div>
      <SDNPortsPanel uuid={detail.uuid} project={detail.project ?? ''} />
      {#each grouped as g (g.key)}
        {#if !g.advanced || showAdvanced}
          <section class="group">
            <h2>{g.label} <span class="count num">{g.devices.length}</span></h2>
            <div class="devices">
              {#each g.devices as d, i (d.title + i)}
                <DeviceCard device={d}>
                  {#snippet actions()}
                    {#if d.kind === 'cdrom' || d.kind === 'floppy'}
                      <button class="ui-btn sm" disabled={!session.can('operator')} title={session.why('operator')} onclick={() => ((mediaDrive = d), (dlg = 'media'))}>
                        <Disc size={14} /> {d.summary?.endsWith('Empty') ? 'Insert media…' : t('Change media…')}
                      </button>
                    {/if}
                    {#if d.kind === 'disk' && d.target}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((mediaDrive = d), (dlg = 'resize'))}><Maximize2 size={14} /> {t('Grow…')}</button>
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((mediaDrive = d), (dlg = 'diskmove'))}><ArrowRightLeft size={14} /> {t('Move…')}</button>
                      {#if d.capacityBytes}
                        <ConfirmButton label={t('Compact')} confirm={t('Trim its free space now')} icon={Shrink}
                          disabled={!session.can('admin') || detail?.state !== 'shutoff'} title={detail?.state !== 'shutoff' ? t('Shut the machine down to compact its disk') : t('Give the host back the space of deleted files (virt-sparsify)')}
                          run={async () => track(await eff.sparsify(uuid, d.target!), { success: t('{disk} compacted', { disk: d.target! }), onSuccess: () => void load() })} />
                      {/if}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((mediaDrive = d), (dlg = 'qos'))}><Gauge size={14} /> {t('QoS Limits…')}</button>
                    {/if}
                    {#if d.kind === 'interface' && d.mac}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((mediaDrive = d), (dlg = 'bandwidth'))}><Gauge size={14} /> {t('Limits…')}</button>
                    {/if}

                    {#if d.kind === 'video'}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((addKind = 'video'), (addOpen = true))}><Pencil size={14} /> {t('Change…')}</button>
                    {/if}
                    {#if removable(d)}
                      <ConfirmButton label={t('Remove')} confirm={REMOVE[d.kind] ?? "Remove it?"} icon={Unplug} failure={t('Could not remove {title}', { title: d.title })} disabled={!session.can('admin')} title={session.why('admin')} run={() => detachDevice(d)} />
                    {/if}
                  {/snippet}
                </DeviceCard>
              {/each}
            </div>
          </section>
        {/if}
      {/each}
      {#if advancedCount}
        <button class="more" aria-expanded={showAdvanced} onclick={() => (showAdvanced = !showAdvanced)}>
          <span class="chev" class:up={showAdvanced}><ChevronDown size={15} /></span>
          {showAdvanced ? t('Hide {device} and expert settings', { device: plural(advancedCount, 'more device') }) : t('Show {device} (controllers, balloon, RNG, TPM…) and expert settings', { device: plural(advancedCount, 'more device') })}
        </button>
      {/if}
      {#if detail.devices.length === 0}
        <div class="state-msg"><strong>{t('No devices defined')}</strong></div>
      {/if}
      {#if detail.persistent && (showAdvanced || advancedCount === 0)}<QemuArgsCard {uuid} onsaved={load} />{/if}
    </div>
  {:else if tab === 'console'}
    <div role="tabpanel">
      {#if liveDom}<ConsolePanel d={liveDom} />{/if}
    </div>
  {:else if tab === 'settings'}
    <div role="tabpanel">
      <SettingsPanel {uuid} {active} onsaved={load} ondirty={(d) => (settingsDirty = d)} />
    </div>
  {:else if tab === 'backups'}
    <div role="tabpanel">
      <BackupsPanel {uuid} name={dom.name} {active} />
    </div>
  {:else if tab === 'snapshots'}
    <div role="tabpanel">
      <SnapshotsPanel {uuid} {active} onchange={load} />
    </div>
  {:else}
    <div role="tabpanel">
      <div class="subtabs" role="tablist" aria-label={t('Advanced sections')}>
        <button role="tab" aria-selected={sub === 'changes'} class:on={sub === 'changes'} onclick={() => setSub('changes')}>{t('Change history')}</button>
        {#if !container}
          <button role="tab" aria-selected={sub === 'firewall'} class:on={sub === 'firewall'} onclick={() => setSub('firewall')}>{t('Firewall')}</button>
        {/if}
        <button role="tab" aria-selected={sub === 'xml'} class:on={sub === 'xml'} onclick={() => setSub('xml')}>{t('XML')}</button>
      </div>
      {#if sub === 'firewall'}
        <FirewallPanel {uuid} name={dom.name} {active} />
      {:else if sub === 'xml'}
        <XmlView {uuid} name={dom.name} {active} persistent={dom.persistent} version={stateKey} onsaved={load} />
      {:else}
        <ChangesPanel {uuid} {active} version={stateKey} onchange={load} />
      {/if}
    </div>
  {/if}

  <ManageDialogs which={dlg} {detail} drive={mediaDrive} {active} onclose={() => (dlg = null)} onchange={load} />

  <Dialog open={projectDlg} title={t('Move to project')} width={440} onclose={() => (projectDlg = false)}>
    <p class="dlg-lead">
      {t('{name} moves with its disks; the target project\'s limits and allowed pools and networks apply.', { name: dom?.name })}
    </p>
    <label class="ui-field">
      <span class="label">{t('Project')}</span>
      <select class="ui-input" bind:value={targetProject}>
        <option value="">{t('No project (global)')}</option>
        {#each projects.list as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
      <span class="hint">{t('Only projects where you are an operator (or better) accept it.')}</span>
    </label>
    <div class="dlg-btns">
      <button class="ui-btn" onclick={() => (projectDlg = false)}>{t('Cancel')}</button>
      <button class="ui-btn primary" onclick={() => void saveProject()}>{t('Move')}</button>
    </div>
  </Dialog>
  {#if addOpen}
    <!-- Loaded on first use: keeps the main bundle small. -->
    {#await import('../components/AddDeviceDialog.svelte') then { default: AddDeviceDialog }}
      <AddDeviceDialog open {detail} {active} start={addKind} onclose={() => (addOpen = false)} onchange={load} />
    {/await}
  {/if}
{/if}

<style>
  .dlg-lead {
    margin: 0;
    color: var(--text-2);
    font-size: 13px;
  }
  .dlg-btns {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
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
  .ident {
    display: flex;
    gap: 14px;
    min-width: 0;
  }
  .avatar {
    flex: none;
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    border: var(--frame) solid var(--border-strong);
    background: var(--metal);
    box-shadow: var(--bevel);
    color: var(--text-2);
  }
  .avatar.on {
    background: var(--metal-accent);
    color: var(--accent-contrast);
  }
  h1 {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .copyable {
    cursor: copy;
    font: inherit;
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .copyable:hover {
    color: var(--text);
    border-color: var(--border-strong);
  }
  .head-right {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: none;
  }
  .desc {
    margin: -8px 0 20px;
    color: var(--text-2);
    max-width: 75ch;
    white-space: pre-line;
  }
  .gone {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    margin-bottom: 18px;
    border: var(--frame) solid var(--warn);
    border-left-width: 6px;
    background: color-mix(in srgb, var(--warn) 10%, var(--surface));
    color: var(--text);
    font-size: 13px;
  }
  .gone :global(svg) {
    color: var(--warn);
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 16px;
    margin-bottom: 22px;
  }
  .stats :global(.card) {
    min-height: 128px;
  }
  .tabs {
    display: flex;
    gap: 3px;
    margin-bottom: 0;
    padding: 0 6px;
    overflow-x: auto;
    border-bottom: var(--frame) solid var(--ink);
  }
  .tabs button {
    flex: none;
    display: flex;
    align-items: center;
    gap: 7px;
    height: 34px;
    margin-bottom: -2px;
    padding: 0 13px;
    border: var(--frame) solid var(--ink);
    border-bottom: 0;
    background: var(--surface-2);
    box-shadow: var(--bevel);
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 15px;
    font-stretch: 80%;
    font-weight: 700;
    white-space: nowrap;
    cursor: pointer;
  }
  .tabs button:hover {
    color: var(--text);
  }
  .tabs button.on {
    position: relative;
    z-index: 1;
    height: 38px;
    margin-top: -4px;
    background: var(--invert-bg);
    box-shadow: none;
    color: var(--invert-fg);
  }
  .tabs button.on::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 2px;
    background: var(--invert-bg);
  }
  .tabs + :global(*) {
    margin-top: 22px;
  }
  .subtabs {
    display: inline-flex;
    gap: 2px;
    margin-bottom: 16px;
    padding: 3px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    box-shadow: var(--bevel-in);
  }
  .subtabs button {
    height: 30px;
    padding: 0 14px;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-2);
    font: inherit;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
  }
  .subtabs button:hover {
    color: var(--text);
  }
  .subtabs button.on {
    background: var(--metal);
    outline: 1px solid var(--border-strong);
    box-shadow: var(--bevel);
    color: var(--text);
  }
  .count {
    padding: 0 5px;
    border: 1px solid currentColor;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
  }
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr);
    gap: 16px;
    align-items: start;
  }
  .col {
    display: grid;
    gap: 16px;
  }
  .panel {
    padding: 16px 18px 12px;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .panel h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: -16px -18px 10px;
    padding: 8px 18px;
    border-bottom: var(--frame) solid var(--border-strong);
    background: var(--metal-head);
    font-size: 14px;
    font-weight: 700;
  }
  .panel h2::before,
  .group h2::before {
    content: '';
    flex: none;
    width: 8px;
    height: 8px;
    background: var(--accent);
    outline: 1px solid var(--ink);
  }
  .group h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 10px;
    padding-bottom: 6px;
    border-bottom: var(--frame) solid var(--border-strong);
    font-size: 14px;
    font-weight: 700;
  }
  .sub-block {
    margin-top: 10px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }
  h3 {
    margin: 0 0 8px;
    color: var(--text-2);
    font-size: 12px;
    font-weight: 700;
  }
  .boot {
    margin: 0 0 4px;
    padding: 0;
    list-style: none;
    counter-reset: boot;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .boot li {
    counter-increment: boot;
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 3px 10px 3px 3px;
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
    box-shadow: var(--bevel);
    font-size: 12.5px;
  }
  .boot li::before {
    content: counter(boot);
    display: grid;
    place-items: center;
    width: 18px;
    height: 18px;
    background: var(--accent);
    color: var(--accent-contrast);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
  }
  .hw {
    display: grid;
    gap: 22px;
  }
  .hw-tools {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: -8px;
  }
  .hw-hint {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .devices {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr));
    gap: 10px;
    align-items: start;
  }
  .more {
    justify-self: start;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    padding: 0 12px;
    border: var(--frame) dashed var(--border-strong);
    background: transparent;
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 12.5px;
    font-weight: 700;
    cursor: pointer;
  }
  .more:hover {
    color: var(--text);
    background: var(--surface-hover);
  }
  .chev {
    display: grid;
    transition: transform var(--dur) var(--ease-out);
  }
  .chev.up {
    transform: rotate(180deg);
  }
  .state-msg {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 72px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .state-msg strong {
    color: var(--text);
    font-size: 15px;
    font-weight: 600;
  }
  .link {
    border: 0;
    background: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0;
  }
  .skeleton {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .sk {
    display: block;
    height: 14px;
    border-radius: var(--radius-sm);
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  .sk-cards {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 16px;
    margin-top: 12px;
  }
  .sk.card {
    height: 128px;
    border-radius: var(--radius-sm);
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
  @media (max-width: 1200px) {
    .stats,
    .sk-cards {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  @media (max-width: 900px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 700px) {
    .page-head {
      flex-direction: column;
    }
    .head-right {
      width: 100%;
      justify-content: space-between;
    }
    .stats {
      grid-template-columns: 1fr;
    }
  }
</style>
