// The demo's pretend cluster: three hosts with a handful of machines each, storage, networks,
// backups, alerts and history. Everything lives in memory; a reload starts over.
// Only used by the demo build (VITE_DEMO=1); never part of the binary's UI.

import type {
  ActivityEntry,
  Alert,
  AlertConfig,
  BackupInfo,
  ConfigVersion,
  Device,
  Domain,
  DomainDetail,
  DomainState,
  HostHardware,
  HostInfo,
  Lease,
  Pool,
  ScheduleInfo,
  SettingsView,
  SnapshotInfo,
  VNetwork,
  Volume,
} from '../api'
import type { Chassis, Firewall, GPU, HAStatus, HostInterface, PoolView, ReportMeta, ReportSchedule, Topology } from '../enterprise'

export interface DemoHost {
  id: string
  name: string
  self: boolean
  info: HostInfo
  /** Idle load of the host itself, on top of its machines (percent). */
  baseCpu: number
}

/** Workload shape: how busy a machine is and how its numbers move. */
export type Profile = 'web' | 'db' | 'desktop' | 'build' | 'gpu' | 'light' | 'k8s'

export interface DemoMachine {
  uuid: string
  host: string
  name: string
  title?: string
  description?: string
  os: 'debian' | 'ubuntu' | 'windows' | 'rocky' | 'alpine'
  state: DomainState
  vcpus: number
  memoryMiB: number
  diskGiB: number
  autostart: boolean
  template?: boolean
  linkedFrom?: string
  owner?: string
  profile: Profile
  network: string
  mac: string
  ip?: string
  firmware: 'uefi' | 'bios'
  secureBoot: boolean
  tpm?: boolean
  hugepages?: boolean
  gpu?: boolean
  display: 'vnc' | 'spice'
  /** Live counters. */
  cpu: number
  cpuTimeNs: number
  memUsedFrac: number
  snapshots: SnapshotInfo[]
  firewall: Firewall
  qemuArgs: { args: string[]; env: Record<string, string> }
}

const MiB = 1024
const GiB = 1024 * 1024 * 1024

export const HOSTS: DemoHost[] = [
  {
    id: 'a1b2c3d4e5f60718',
    name: 'hv-01',
    self: true,
    baseCpu: 3,
    info: {
      hostname: 'hv-01',
      hypervisor: 'QEMU',
      hypervisorVersion: '10.1.0',
      libvirtVersion: '12.0.0',
      cpuModel: 'AMD EPYC 9124 16-Core Processor',
      cpus: 64,
      cpuMHz: 3000,
      numaNodes: 2,
      sockets: 2,
      coresPerSocket: 16,
      threadsPerCore: 2,
      memoryKiB: 256 * 1024 * MiB,
    },
  },
  {
    id: 'b2c3d4e5f6071829',
    name: 'hv-02',
    self: false,
    baseCpu: 2,
    info: {
      hostname: 'hv-02',
      hypervisor: 'QEMU',
      hypervisorVersion: '10.1.0',
      libvirtVersion: '12.0.0',
      cpuModel: 'AMD EPYC 9124 16-Core Processor',
      cpus: 64,
      cpuMHz: 3000,
      numaNodes: 2,
      sockets: 2,
      coresPerSocket: 16,
      threadsPerCore: 2,
      memoryKiB: 256 * 1024 * MiB,
    },
  },
  {
    id: 'c3d4e5f60718293a',
    name: 'hv-03',
    self: false,
    baseCpu: 2,
    info: {
      hostname: 'hv-03',
      hypervisor: 'QEMU',
      hypervisorVersion: '10.1.0',
      libvirtVersion: '12.0.0',
      cpuModel: 'Intel(R) Xeon(R) Silver 4410Y',
      cpus: 48,
      cpuMHz: 2000,
      numaNodes: 1,
      sockets: 2,
      coresPerSocket: 12,
      threadsPerCore: 2,
      memoryKiB: 128 * 1024 * MiB,
    },
  },
]

export const SELF = HOSTS[0]

const noFirewall = (): Firewall => ({ enabled: false, defaultIn: 'accept', defaultOut: 'accept', antiSpoof: true, rules: [], adapters: ['vnet0'] })

function machine(m: Partial<DemoMachine> & Pick<DemoMachine, 'uuid' | 'host' | 'name' | 'os' | 'state' | 'vcpus' | 'memoryMiB' | 'diskGiB' | 'profile' | 'mac'>): DemoMachine {
  return {
    autostart: false,
    network: 'default',
    firmware: m.os === 'windows' ? 'uefi' : 'uefi',
    secureBoot: m.os === 'windows',
    display: 'vnc',
    cpu: 0,
    cpuTimeNs: Math.round(3.6e12 * (1 + m.vcpus)),
    memUsedFrac: 0.4,
    snapshots: [],
    firewall: noFirewall(),
    qemuArgs: { args: [], env: {} },
    ...m,
  }
}

const now = () => Date.now()
const ago = (ms: number) => new Date(now() - ms).toISOString()
const H = 3600_000
const D = 24 * H

export const machines: DemoMachine[] = [
  // hv-01
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', host: SELF.id, name: 'web-01', title: 'Public web (nginx)',
    description: 'Front end for the company site. Behind the load balancer with web-02.',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 4096, diskGiB: 40, autostart: true, profile: 'web',
    mac: '52:54:00:3a:11:01', ip: '192.168.122.11',
    firewall: {
      enabled: true, defaultIn: 'drop', defaultOut: 'accept', antiSpoof: true, adapters: ['vnet0'],
      rules: [
        { direction: 'in', action: 'accept', protocol: 'tcp', ports: '80,443', comment: 'HTTP(S) from anywhere' },
        { direction: 'in', action: 'accept', protocol: 'tcp', ports: '22', remote: '10.20.0.0/24', comment: 'SSH from the admin VLAN' },
        { direction: 'in', action: 'accept', protocol: 'icmp', comment: 'ping' },
      ],
    },
    snapshots: [
      { name: 'before-nginx-1.28', description: 'Before the nginx upgrade', state: 'shutoff', created: (now() - 9 * D) / 1000, current: false, memory: false, location: 'internal' },
      { name: 'tls-renewed', description: 'New certificate in place', parent: 'before-nginx-1.28', state: 'running', created: (now() - 2 * D) / 1000, current: true, memory: true, location: 'internal' },
    ],
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02', host: SELF.id, name: 'web-02', title: 'Public web (nginx)',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 4096, diskGiB: 40, autostart: true, profile: 'web',
    mac: '52:54:00:3a:11:02', ip: '192.168.122.12',
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03', host: SELF.id, name: 'db-01', title: 'PostgreSQL 17 primary',
    description: 'Main database. Huge pages, pinned to NUMA node 0.',
    os: 'rocky', state: 'running', vcpus: 8, memoryMiB: 32768, diskGiB: 500, autostart: true, profile: 'db', hugepages: true,
    mac: '52:54:00:3a:11:03', ip: '192.168.122.20', memUsedFrac: 0.82,
    snapshots: [
      { name: 'pre-pg17', description: 'PostgreSQL 16, before pg_upgrade', state: 'shutoff', created: (now() - 30 * D) / 1000, current: false, memory: false, location: 'internal' },
      { name: 'pg17-done', parent: 'pre-pg17', state: 'shutoff', created: (now() - 29 * D) / 1000, current: true, memory: false, location: 'internal' },
    ],
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e04', host: SELF.id, name: 'win11-design', title: 'Windows 11 workstation',
    os: 'windows', state: 'running', vcpus: 6, memoryMiB: 16384, diskGiB: 128, profile: 'desktop', tpm: true, display: 'spice',
    mac: '52:54:00:3a:11:04', ip: '192.168.122.40', memUsedFrac: 0.61,
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e05', host: SELF.id, name: 'gpu-train-01', title: 'Model training (vGPU)',
    description: 'NVIDIA vGPU slice, CUDA 13.',
    os: 'ubuntu', state: 'running', vcpus: 16, memoryMiB: 65536, diskGiB: 300, profile: 'gpu', gpu: true, owner: 'deniz',
    mac: '52:54:00:3a:11:05', ip: '192.168.122.50', memUsedFrac: 0.7,
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06', host: SELF.id, name: 'ci-runner-03',
    os: 'debian', state: 'shutoff', vcpus: 4, memoryMiB: 8192, diskGiB: 80, profile: 'build', owner: 'deniz',
    mac: '52:54:00:3a:11:06', linkedFrom: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e07',
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e07', host: SELF.id, name: 'tmpl-ubuntu-24.04', title: 'Ubuntu 24.04 cloud template',
    description: 'Cloud image with qemu-guest-agent. Deploy linked clones from it.',
    os: 'ubuntu', state: 'shutoff', vcpus: 2, memoryMiB: 2048, diskGiB: 20, profile: 'light', template: true,
    mac: '52:54:00:3a:11:07',
  }),
  // hv-02
  machine({
    uuid: '7a2b3c4d-1e2f-4a3b-9c8d-1a2b3c4d5e01', host: HOSTS[1].id, name: 'k8s-cp-01', title: 'Kubernetes control plane',
    os: 'ubuntu', state: 'running', vcpus: 4, memoryMiB: 8192, diskGiB: 60, autostart: true, profile: 'k8s',
    mac: '52:54:00:4b:22:01', ip: '10.30.0.11', network: 'k8s',
  }),
  machine({
    uuid: '7a2b3c4d-1e2f-4a3b-9c8d-1a2b3c4d5e02', host: HOSTS[1].id, name: 'k8s-worker-01',
    os: 'ubuntu', state: 'running', vcpus: 8, memoryMiB: 16384, diskGiB: 120, autostart: true, profile: 'k8s',
    mac: '52:54:00:4b:22:02', ip: '10.30.0.21', network: 'k8s',
  }),
  machine({
    uuid: '7a2b3c4d-1e2f-4a3b-9c8d-1a2b3c4d5e03', host: HOSTS[1].id, name: 'k8s-worker-02',
    os: 'ubuntu', state: 'running', vcpus: 8, memoryMiB: 16384, diskGiB: 120, autostart: true, profile: 'k8s',
    mac: '52:54:00:4b:22:03', ip: '10.30.0.22', network: 'k8s',
  }),
  machine({
    uuid: '7a2b3c4d-1e2f-4a3b-9c8d-1a2b3c4d5e04', host: HOSTS[1].id, name: 'monitoring', title: 'Grafana + Prometheus',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 6144, diskGiB: 200, autostart: true, profile: 'light',
    mac: '52:54:00:4b:22:04', ip: '192.168.122.30',
  }),
  // hv-03
  machine({
    uuid: '9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e01', host: HOSTS[2].id, name: 'dns-01',
    os: 'alpine', state: 'running', vcpus: 1, memoryMiB: 512, diskGiB: 4, autostart: true, profile: 'light', firmware: 'bios',
    mac: '52:54:00:5c:33:01', ip: '192.168.122.2',
  }),
  machine({
    uuid: '9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e02', host: HOSTS[2].id, name: 'mail-01',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 4096, diskGiB: 100, autostart: true, profile: 'light',
    mac: '52:54:00:5c:33:02', ip: '192.168.122.25',
  }),
  machine({
    uuid: '9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e03', host: HOSTS[2].id, name: 'legacy-erp', title: 'Old ERP (Windows Server 2016)',
    os: 'windows', state: 'crashed', vcpus: 4, memoryMiB: 8192, diskGiB: 160, profile: 'desktop', firmware: 'bios', secureBoot: false,
    mac: '52:54:00:5c:33:03',
  }),
]

export const byUUID = (uuid: string) => machines.find((m) => m.uuid === uuid)
export const onHost = (host: string) => machines.filter((m) => m.host === host)
export const hostByID = (id: string | null) => (id ? HOSTS.find((h) => h.id === id) : SELF)

// ---- Live numbers ----------------------------------------------------------------------------

/** Deterministic-enough wobble: a smooth curve plus noise. */
const wobble = (base: number, amp: number, period: number, phase: number) =>
  base + amp * Math.sin((now() / 1000 + phase) / period) + (Math.random() - 0.5) * amp * 0.6

const PROFILE: Record<Profile, { cpu: number; amp: number; disk: number; net: number }> = {
  web: { cpu: 18, amp: 10, disk: 0.6e6, net: 4e6 },
  db: { cpu: 42, amp: 18, disk: 38e6, net: 9e6 },
  desktop: { cpu: 12, amp: 9, disk: 1.5e6, net: 0.8e6 },
  build: { cpu: 70, amp: 25, disk: 20e6, net: 2e6 },
  gpu: { cpu: 78, amp: 12, disk: 12e6, net: 1.5e6 },
  light: { cpu: 4, amp: 3, disk: 0.2e6, net: 0.3e6 },
  k8s: { cpu: 26, amp: 14, disk: 3e6, net: 6e6 },
}

const running = (s: DomainState) => s === 'running' || s === 'blocked' || s === 'shutting-down'

export function liveDomain(m: DemoMachine, i: number): Domain {
  const base: Domain = {
    uuid: m.uuid,
    name: m.name,
    state: m.state,
    vcpus: m.vcpus,
    memoryKiB: m.memoryMiB * MiB,
    maxMemoryKiB: m.memoryMiB * MiB,
    cpuTimeNs: m.cpuTimeNs,
    persistent: true,
    autostart: m.autostart,
    template: m.template || undefined,
    linkedFrom: m.linkedFrom,
    owner: m.owner,
    cpuPercent: 0,
  }
  if (!running(m.state)) return base
  const p = PROFILE[m.profile]
  const cpu = Math.max(0.3, Math.min(99, wobble(p.cpu, p.amp, 25 + i * 3, i * 17)))
  m.cpu = cpu
  m.cpuTimeNs += Math.round((cpu / 100) * m.vcpus * 1e9)
  const id = 1 + machines.indexOf(m)
  const io = (v: number) => Math.max(0, wobble(v, v * 0.6, 9 + i, i * 5))
  return {
    ...base,
    id,
    cpuPercent: cpu,
    vcpuPercent: Array.from({ length: m.vcpus }, (_, k) => Math.max(0, Math.min(100, cpu + (Math.random() - 0.5) * 30 + (k % 3) * 4))),
    disks: [
      {
        name: m.os === 'windows' ? 'sda' : 'vda',
        readBps: io(p.disk * 0.6),
        writeBps: io(p.disk * 0.4),
        readIops: io(p.disk / 40000),
        writeIops: io(p.disk / 60000),
        readLatencyMs: Math.max(0.05, wobble(0.4, 0.3, 7, i)),
        writeLatencyMs: Math.max(0.05, wobble(0.9, 0.6, 7, i + 3)),
      },
    ],
    nets: [{ name: 'vnet' + (id - 1), rxBps: io(p.net), txBps: io(p.net * 0.7), rxPps: io(p.net / 900), txPps: io(p.net / 1100) }],
    guestMemTotalKiB: m.memoryMiB * MiB,
    guestMemUsedKiB: Math.round(m.memoryMiB * MiB * Math.min(0.97, m.memUsedFrac + (Math.random() - 0.5) * 0.02)),
    hostRssKiB: Math.round(m.memoryMiB * MiB * Math.min(1, m.memUsedFrac + 0.08)),
  }
}

export function snapshotOf(host: DemoHost, at = Date.now()) {
  const domains = onHost(host.id).map((m, i) => liveDomain(m, i))
  const used = domains.reduce((n, d) => n + (d.cpuPercent / 100) * d.vcpus, 0)
  const memUsed = onHost(host.id).reduce((n, m) => n + (running(m.state) ? m.memoryMiB * MiB : 0), 0) + 6 * 1024 * MiB
  const sum = (f: (d: Domain) => number) => domains.reduce((n, d) => n + f(d), 0)
  return {
    time: new Date(at).toISOString(),
    connected: true,
    host: {
      cpuPercent: Math.min(100, host.baseCpu + (used / host.info.cpus) * 100),
      memTotalKiB: host.info.memoryKiB,
      memUsedKiB: memUsed,
      io: {
        diskReadBps: sum((d) => d.disks?.[0]?.readBps ?? 0) * 1.05,
        diskWriteBps: sum((d) => d.disks?.[0]?.writeBps ?? 0) * 1.1,
        netRxBps: sum((d) => d.nets?.[0]?.rxBps ?? 0) * 1.02 + 2e4,
        netTxBps: sum((d) => d.nets?.[0]?.txBps ?? 0) * 1.02 + 1e4,
      },
    },
    domains,
  }
}

// ---- Machine page model ---------------------------------------------------------------------

const imagePath = (m: DemoMachine) => `/var/lib/libvirt/images/${m.name}.qcow2`
const osLabel: Record<DemoMachine['os'], string> = {
  debian: 'Debian 13',
  ubuntu: 'Ubuntu 24.04',
  windows: 'Windows',
  rocky: 'Rocky Linux 10',
  alpine: 'Alpine 3.22',
}

export function detail(m: DemoMachine): DomainDetail {
  const win = m.os === 'windows'
  const bus = win ? 'SATA' : 'VirtIO'
  const dev = win ? 'sda' : 'vda'
  const pci = (slot: number) => `PCI 0000:0${slot}:00.0`
  const devices: Device[] = [
    {
      kind: 'disk', group: 'storage', title: 'Disk', target: dev, bootOrder: 1,
      summary: `${bus} · QCOW2 · ${m.name}.qcow2`,
      capacityBytes: m.diskGiB * GiB, allocationBytes: Math.round(m.diskGiB * GiB * (m.template ? 0.18 : 0.46)),
      props: [
        { label: 'Source', value: imagePath(m), tech: 'source@file' },
        { label: 'Bus', value: bus, tech: 'target@bus' },
        { label: 'Device name in guest', value: dev, tech: 'target@dev' },
        { label: 'Image format', value: 'qcow2', tech: 'driver@type' },
        { label: 'Cache mode', value: 'none', tech: 'driver@cache' },
        { label: 'I/O mode', value: 'native', tech: 'driver@io' },
        { label: 'Discard (TRIM)', value: 'unmap', tech: 'driver@discard' },
        ...(m.linkedFrom ? [{ label: 'Backing image', value: '/var/lib/libvirt/images/tmpl-ubuntu-24.04.qcow2', tech: 'backingStore/source@file' }] : []),
        { label: 'Guest address', value: pci(4), tech: 'address' },
      ],
    },
    {
      kind: 'cdrom', group: 'storage', title: 'CD/DVD drive', target: 'sdb',
      summary: 'SATA · RAW · Empty',
      props: [
        { label: 'Bus', value: 'SATA', tech: 'target@bus' },
        { label: 'Device name in guest', value: 'sdb', tech: 'target@dev' },
        { label: 'Read-only', value: 'yes', tech: 'readonly' },
      ],
    },
    {
      kind: 'interface', group: 'network', title: 'Network adapter', mac: m.mac,
      summary: `Virtual network ${m.network} · ${win ? 'e1000e' : 'VirtIO'}`,
      props: [
        { label: 'Connected to', value: `Virtual network ${m.network}`, tech: 'source@network' },
        { label: 'MAC address', value: m.mac, tech: 'mac@address' },
        { label: 'Adapter model', value: win ? 'e1000e' : 'VirtIO', tech: 'model@type' },
        ...(running(m.state) ? [{ label: 'Host interface', value: 'vnet' + machines.indexOf(m), tech: 'target@dev' }] : []),
        ...(m.firewall.enabled ? [{ label: 'Network filter', value: `virtomir-${m.name}`, tech: 'filterref@filter' }] : []),
        { label: 'Guest address', value: pci(1), tech: 'address' },
      ],
      // Reported by the guest agent while the machine runs.
      addresses: running(m.state) && m.ip ? [`${m.ip}/24`, `fe80::5054:ff:${m.mac.slice(9, 11)}${m.mac.slice(12, 14)}:${m.mac.slice(15)}/64`] : undefined,
    },
    {
      kind: 'graphics', group: 'display', title: m.display === 'spice' ? 'SPICE display' : 'VNC display',
      summary: 'localhost · automatic port',
      props: [
        { label: 'Listen address', value: '127.0.0.1', tech: 'listen@address' },
        { label: 'Port', value: 'automatic', tech: 'graphics@port' },
      ],
    },
    { kind: 'video', group: 'display', title: 'Video card', summary: win ? 'QXL' : 'VirtIO', props: [{ label: 'Model', value: win ? 'qxl' : 'virtio', tech: 'model@type' }, { label: 'Monitors', value: '1', tech: 'model@heads' }] },
    { kind: 'serial', group: 'console', title: 'Serial port', summary: 'pty', props: [{ label: 'Type', value: 'pty', tech: 'serial@type' }] },
    { kind: 'channel', group: 'console', title: 'Guest agent channel', summary: 'org.qemu.guest_agent.0', props: [{ label: 'Name', value: 'org.qemu.guest_agent.0', tech: 'target@name' }] },
    { kind: 'input', group: 'input', title: 'Tablet', summary: 'USB', props: [{ label: 'Bus', value: 'USB', tech: 'input@bus' }] },
    { kind: 'rng', group: 'system', title: 'Random number generator', summary: 'VirtIO · /dev/urandom', props: [{ label: 'Source', value: '/dev/urandom', tech: 'backend' }] },
    { kind: 'controller', group: 'controller', title: 'USB controller 0', summary: 'qemu-xhci', props: [{ label: 'Model', value: 'qemu-xhci', tech: 'controller@model' }] },
  ]
  if (m.tpm) devices.push({ kind: 'tpm', group: 'system', title: 'TPM', summary: 'Emulated · 2.0', props: [{ label: 'Version', value: '2.0', tech: 'backend@version' }] })
  if (m.gpu)
    devices.push({
      kind: 'hostdev', group: 'passthrough', title: 'vGPU slice', summary: 'NVIDIA L40S · nvidia-1155 (L40S-12Q)',
      props: [
        { label: 'Type', value: 'Mediated device (vGPU)', tech: 'hostdev@type=mdev' },
        { label: 'UUID', value: '2b9d0d1e-6a47-4c61-9a0b-6a1f2c0e3d11', tech: 'source/address@uuid' },
        { label: 'Card', value: '0000:41:00.0', tech: '' },
      ],
    })
  const bootOrder = ['hd', 'cdrom']
  return {
    uuid: m.uuid,
    name: m.name,
    id: running(m.state) ? machines.indexOf(m) + 1 : undefined,
    state: m.state,
    vcpus: m.vcpus,
    memoryKiB: m.memoryMiB * MiB,
    maxMemoryKiB: m.memoryMiB * MiB,
    cpuTimeNs: m.cpuTimeNs,
    persistent: true,
    autostart: m.autostart,
    template: m.template,
    linkedFrom: m.linkedFrom,
    owner: m.owner,
    title: m.title,
    description: m.description,
    system: {
      hypervisor: 'kvm',
      osType: 'hvm',
      arch: 'x86_64',
      machine: 'pc-q35-10.1',
      firmware: m.firmware,
      secureBoot: m.secureBoot,
      loader: m.firmware === 'uefi' ? (m.secureBoot ? '/usr/share/OVMF/OVMF_CODE_4M.ms.fd' : '/usr/share/OVMF/OVMF_CODE_4M.fd') : undefined,
      nvram: m.firmware === 'uefi' ? `/var/lib/libvirt/qemu/nvram/${m.name}_VARS.fd` : undefined,
      bootOrder,
      bootMenu: false,
      emulator: '/usr/bin/qemu-system-x86_64',
      clock: win ? 'localtime' : 'utc',
      features: win ? ['acpi', 'apic', 'hyperv', 'smm'] : ['acpi', 'apic', ...(m.secureBoot ? ['smm'] : [])],
      onPoweroff: 'destroy',
      onReboot: 'restart',
      onCrash: 'destroy',
    },
    cpu: {
      mode: 'host-passthrough',
      sockets: 1,
      cores: m.vcpus,
      threads: 1,
      vcpus: m.vcpus,
      maxVcpus: m.vcpus,
      features: 0,
      numaNodes: m.hugepages ? 1 : 0,
      pinned: !!m.hugepages,
      cpuset: m.hugepages ? '0-7' : undefined,
    },
    memory: { maxKiB: m.memoryMiB * MiB, currentKiB: m.memoryMiB * MiB, hugepages: !!m.hugepages, balloon: 'virtio' },
    devices,
  }
}

export function settings(m: DemoMachine): SettingsView {
  return {
    title: m.title ?? '',
    description: m.description ?? '',
    vcpus: m.vcpus,
    maxVcpus: m.vcpus,
    cpuMode: 'host-passthrough',
    sockets: 1,
    cores: m.vcpus,
    threads: 1,
    memoryMiB: m.memoryMiB,
    currentMemoryMiB: m.memoryMiB,
    bootOrder: ['hd', 'cdrom'],
    bootMenu: false,
    firmware: m.firmware,
    secureBoot: m.secureBoot,
    pinning: Array.from({ length: m.vcpus }, (_, i) => (m.hugepages ? String(i) : '')),
    emulatorPin: m.hugepages ? '8-9' : '',
    hugePages: !!m.hugepages,
    numaMode: m.hugepages ? 'strict' : '',
    numaNodes: m.hugepages ? '0' : '',
    bootDevices: [
      { key: 'hd', label: 'Hard disk' },
      { key: 'cdrom', label: 'CD/DVD' },
      { key: 'network', label: 'Network (PXE)' },
    ],
  }
}

export function domainXML(m: DemoMachine): string {
  const win = m.os === 'windows'
  const dev = win ? 'sda' : 'vda'
  const bus = win ? 'sata' : 'virtio'
  const loader =
    m.firmware === 'uefi'
      ? `\n    <loader readonly='yes' secure='${m.secureBoot ? 'yes' : 'no'}' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M${m.secureBoot ? '.ms' : ''}.fd</loader>\n    <nvram>/var/lib/libvirt/qemu/nvram/${m.name}_VARS.fd</nvram>`
      : ''
  return `<domain type='kvm'${running(m.state) ? ` id='${machines.indexOf(m) + 1}'` : ''}>
  <name>${m.name}</name>
  <uuid>${m.uuid}</uuid>${m.title ? `\n  <title>${m.title}</title>` : ''}${m.description ? `\n  <description>${m.description}</description>` : ''}
  <metadata>
    <virtomir:machine xmlns:virtomir="https://virtomir.dev/xmlns/machine/1">
      <virtomir:os>${osLabel[m.os]}</virtomir:os>${m.owner ? `\n      <virtomir:owner>${m.owner}</virtomir:owner>` : ''}${m.template ? '\n      <virtomir:template/>' : ''}
    </virtomir:machine>
  </metadata>
  <memory unit='KiB'>${m.memoryMiB * MiB}</memory>
  <currentMemory unit='KiB'>${m.memoryMiB * MiB}</currentMemory>${m.hugepages ? `\n  <memoryBacking>\n    <hugepages/>\n  </memoryBacking>` : ''}
  <vcpu placement='static'>${m.vcpus}</vcpu>
  <os>
    <type arch='x86_64' machine='pc-q35-10.1'>hvm</type>${loader}
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/>
    <apic/>${m.secureBoot ? '\n    <smm state=\'on\'/>' : ''}
  </features>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
  <clock offset='${win ? 'localtime' : 'utc'}'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none' io='native' discard='unmap'/>
      <source file='${imagePath(m)}'/>
      <target dev='${dev}' bus='${bus}'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <mac address='${m.mac}'/>
      <source network='${m.network}'/>
      <model type='${win ? 'e1000e' : 'virtio'}'/>
    </interface>
    <serial type='pty'>
      <target type='isa-serial' port='0'/>
    </serial>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <input type='tablet' bus='usb'/>
    <graphics type='${m.display}' autoport='yes' listen='127.0.0.1'/>
    <video>
      <model type='${win ? 'qxl' : 'virtio'}' heads='1' primary='yes'/>
    </video>${m.tpm ? `\n    <tpm model='tpm-crb'>\n      <backend type='emulator' version='2.0'/>\n    </tpm>` : ''}
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
  </devices>
</domain>
`
}

/** An earlier definition: each older version had less memory, fewer vCPUs, no description. */
export function configVersionXML(m: DemoMachine, id: string): string {
  const age = { v4: 0, v3: 1, v2: 2, v1: 3 }[id] ?? 0
  if (age === 0) return domainXML(m)
  const older: DemoMachine = {
    ...m,
    memoryMiB: age >= 2 ? Math.max(1024, m.memoryMiB / 2) : m.memoryMiB,
    vcpus: age >= 3 ? Math.max(1, m.vcpus / 2) : m.vcpus,
    description: age >= 1 ? undefined : m.description,
    autostart: age < 3 && m.autostart,
  }
  return domainXML(older)
}

export function configVersions(m: DemoMachine): ConfigVersion[] {
  return [
    { id: 'v4', time: ago(2 * D), user: 'onur', source: 'virtomir', size: 2310 },
    { id: 'v3', time: ago(6 * D), source: 'outside', size: 2288 },
    { id: 'v2', time: ago(13 * D), user: 'onur', source: 'virtomir', size: 2264 },
    { id: 'v1', time: ago(40 * D + m.vcpus * H), source: 'first seen', size: 2190 },
  ]
}

// ---- Storage & networks ---------------------------------------------------------------------

export const pools: Pool[] = [
  { name: 'default', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000001', type: 'dir', path: '/var/lib/libvirt/images', active: true, autostart: true, capacityBytes: 3.6e12, allocationBytes: 1.9e12, availableBytes: 1.7e12, volumes: 12 },
  { name: 'iso', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000002', type: 'dir', path: '/srv/iso', active: true, autostart: true, capacityBytes: 0.5e12, allocationBytes: 0.06e12, availableBytes: 0.44e12, volumes: 5 },
  { name: 'nas-nfs', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000003', type: 'netfs', path: '/mnt/nas-vms', active: true, autostart: true, capacityBytes: 16e12, allocationBytes: 11.2e12, availableBytes: 4.8e12, volumes: 9 },
  { name: 'fast-zfs', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000004', type: 'zfs', active: true, autostart: true, capacityBytes: 1.8e12, allocationBytes: 1.66e12, availableBytes: 0.14e12, volumes: 4 },
]

export function volumes(pool: string): Volume[] {
  if (pool === 'iso')
    return [
      { name: 'debian-13.1.0-amd64-netinst.iso', path: '/srv/iso/debian-13.1.0-amd64-netinst.iso', type: 'file', format: 'iso', capacityBytes: 7.9e8, allocationBytes: 7.9e8, usedBy: [] },
      { name: 'ubuntu-24.04.3-live-server-amd64.iso', path: '/srv/iso/ubuntu-24.04.3-live-server-amd64.iso', type: 'file', format: 'iso', capacityBytes: 3.1e9, allocationBytes: 3.1e9, usedBy: [] },
      { name: 'Win11_25H2_English_x64.iso', path: '/srv/iso/Win11_25H2_English_x64.iso', type: 'file', format: 'iso', capacityBytes: 7.3e9, allocationBytes: 7.3e9, usedBy: [] },
      { name: 'virtio-win-0.1.285.iso', path: '/srv/iso/virtio-win-0.1.285.iso', type: 'file', format: 'iso', capacityBytes: 7.5e8, allocationBytes: 7.5e8, usedBy: [] },
      { name: 'noble-server-cloudimg-amd64.img', path: '/srv/iso/noble-server-cloudimg-amd64.img', type: 'file', format: 'qcow2', capacityBytes: 3.5e9, allocationBytes: 6.0e8, usedBy: [] },
    ]
  if (pool !== 'default') return []
  const vols: Volume[] = onHost(SELF.id).map((m) => ({
    name: `${m.name}.qcow2`,
    path: imagePath(m),
    type: 'file',
    format: 'qcow2',
    capacityBytes: m.diskGiB * GiB,
    allocationBytes: Math.round(m.diskGiB * GiB * (m.template ? 0.18 : 0.46)),
    backing: m.linkedFrom ? '/var/lib/libvirt/images/tmpl-ubuntu-24.04.qcow2' : undefined,
    usedBy: [m.name],
  }))
  vols.push({ name: 'old-test.qcow2', path: '/var/lib/libvirt/images/old-test.qcow2', type: 'file', format: 'qcow2', capacityBytes: 40 * GiB, allocationBytes: 9 * GiB, usedBy: [] })
  return vols
}

export const networks: VNetwork[] = [
  {
    name: 'default', uuid: '5b0f9e1c-2a8e-4f17-b6c4-000000000001', active: true, autostart: true, persistent: true, mode: 'nat',
    bridge: 'virbr0', subnet: '192.168.122.1/24', dhcpStart: '192.168.122.100', dhcpEnd: '192.168.122.254', subnet6: 'fd00:122::1/64',
    usedBy: ['web-01', 'web-02', 'db-01', 'win11-design', 'gpu-train-01', 'ci-runner-03'],
    dhcpHosts: [
      { mac: '52:54:00:3a:11:01', ip: '192.168.122.11', name: 'web-01' },
      { mac: '52:54:00:3a:11:02', ip: '192.168.122.12', name: 'web-02' },
      { mac: '52:54:00:3a:11:03', ip: '192.168.122.20', name: 'db-01' },
    ],
  },
  {
    name: 'lan-bridge', uuid: '5b0f9e1c-2a8e-4f17-b6c4-000000000002', active: true, autostart: true, persistent: true, mode: 'bridge',
    bridge: 'br0', usedBy: [], dhcpHosts: [],
  },
  {
    name: 'storage-isolated', uuid: '5b0f9e1c-2a8e-4f17-b6c4-000000000003', active: true, autostart: true, persistent: true, mode: 'isolated',
    bridge: 'virbr2', subnet: '10.99.0.1/24', dhcpStart: '10.99.0.100', dhcpEnd: '10.99.0.200', usedBy: [], dhcpHosts: [],
  },
]

export function leases(net: string): Lease[] {
  return machines
    .filter((m) => m.host === SELF.id && m.network === net && m.ip && running(m.state))
    .map((m) => ({ mac: m.mac, ip: m.ip!, hostname: m.name, expires: Math.round(now() / 1000) + 3000, machine: m.name }))
}

export const networkXML = (n: VNetwork) => `<network>
  <name>${n.name}</name>
  <uuid>${n.uuid}</uuid>
  ${n.mode === 'bridge' ? `<forward mode='bridge'/>\n  <bridge name='${n.bridge}'/>` : `${n.mode === 'nat' ? "<forward mode='nat'/>\n  " : ''}<bridge name='${n.bridge}' stp='on' delay='0'/>`}${
    n.subnet
      ? `\n  <ip address='${n.subnet.split('/')[0]}' prefix='24'>\n    <dhcp>\n      <range start='${n.dhcpStart}' end='${n.dhcpEnd}'/>${n.dhcpHosts.map((h) => `\n      <host mac='${h.mac}' name='${h.name}' ip='${h.ip}'/>`).join('')}\n    </dhcp>\n  </ip>`
      : ''
  }
</network>
`

export const poolXML = (p: Pool) => `<pool type='${p.type}'>
  <name>${p.name}</name>
  <uuid>${p.uuid}</uuid>
  <capacity unit='bytes'>${p.capacityBytes}</capacity>
  <allocation unit='bytes'>${p.allocationBytes}</allocation>
  <available unit='bytes'>${p.availableBytes}</available>
  <target>
    <path>${p.path ?? '/dev/zvol/fast'}</path>
  </target>
</pool>
`

// ---- Backups, schedules, alerts, activity ---------------------------------------------------

export const backups: BackupInfo[] = [
  ['web-01', 0, 1.2e9], ['db-01', 0, 41e9], ['web-01', 1, 1.2e9], ['db-01', 1, 40.6e9], ['win11-design', 2, 23e9], ['db-01', 2, 40.1e9], ['web-02', 3, 1.1e9],
].map(([name, days, size], i) => {
  const m = machines.find((x) => x.name === name)!
  const created = new Date(now() - (days as number) * D - 4 * H).toISOString()
  return {
    id: `${created.slice(0, 10)}-${name}-${i}`,
    domain: name as string,
    domainUuid: m.uuid,
    created,
    live: m.state === 'running',
    automatic: i !== 4,
    disks: [{ target: m.os === 'windows' ? 'sda' : 'vda', file: (m.os === 'windows' ? 'sda' : 'vda') + '.qcow2', source: imagePath(m), sizeBytes: size as number }],
    sizeBytes: size as number,
    durationSec: Math.round((size as number) / 4e8),
  }
})

export const schedules: ScheduleInfo[] = [
  { id: 'sch-db-backup', kind: 'backup', domain: 'db-01', enabled: true, every: 'daily', at: '02:30', keep: 7, lastRun: ago(18 * H), lastResult: 'Backup done (40.6 GB, 1m42s)', nextRun: new Date(now() + 6 * H).toISOString() },
  { id: 'sch-web-backup', kind: 'backup', domain: 'web-01', enabled: true, every: 'daily', at: '03:00', keep: 5, lastRun: ago(17 * H), lastResult: 'Backup done (1.2 GB, 4s)', nextRun: new Date(now() + 7 * H).toISOString() },
  { id: 'sch-db-snap', kind: 'snapshot', domain: 'db-01', enabled: true, every: 'hours', hours: 4, keep: 6, lastRun: ago(2 * H), lastResult: 'Snapshot auto-0412 taken', nextRun: new Date(now() + 2 * H).toISOString() },
  { id: 'sch-win-backup', kind: 'backup', domain: 'win11-design', enabled: false, every: 'weekly', weekday: 6, at: '22:00', keep: 4, lastRun: ago(9 * D), lastError: 'The pool nas-nfs was not mounted', nextRun: undefined },
]

export const alerts: Alert[] = [
  { id: 'al-1', ruleId: 'r-vm-crashed', kind: 'vm.crashed', severity: 'critical', subject: 'legacy-erp', subjectId: '9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e03', message: 'legacy-erp crashed (guest panic)', started: ago(38 * 60_000) },
  { id: 'al-2', ruleId: 'r-pool-free', kind: 'pool.free', severity: 'warning', subject: 'fast-zfs', message: 'Pool fast-zfs has 7.8% free (below 10%)', value: 7.8, started: ago(5 * H) },
  { id: 'al-3', ruleId: 'r-vm-cpu', kind: 'vm.cpu', severity: 'warning', subject: 'gpu-train-01', subjectId: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e05', message: 'gpu-train-01 CPU above 90% for 10 minutes', value: 94, started: ago(3 * D), resolved: ago(3 * D - 50 * 60_000), ackedBy: 'onur', ackedAt: ago(3 * D - 20 * 60_000) },
]

export const alertConfig: AlertConfig = {
  rules: [
    { id: 'r-host-cpu', kind: 'host.cpu', enabled: true, threshold: 90, forSec: 300, severity: 'warning' },
    { id: 'r-host-mem', kind: 'host.mem', enabled: true, threshold: 92, forSec: 300, severity: 'critical' },
    { id: 'r-vm-cpu', kind: 'vm.cpu', enabled: true, threshold: 90, forSec: 600, severity: 'warning' },
    { id: 'r-vm-crashed', kind: 'vm.crashed', enabled: true, forSec: 0, severity: 'critical' },
    { id: 'r-vm-down', kind: 'vm.down', enabled: true, forSec: 60, severity: 'critical', target: 'db-01' },
    { id: 'r-pool-free', kind: 'pool.free', enabled: true, threshold: 10, forSec: 0, severity: 'warning' },
    { id: 'r-libvirt', kind: 'libvirt.down', enabled: true, forSec: 30, severity: 'critical' },
  ],
  channels: [
    { id: 'ch-ntfy', name: 'Phone (ntfy)', kind: 'webhook', enabled: true, minSeverity: 'critical', url: 'https://ntfy.sh/••••••••', format: 'ntfy', resolved: true },
    { id: 'ch-mail', name: 'Ops mailbox', kind: 'email', enabled: true, minSeverity: 'warning', to: 'ops@example.com', resolved: false },
  ],
  smtp: { host: 'smtp.example.com', port: 587, username: 'virtomir', password: '••••••••', from: 'virtomir@example.com', tls: 'starttls' },
}

const act = (minsAgo: number, user: string, method: string, path: string, status = 200, error?: string): ActivityEntry => ({
  time: ago(minsAgo * 60_000), user, remote: '10.20.0.15:52344', method, path, status, error,
})
export const activity: ActivityEntry[] = [
  act(4, 'onur', 'POST', '/api/v1/domains/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e04/actions/start', 204),
  act(22, 'deniz', 'POST', '/api/v1/domains/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e05/snapshots', 200),
  act(41, 'onur', 'PUT', '/api/v1/domains/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01/firewall', 200),
  act(95, 'deniz', 'POST', '/api/v1/domains', 403, 'your quota allows 64 GiB of memory; this would make 72 GiB'),
  act(130, 'onur', 'PUT', '/api/v1/domains/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03/settings', 200),
  act(360, 'ci-bot (token)', 'POST', '/api/v1/domains/4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e07/deploy', 200),
  act(1440, 'onur', 'POST', '/api/v1/storage/pools/iso/download', 200),
  act(2880, 'onur', 'POST', '/api/v1/networks', 201),
]

// ---- Host hardware --------------------------------------------------------------------------

export const hardware: HostHardware = {
  iommu: true,
  pci: [
    { name: 'pci_0000_41_00_0', address: '0000:41:00.0', class: '0x030200', className: '3D controller', vendor: 'NVIDIA Corporation', product: 'AD102GL [L40S]', vendorId: '10de', productId: '26b9', driver: 'nvidia', iommuGroup: 34, usedBy: [], mdevTypes: [
      { id: 'nvidia-1155', name: 'NVIDIA L40S-12Q', api: 'vfio-pci', available: 3 },
      { id: 'nvidia-1156', name: 'NVIDIA L40S-24Q', api: 'vfio-pci', available: 1 },
    ] },
    { name: 'pci_0000_c1_00_0', address: '0000:c1:00.0', class: '0x020000', className: 'Ethernet controller', vendor: 'Intel Corporation', product: 'Ethernet Controller E810-XXV', vendorId: '8086', productId: '159b', driver: 'ice', iommuGroup: 51, usedBy: [], hostUse: 'Interface enp193s0f0np0 has an address (10.20.0.11/24)', blocked: 'The host uses it: enp193s0f0np0 carries the host\'s own address.' },
    { name: 'pci_0000_c1_00_1', address: '0000:c1:00.1', class: '0x020000', className: 'Ethernet controller', vendor: 'Intel Corporation', product: 'Ethernet Controller E810-XXV', vendorId: '8086', productId: '159b', driver: 'ice', iommuGroup: 52, usedBy: [] },
    { name: 'pci_0000_21_00_0', address: '0000:21:00.0', class: '0x010802', className: 'Non-Volatile memory controller', vendor: 'Samsung Electronics Co Ltd', product: 'NVMe SSD Controller PM9A3', vendorId: '144d', productId: 'a80a', driver: 'nvme', iommuGroup: 12, usedBy: [], hostUse: 'Mounted at / and /var/lib/libvirt', blocked: 'The host needs it: it holds /.' },
    { name: 'pci_0000_22_00_0', address: '0000:22:00.0', class: '0x010802', className: 'Non-Volatile memory controller', vendor: 'Samsung Electronics Co Ltd', product: 'NVMe SSD Controller PM9A3', vendorId: '144d', productId: 'a80a', driver: 'vfio-pci', iommuGroup: 13, usedBy: ['db-01'] },
  ],
  usb: [
    { name: 'usb_1_4', bus: 1, device: 4, vendorId: '0529', productId: '0001', vendor: 'Aladdin Knowledge Systems', product: 'HASP licence dongle', usedBy: ['win11-design'] },
    { name: 'usb_1_2', bus: 1, device: 2, vendorId: '046d', productId: 'c31c', vendor: 'Logitech, Inc.', product: 'Keyboard K120', usedBy: [] },
  ],
  mdevs: [
    { name: 'mdev_2b9d0d1e', uuid: '2b9d0d1e-6a47-4c61-9a0b-6a1f2c0e3d11', parent: '0000:41:00.0', type: 'nvidia-1155', typeName: 'NVIDIA L40S-12Q', active: true, autostart: true, usedBy: ['gpu-train-01'] },
  ],
}

export const chassis: Chassis = {
  vendor: 'Supermicro', product: 'AS-1125HS-TNR', version: '0123456789', serial: 'S7Q00DEMO', boardVendor: 'Supermicro', boardName: 'H13DSH',
  biosVendor: 'American Megatrends International, LLC.', biosVersion: '1.6', biosDate: '05/14/2026', chassisType: 'Rack Mount Chassis',
  virtual: false, os: 'Debian GNU/Linux 13 (trixie)', kernel: '6.12.48-1-amd64', uptimeSec: 41 * 86400 + 5 * 3600, bootTime: ago(41 * D + 5 * H),
  sensors: [
    { chip: 'k10temp-pci-00c3', label: 'CPU 1 Tctl', kind: 'temperature', value: 54, unit: '°C', max: 95, crit: 100 },
    { chip: 'k10temp-pci-00cb', label: 'CPU 2 Tctl', kind: 'temperature', value: 49, unit: '°C', max: 95, crit: 100 },
    { chip: 'nvme-pci-2100', label: 'NVMe 0 composite', kind: 'temperature', value: 41, unit: '°C', max: 80, crit: 85 },
    { chip: 'ipmi', label: 'FAN1', kind: 'fan', value: 6200, unit: 'RPM' },
    { chip: 'ipmi', label: 'FAN2', kind: 'fan', value: 6150, unit: 'RPM' },
    { chip: 'ipmi', label: 'PSU 1 input', kind: 'power', value: 412, unit: 'W', max: 1200 },
    { chip: 'ipmi', label: 'PSU 2 input', kind: 'power', value: 398, unit: 'W', max: 1200 },
  ],
  bmc: { driver: 'redfish', url: 'https://10.20.0.111' },
}

export const gpus: GPU[] = [
  { address: '0000:41:00.0', vendorId: '10de', deviceId: '26b9', vendor: 'NVIDIA', name: 'NVIDIA L40S', driver: 'nvidia', telemetry: 'nvidia-smi', utilPercent: 87, memTotalMiB: 46068, memUsedMiB: 11912, tempC: 63, powerW: 268, powerLimitW: 350 },
]

export function topology(): Topology {
  const cpus = Array.from({ length: 64 }, (_, id) => {
    const core = id % 32
    return { id, node: core < 16 ? 0 : 1, socket: core < 16 ? 0 : 1, core: core % 16, online: true, siblings: [core, core + 32], loadPercent: Math.round(Math.random() * (id < 8 ? 60 : 25)) }
  })
  return {
    nodes: [
      { id: 0, cpus: cpus.filter((c) => c.node === 0).map((c) => c.id), memTotalKiB: 128 * 1024 * MiB, memFreeKiB: 70 * 1024 * MiB },
      { id: 1, cpus: cpus.filter((c) => c.node === 1).map((c) => c.id), memTotalKiB: 128 * 1024 * MiB, memFreeKiB: 101 * 1024 * MiB },
    ],
    cpus,
    machines: onHost(SELF.id)
      .filter((m) => running(m.state))
      .map((m, i) => ({
        uuid: m.uuid,
        name: m.name,
        numaNodes: m.hugepages ? '0' : undefined,
        vcpus: Array.from({ length: m.vcpus }, (_, k) => (m.hugepages ? { cpu: k, pinned: String(k), pinnedCpus: [k] } : { cpu: (8 + i * 7 + k * 3) % 64 })),
      })),
  }
}

export const interfaces: HostInterface[] = [
  { name: 'enp193s0f0np0', kind: 'physical', mac: '3c:ec:ef:10:20:01', mtu: 9000, state: 'up', speedMbps: 25000, driver: 'ice', master: 'bond0', rxBytes: 9.1e12, txBytes: 6.4e12 },
  { name: 'enp193s0f1np1', kind: 'physical', mac: '3c:ec:ef:10:20:02', mtu: 9000, state: 'up', speedMbps: 25000, driver: 'ice', master: 'bond0', rxBytes: 8.7e12, txBytes: 6.1e12 },
  { name: 'bond0', kind: 'bond', mac: '3c:ec:ef:10:20:01', mtu: 9000, state: 'up', members: ['enp193s0f0np0', 'enp193s0f1np1'], bondMode: '802.3ad', master: 'br0', rxBytes: 17.8e12, txBytes: 12.5e12 },
  { name: 'br0', kind: 'bridge', mac: '3c:ec:ef:10:20:01', mtu: 9000, state: 'up', members: ['bond0'], addresses: ['10.20.0.11/24', 'fe80::3eec:efff:fe10:2001/64'], rxBytes: 17.1e12, txBytes: 12.2e12 },
  { name: 'bond0.30', kind: 'vlan', mtu: 9000, state: 'up', vlanId: 30, parent: 'bond0', master: 'br-k8s', rxBytes: 1.2e12, txBytes: 0.9e12 },
  { name: 'virbr0', kind: 'bridge', mac: '52:54:00:aa:bb:01', mtu: 1500, state: 'up', addresses: ['192.168.122.1/24'], members: ['vnet0', 'vnet1', 'vnet2', 'vnet3', 'vnet4'], rxBytes: 4.3e11, txBytes: 9.9e11 },
  ...onHost(SELF.id)
    .filter((m) => running(m.state))
    .map((m, i): HostInterface => ({ name: 'vnet' + i, kind: 'tap', mac: 'fe' + m.mac.slice(2), mtu: 1500, state: 'up', master: 'virbr0', rxBytes: 1e10 * (i + 1), txBytes: 2e10 * (i + 1) })),
  { name: 'lo', kind: 'loopback', mtu: 65536, state: 'unknown', addresses: ['127.0.0.1/8', '::1/128'], rxBytes: 2e9, txBytes: 2e9 },
]

// ---- High availability, resource pools, reports ---------------------------------------------

export function haStatus(): HAStatus {
  const policies = [
    { uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03', name: 'db-01', priority: 'critical' as const, targetNode: HOSTS[1].id },
    { uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', name: 'web-01', priority: 'high' as const },
    { uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02', name: 'web-02', priority: 'high' as const, targetNode: HOSTS[2].id },
    { uuid: '7a2b3c4d-1e2f-4a3b-9c8d-1a2b3c4d5e01', name: 'k8s-cp-01', priority: 'critical' as const },
    { uuid: '9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e01', name: 'dns-01', priority: 'normal' as const },
  ].map((p) => ({ ...p, enabled: true, restartDelaySec: p.priority === 'critical' ? 0 : 15, maxRetries: 3 }))
  const vmPolicies = Object.fromEntries(policies.map((p) => [p.uuid, p]))
  const config = {
    version: 14,
    autoFailover: true,
    heartbeatIntervalSec: 2,
    failoverTimeoutSec: 30,
    witness: '10.20.0.5:8420',
    watchdog: true,
    watchdogTimeoutSec: 60,
    fenceAgents: Object.fromEntries(
      HOSTS.map((h, i) => [h.id, { nodeId: h.id, driver: i === 2 ? ('ipmi' as const) : ('redfish' as const), endpoint: `https://10.20.0.11${i + 1}`, username: 'fence', password: '********' }]),
    ),
    vmPolicies,
  }
  return {
    selfId: SELF.id,
    clustered: true,
    quorum: { quorate: true, votes: 4, totalVotes: 4, required: 3, witness: '10.20.0.5:8420', witnessReachable: true, coordinator: SELF.id },
    nodes: HOSTS.map((h) => ({
      id: h.id,
      name: h.name,
      self: h.self,
      status: 'online' as const,
      lastSeen: new Date().toISOString(),
      machines: onHost(h.id).filter((m) => running(m.state)).length,
      report: {
        nodeId: h.id, name: h.name, time: new Date().toISOString(), quorate: true, configVersion: 14, watchdogArmed: true, watchdogTimeoutSec: 60,
        memTotalKiB: h.info.memoryKiB, memFreeKiB: Math.round(h.info.memoryKiB * 0.45),
      },
    })),
    machines: policies.map((p) => {
      const m = byUUID(p.uuid)!
      const h = hostByID(m.host)!
      return { ...p, owner: h.id, ownerName: h.name, running: running(m.state), epoch: 3, updated: ago(2 * H), known: true }
    }),
    config,
    watchdogArmed: true,
    events: [
      { time: ago(26 * D), level: 'warn', node: 'hv-03', message: 'hv-03 missed 15 heartbeats; fencing through IPMI' },
      { time: ago(26 * D - 40_000), level: 'info', node: 'hv-03', message: 'hv-03 is powered off (IPMI chassis status: off)' },
      { time: ago(26 * D - 45_000), level: 'info', machine: 'dns-01', message: 'dns-01 restarted on hv-02' },
      { time: ago(26 * D - 3 * H), level: 'info', node: 'hv-03', message: 'hv-03 is back; dns-01 moved home with live migration' },
      { time: ago(2 * H), level: 'info', message: 'Configuration version 14 saved by onur' },
    ],
  }
}

export const respools: PoolView[] = [
  {
    id: 'rp-prod', name: 'Production', description: 'Customer-facing services', cpuLimit: 48, memoryLimitMiB: 196608, storageLimits: { default: 2e12 },
    machines: ['4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02', '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03'], createdAt: ago(90 * D),
    usage: { poolId: 'rp-prod', machines: 3, vcpus: 12, memoryMiB: 40960, storageBytes: { default: 0.62e12 } },
  },
  {
    id: 'rp-ml', name: 'ML team', parentId: undefined, description: 'GPU work, self-service for the ML group', cpuLimit: 32, memoryLimitMiB: 131072, storageLimits: { default: 1e12 },
    machines: ['4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e05'], createdAt: ago(40 * D),
    usage: { poolId: 'rp-ml', machines: 1, vcpus: 16, memoryMiB: 65536, storageBytes: { default: 0.3e12 } },
  },
  {
    id: 'rp-ci', name: 'CI runners', parentId: 'rp-ml', cpuLimit: 8, memoryLimitMiB: 16384, storageLimits: {},
    machines: ['4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06'], createdAt: ago(20 * D),
    usage: { poolId: 'rp-ci', machines: 1, vcpus: 4, memoryMiB: 8192, storageBytes: { default: 0.08e12 } },
  },
]

export const reports: ReportMeta[] = [
  { id: 'rep-1', kind: 'capacity', title: 'Capacity — cluster', format: 'html', generatedAt: ago(3 * H), sizeBytes: 48211, summary: '3 hosts · 14 machines · N+1: yes', by: 'schedule', scheduleId: 'rs-1' },
  { id: 'rep-2', kind: 'backup_sla', title: 'Backup compliance', format: 'html', generatedAt: ago(2 * D), sizeBytes: 22190, summary: '11 of 14 machines backed up in the last 24 h', by: 'onur' },
  { id: 'rep-3', kind: 'inventory', title: 'Machine inventory', format: 'csv', generatedAt: ago(9 * D), sizeBytes: 3120, summary: '14 machines', by: 'onur' },
]

export const reportSchedules: ReportSchedule[] = [
  { id: 'rs-1', name: 'Weekly capacity', kind: 'capacity', format: 'html', enabled: true, every: 'weekly', at: '07:00', keepDays: 90, notify: true, lastRun: ago(3 * H), nextRun: new Date(now() + 7 * D - 3 * H).toISOString() },
]
