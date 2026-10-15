<script lang="ts">
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
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import ManageDialogs, { type ManageDialog } from '../components/ManageDialogs.svelte'
  import ChangesPanel from '../components/ChangesPanel.svelte'
  import QemuArgsCard from '../components/QemuArgsCard.svelte'
  import FirewallPanel from '../components/FirewallPanel.svelte'
  import MetricsPanel from '../components/MetricsPanel.svelte'
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

  async function load() {
    try {
      detail = await api.domain(uuid)
      error = null
    } catch (e) {
      error = e instanceof ApiError ? { status: e.status, message: e.message } : { status: 0, message: String(e) }
    }
  }

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

  type Tab = 'overview' | 'console' | 'metrics' | 'firewall' | 'settings' | 'hardware' | 'snapshots' | 'backups' | 'changes' | 'xml'
  const TABS: Tab[] = ['overview', 'console', 'metrics', 'firewall', 'settings', 'hardware', 'snapshots', 'backups', 'changes', 'xml']
  let tab = $derived<Tab>(
    router.route.name === 'machine' && TABS.includes(router.route.tab as Tab) ? (router.route.tab as Tab) : 'overview',
  )

  // ---- Management ---------------------------------------------------------------------
  let dlg = $state<ManageDialog>(null)
  let mediaDrive = $state<Device | undefined>()
  let addOpen = $state(false)
  let addKind = $state<ExtraDeviceKind | null>(null)

  let manageItems = $derived.by((): MenuItem[] => {
    if (!detail) return []
    const admin = session.why('admin') || undefined
    const items: MenuItem[] = [
      { label: 'Rename…', icon: Pencil, run: () => (dlg = 'rename'), disabled: session.whyManage(dom) || undefined },
      { label: 'Clone…', icon: CopyPlus, run: () => (dlg = 'clone'), disabled: admin },
    ]
    if (cluster.clustered && detail.persistent)
      items.push({ label: 'Move to another host…', icon: ArrowRightLeft, run: () => (dlg = 'move'), disabled: admin })
    if (detail.persistent)
      items.push(
        dom?.template
          ? { label: 'Turn into a normal machine', icon: LayoutTemplate, run: () => void setTemplate(false), disabled: admin }
          : { label: 'Make template…', icon: LayoutTemplate, run: () => (dlg = 'template'), disabled: admin },
      )
    if (detail.persistent)
      items.push({
        label: liveDom?.autostart ?? detail.autostart ? 'Don’t start with the host' : 'Start with the host',
        icon: Power,
        run: () => void setAutostart(!(liveDom?.autostart ?? detail!.autostart)),
        disabled: admin,
      })
    items.push({ label: 'Delete…', icon: Trash, run: () => (dlg = 'delete'), disabled: session.whyManage(dom) || undefined })
    return items
  })

  async function setTemplate(on: boolean) {
    try {
      await api.setTemplate(uuid, on)
      toasts.push({ kind: 'info', title: on ? 'Now a template' : `${detail?.name} is a normal machine again` })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not changed', detail: errorText(e) })
    }
  }

  async function setAutostart(on: boolean) {
    try {
      await api.setAutostart(uuid, on)
      toasts.push({ kind: 'info', title: on ? 'Starts with the host' : 'No longer starts with the host' })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Autostart not changed', detail: errorText(e) })
    }
  }

  /** Devices the Remove button handles, with its question. The rest: the XML tab. */
  const REMOVE: Partial<Record<string, string>> = {
    disk: 'Detach from machine?',
    cdrom: 'Detach from machine?',
    'hostdev-pci': 'Give it back to the host?',
    'hostdev-usb': 'Give it back to the host?',
    'hostdev-mdev': 'Give the slice back?',
    interface: 'Unplug this adapter?',
    filesystem: 'Stop sharing this folder?',
    watchdog: 'Remove the watchdog?',
    tpm: 'Remove the TPM and its keys?',
    rng: 'Remove it?',
    clipboard: 'Turn clipboard sharing off?',
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
        ? `Gone from the saved definition; the running machine keeps it until it stops: ${r.liveError}`
        : r.appliesOnNextBoot
          ? 'Gone from the next start; the running machine keeps it until then.'
          : d.kind === 'disk'
            ? 'The disk image stays in its pool.'
            : undefined,
    })
    await load()
  }
  const setTab = (t: Tab) => router.navigate(`/machines/${uuid}${t === 'overview' ? '' : '#' + t}`, { replace: true })

  // ---- Overview cards -------------------------------------------------------------

  const CPU_MODE: Record<string, string> = {
    'host-passthrough': 'Same as host (passthrough)',
    'host-model': 'Closest to the host CPU',
    maximum: 'Everything the hypervisor supports',
    custom: 'Named CPU model',
  }
  const POWER_EVENT: Record<string, string> = {
    destroy: 'Power off',
    restart: 'Restart',
    preserve: 'Keep for debugging',
    'rename-restart': 'Rename and restart',
    'coredump-destroy': 'Dump memory, then power off',
    'coredump-restart': 'Dump memory, then restart',
  }
  const FEATURE: Record<string, string> = {
    acpi: 'ACPI',
    apic: 'APIC',
    pae: 'PAE',
    hyperv: 'Hyper-V enlightenments',
    kvm: 'KVM tweaks',
    smm: 'SMM',
    vmport: 'VMware port',
    ioapic: 'IOAPIC',
    vmcoreinfo: 'vmcoreinfo',
    pvspinlock: 'PV spinlocks',
    pmu: 'PMU',
  }

  const p = (label: string, value: string | number | undefined | null, tech?: string): Prop | null =>
    value === undefined || value === null || value === '' ? null : { label, value: String(value), tech }
  const clean = (xs: (Prop | null)[]) => xs.filter((x): x is Prop => x !== null)

  let systemProps = $derived.by(() => {
    if (!detail) return []
    const s = detail.system
    const hv =
      s.hypervisor === 'kvm'
        ? 'KVM (hardware accelerated)'
        : s.hypervisor === 'qemu'
          ? 'QEMU emulation (no acceleration, slow)'
          : s.hypervisor
    const fw = s.firmware === 'uefi' ? (s.secureBoot ? 'UEFI with Secure Boot' : 'UEFI') : 'BIOS (legacy)'
    return clean([
      p('Hypervisor', hv, 'domain@type'),
      p('Architecture', s.arch, 'os/type@arch'),
      p('Chipset / machine type', s.machine, 'os/type@machine'),
      p('Firmware', fw, s.firmware === 'uefi' ? 'os@firmware / os/loader' : 'os/loader'),
      p('Firmware image', s.loader, 'os/loader'),
      p('UEFI variables', s.nvram, 'os/nvram'),
      p('Kernel (direct boot)', s.kernel, 'os/kernel'),
      p('Kernel command line', s.cmdline, 'os/cmdline'),
      p('Boot menu', s.bootMenu ? 'shown' : null, 'os/bootmenu@enable'),
      p('Clock', s.clock, 'clock@offset'),
      p('When the guest powers off', POWER_EVENT[s.onPoweroff ?? ''] ?? s.onPoweroff, 'on_poweroff'),
      p('When the guest reboots', POWER_EVENT[s.onReboot ?? ''] ?? s.onReboot, 'on_reboot'),
      p('When the guest crashes', POWER_EVENT[s.onCrash ?? ''] ?? s.onCrash, 'on_crash'),
      p('Emulator', s.emulator, 'devices/emulator'),
    ])
  })

  let cpuProps = $derived.by(() => {
    if (!detail) return []
    const c = detail.cpu
    const topo = `${plural(c.sockets, 'socket')} × ${plural(c.cores, 'core')} × ${plural(c.threads, 'thread')}`
    return clean([
      p('Virtual CPUs', c.vcpus === c.maxVcpus ? c.vcpus : `${c.vcpus} active of ${c.maxVcpus} (hot-pluggable)`, 'vcpu'),
      p('Topology', topo, 'cpu/topology'),
      p('CPU model', CPU_MODE[c.mode] ? CPU_MODE[c.mode] + (c.model ? ` · ${c.model}` : '') : c.mode, 'cpu@mode / cpu/model'),
      p('Feature overrides', c.features ? plural(c.features, 'feature') : null, 'cpu/feature'),
      p('Pinned to host CPUs', c.pinned ? 'yes' : null, 'cputune/vcpupin'),
      p('Allowed host CPUs', c.cpuset, 'vcpu@cpuset'),
      p('Guest NUMA nodes', c.numaNodes || null, 'cpu/numa/cell'),
    ])
  })

  let memoryProps = $derived.by(() => {
    if (!detail) return []
    const m = detail.memory
    return clean([
      p('Assigned', formatKiB(m.maxKiB), 'memory'),
      p('Current (balloon target)', m.currentKiB !== m.maxKiB ? formatKiB(m.currentKiB) : null, 'currentMemory'),
      p('Hot-plug maximum', m.hotplugKiB ? formatKiB(m.hotplugKiB) : null, 'maxMemory'),
      p('Huge pages', m.hugepages ? 'yes' : null, 'memoryBacking/hugepages'),
      p('Balloon device', m.balloon ? (m.balloon === 'virtio' ? 'VirtIO' : m.balloon) : 'none', 'devices/memballoon@model'),
    ])
  })

  // ---- Hardware --------------------------------------------------------------------

  const GROUPS: { key: DeviceGroup; label: string; advanced?: boolean }[] = [
    { key: 'storage', label: 'Storage' },
    { key: 'network', label: 'Network' },
    { key: 'display', label: 'Display' },
    { key: 'console', label: 'Consoles & channels' },
    { key: 'passthrough', label: 'Passthrough' },
    { key: 'input', label: 'Input & sound' },
    { key: 'system', label: 'System devices', advanced: true },
    { key: 'controller', label: 'Controllers', advanced: true },
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

  let title = $derived(dom ? `${dom.name} · VirtoMir` : 'Machine · VirtoMir')
</script>

<svelte:head><title>{title}</title></svelte:head>

<a class="back" href="/" onclick={router.link}><ArrowLeft size={15} /> Overview</a>

{#if error && !detail}
  <div class="state-msg">
    {#if error.status === 404}
      <SearchX size={30} strokeWidth={1.5} />
      <strong>This machine does not exist</strong>
      <span>It may have been deleted, or the link is wrong.</span>
    {:else}
      <TriangleAlert size={30} strokeWidth={1.5} />
      <strong>Could not load this machine</strong>
      <span class="mono">{error.message}</span>
      <button class="link" onclick={load}>Try again</button>
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
      This machine is no longer on the host. What you see is its last known configuration.
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
          <button class="chip mono copyable" title="Copy UUID" onclick={() => copy(uuid, 'UUID')}>{uuid}</button>
          <span class="chip" title={dom.persistent ? 'Defined: survives a host reboot' : 'Exists only while running'}>
            {dom.persistent ? 'persistent' : 'transient'}
          </span>
          {#if dom.autostart}<span class="chip" title="Starts when the host boots">autostart</span>{/if}
          {#if dom.template}<span class="chip accent" title="Never starts; new machines are made from it"><LayoutTemplate size={12} /> template</span>{/if}
          {#if dom.linkedFrom}
            {@const t = live.snapshot?.domains.find((d) => d.uuid === dom.linkedFrom)}
            <a class="chip link" href={machineHref(dom.linkedFrom)} onclick={router.link} title="Its disks only store changes on top of this template's disks">linked to {t?.name ?? 'template'}</a>
          {/if}
          <span class="chip mono" title="Firmware">{detail.system.firmware === 'uefi' ? 'UEFI' : 'BIOS'}{detail.system.secureBoot ? ' · Secure Boot' : ''}</span>
          {#if detail.system.machine}<span class="chip mono" title="Machine type">{detail.system.machine}</span>{/if}
        </div>
      </div>
    </div>
    <div class="head-right">
      {#if live.streamOpen && live.snapshot?.connected && active}
        <span class="live" title="Updated every second"><span class="live-dot"></span>Live</span>
      {/if}
      {#if !gone && liveDom}<MachineActions d={liveDom} size="lg" extra={manageItems} />{/if}
    </div>
  </header>

  {#if detail.description}<p class="desc">{detail.description}</p>{/if}

  <section class="stats">
    <StatCard
      label="CPU"
      icon={Cpu}
      value={active ? formatPercent(liveDom?.cpuPercent ?? 0) : '—'}
      unit={active ? '%' : ''}
      sub="{plural(detail.cpu.vcpus, 'vCPU')} · {plural(detail.cpu.sockets, 'socket')} × {plural(detail.cpu.cores, 'core')} × {plural(detail.cpu.threads, 'thread')}"
    >
      {#snippet chart()}<Sparkline data={live.domainCpu(uuid)} tick={live.tick} height={56} />{/snippet}
    </StatCard>
    <StatCard
      label="Memory"
      icon={MemoryStick}
      value={formatKiB(memCurrent).split(' ')[0]}
      unit={formatKiB(memCurrent).split(' ')[1] + (memCurrent !== memMax ? ` of ${formatKiB(memMax)}` : '')}
      sub={liveDom?.guestMemTotalKiB
        ? `Guest uses ${formatKiB(liveDom.guestMemUsedKiB ?? 0)} (${formatPercent(((liveDom.guestMemUsedKiB ?? 0) / liveDom.guestMemTotalKiB) * 100)} %)`
        : memCurrent !== memMax
          ? 'Balloon is holding memory back'
          : detail.memory.balloon
            ? 'Balloon can return memory to the host'
            : 'Fixed size'}
    />
    <StatCard
      label="Storage"
      icon={HardDrive}
      value={diskTotal ? formatBytes(diskTotal).split(' ')[0] : String(disks.length)}
      unit={diskTotal ? formatBytes(diskTotal).split(' ')[1] : disks.length === 1 ? 'disk' : 'disks'}
      sub={diskTotal ? `${plural(disks.length, 'disk')} · virtual size` : 'Size unknown'}
    />
    <StatCard
      label="Network"
      icon={EthernetPort}
      value={ipv4[0]?.split('/')[0] ?? (nics.length ? String(nics.length) : 'None')}
      unit={ipv4[0] ? '' : nics.length ? (nics.length === 1 ? 'adapter' : 'adapters') : ''}
      sub={ipv4.length > 1
        ? `+${ipv4.length - 1} more address${ipv4.length > 2 ? 'es' : ''}`
        : ipv4.length
          ? nics[0]?.summary
          : active
            ? 'No address reported yet'
            : 'Addresses appear while running'}
    />
  </section>

  <div class="tabs" role="tablist" aria-label="Machine sections">
    <button role="tab" aria-selected={tab === 'overview'} class:on={tab === 'overview'} onclick={() => setTab('overview')}>
      Overview
    </button>
    {#if session.can('operator')}
      <button role="tab" aria-selected={tab === 'console'} class:on={tab === 'console'} onclick={() => setTab('console')}>
        Console
      </button>
    {/if}
    <button role="tab" aria-selected={tab === 'metrics'} class:on={tab === 'metrics'} onclick={() => setTab('metrics')}>
      Metrics
    </button>
    <button role="tab" aria-selected={tab === 'firewall'} class:on={tab === 'firewall'} onclick={() => setTab('firewall')}>
      Firewall
    </button>
    <button role="tab" aria-selected={tab === 'settings'} class:on={tab === 'settings'} onclick={() => setTab('settings')}>
      Settings
    </button>
    <button role="tab" aria-selected={tab === 'hardware'} class:on={tab === 'hardware'} onclick={() => setTab('hardware')}>
      Hardware <span class="count num">{detail.devices.length}</span>
    </button>
    <button role="tab" aria-selected={tab === 'snapshots'} class:on={tab === 'snapshots'} onclick={() => setTab('snapshots')}>
      Snapshots
    </button>
    <button role="tab" aria-selected={tab === 'backups'} class:on={tab === 'backups'} onclick={() => setTab('backups')}>
      Backups
    </button>
    <button role="tab" aria-selected={tab === 'changes'} class:on={tab === 'changes'} onclick={() => setTab('changes')}>Changes</button>
    <button role="tab" aria-selected={tab === 'xml'} class:on={tab === 'xml'} onclick={() => setTab('xml')}>XML</button>
  </div>

  {#if tab === 'overview'}
    {#if liveDom}<PerformancePanel d={liveDom} devices={detail.devices} />{/if}
    <div class="grid" role="tabpanel">
      <section class="panel">
        <h2>System</h2>
        <PropList props={systemProps} />
        {#if detail.system.bootOrder.length}
          <div class="sub-block">
            <h3>Boot order</h3>
            <ol class="boot">
              {#each detail.system.bootOrder as b, i (b + i)}<li>{b}</li>{/each}
            </ol>
          </div>
        {/if}
        {#if detail.system.features.length}
          <div class="sub-block">
            <h3>Platform features</h3>
            <div class="chips">
              {#each detail.system.features as f (f)}<span class="chip" title="features/{f}">{FEATURE[f] ?? f}</span>{/each}
            </div>
          </div>
        {/if}
      </section>
      <div class="col">
        <section class="panel">
          <h2>Processor</h2>
          <PropList props={cpuProps} />
        </section>
        <section class="panel">
          <h2>Memory</h2>
          <PropList props={memoryProps} />
        </section>
      </div>
    </div>
  {:else if tab === 'hardware'}
    <div class="hw" role="tabpanel">
      <div class="hw-tools">
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'disk')}><Plus size={14} /> Add storage</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'nic')}><Plus size={14} /> Add network adapter</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (dlg = 'hostdev')}><Plus size={14} /> Add host device</button>
        <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((addKind = null), (addOpen = true))}><Plus size={14} /> Add other device…</button>
        <span class="hw-hint">{active ? 'Hot-plugged where the hardware allows; the rest from the next start.' : 'Anything else: the XML tab.'}</span>
      </div>
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
                        <Disc size={14} /> {d.summary?.endsWith('Empty') ? 'Insert media…' : 'Change media…'}
                      </button>
                    {/if}
                    {#if d.kind === 'disk' && d.target}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((mediaDrive = d), (dlg = 'resize'))}><Maximize2 size={14} /> Grow…</button>
                    {/if}
                    {#if d.kind === 'video'}
                      <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => ((addKind = 'video'), (addOpen = true))}><Pencil size={14} /> Change…</button>
                    {/if}
                    {#if removable(d)}
                      <ConfirmButton label="Remove" confirm={REMOVE[d.kind] ?? "Remove it?"} icon={Unplug} failure="Could not remove {d.title}" disabled={!session.can('admin')} title={session.why('admin')} run={() => detachDevice(d)} />
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
          {showAdvanced ? 'Hide' : 'Show'}
          {plural(advancedCount, 'more device')} (controllers, balloon, RNG, TPM…)
        </button>
      {/if}
      {#if detail.devices.length === 0}
        <div class="state-msg"><strong>No devices defined</strong></div>
      {/if}
      {#if detail.persistent}<QemuArgsCard {uuid} onsaved={load} />{/if}
    </div>
  {:else if tab === 'console'}
    <div role="tabpanel">
      {#if liveDom}<ConsolePanel d={liveDom} />{/if}
    </div>
  {:else if tab === 'metrics'}
    <div role="tabpanel">
      <MetricsPanel {uuid} {active} />
    </div>
  {:else if tab === 'firewall'}
    <div role="tabpanel">
      <FirewallPanel {uuid} name={dom.name} {active} />
    </div>
  {:else if tab === 'settings'}
    <div role="tabpanel">
      <SettingsPanel {uuid} {active} onsaved={load} />
    </div>
  {:else if tab === 'backups'}
    <div role="tabpanel">
      <BackupsPanel {uuid} name={dom.name} {active} />
    </div>
  {:else if tab === 'changes'}
    <div role="tabpanel">
      <ChangesPanel {uuid} {active} version={stateKey} onchange={load} />
    </div>
  {:else if tab === 'snapshots'}
    <div role="tabpanel">
      <SnapshotsPanel {uuid} {active} onchange={load} />
    </div>
  {:else}
    <div role="tabpanel">
      <XmlView {uuid} name={dom.name} {active} persistent={dom.persistent} version={stateKey} onsaved={load} />
    </div>
  {/if}

  <ManageDialogs which={dlg} {detail} drive={mediaDrive} {active} onclose={() => (dlg = null)} onchange={load} />
  {#if addOpen}
    <!-- Loaded on first use: keeps the main bundle small. -->
    {#await import('../components/AddDeviceDialog.svelte') then { default: AddDeviceDialog }}
      <AddDeviceDialog open {detail} {active} start={addKind} onclose={() => (addOpen = false)} onchange={load} />
    {/await}
  {/if}
{/if}

<style>
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
    margin-bottom: 18px;
    overflow-x: auto;
    border: var(--frame) solid var(--border-strong);
    background: var(--bg-elev);
    box-shadow: var(--bevel-in);
  }
  .tabs button {
    flex: none;
    display: flex;
    align-items: center;
    gap: 7px;
    height: 38px;
    padding: 0 16px;
    border: 0;
    border-right: 1px solid var(--border-strong);
    background: var(--metal);
    box-shadow: var(--bevel);
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 13px;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    white-space: nowrap;
    cursor: pointer;
  }
  .tabs button:hover {
    background: var(--metal-hover);
    color: var(--text);
  }
  .tabs button.on {
    background: var(--metal-accent);
    color: var(--accent-contrast);
  }
  .count {
    padding: 0 5px;
    border: 1px solid currentColor;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0;
    opacity: 0.85;
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
    border-radius: 0;
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
    letter-spacing: 0.08em;
    text-transform: uppercase;
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
    letter-spacing: 0.08em;
    text-transform: uppercase;
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
    letter-spacing: 0.1em;
    text-transform: uppercase;
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
    letter-spacing: 0.05em;
    text-transform: uppercase;
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
    border-radius: 0;
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
    border-radius: 0;
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
