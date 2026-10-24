// The demo's pretend cluster: three hosts with a handful of machines each, storage, networks,
// backups, alerts and history. Everything lives in memory; a reload starts over.
// Only used by the demo build (VITE_DEMO=1); never part of the binary's UI.

import type {
  ActivityEntry,
  Alert,
  AlertConfig,
  AuditVerification,
  BackupInfo,
  Blueprint,
  CatalogLease,
  CatalogRequest,
  ComplianceCheck,
  ConfigVersion,
  ContentHostConfig,
  ContentItem,
  ContentVersion,
  Device,
  Domain,
  DomainDetail,
  DomainState,
  FIPSInfo,
  HostCompliance,
  HostHardware,
  HostInfo,
  Lease,
  LCMPlan,
  LCMStep,
  LCMStepKind,
  Pool,
  PricingConfig,
  Project,
  ScheduleInfo,
  SettingsView,
  SnapshotInfo,
  VNetwork,
  Volume,
} from '../api'
import type { FloatingIP, IPsecConfig, LoadBalancer, SDNPort, SDNStatus, SecurityGroup, Subnet, VPCDetail } from '../sdn'
import type { CephHost, ClusterConfig, DiskInfo, RBDPool, StoragePolicy, Telemetry } from '../ceph'
import type { NVMeSubsystem, NVMeTarget, Tier, ZFSEfficiency } from '../storageeff'
import type { BGPView, DNSConfig, IPPool, SRIOVDevice } from '../physnet'
import type { Chassis, ClusterGPU, Firewall, GPU, GPUPool, GPUPoolView, GPUResource, HAStatus, HostInterface, PoolView, ReportMeta, ReportSchedule, Topology } from '../enterprise'

export interface DemoHost {
  id: string
  name: string
  self: boolean
  info: HostInfo
  /** Idle load of the host itself, on top of its machines (percent). */
  baseCpu: number
  labels?: Record<string, string>
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
  /** The project this machine belongs to (ADR-0060); empty = global. */
  project?: string
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
    labels: { rack: 'r1', zone: 'zone-a' },
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
    labels: { rack: 'r1', zone: 'zone-a' },
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
    labels: { rack: 'r2', zone: 'zone-b' },
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

// ---- Projects (ADR-0060) ----------------------------------------------------------------------

export const projects: Project[] = [
  {
    id: 'prod',
    name: 'Production',
    description: 'Customer-facing services',
    members: [
      { kind: 'user', name: 'onur', role: 'admin' },
      { kind: 'user', name: 'demo', role: 'admin' },
      { kind: 'group', name: 'ops', role: 'operator' },
      { kind: 'group', name: 'devs', role: 'viewer' },
    ],
    limits: { machines: 10, vcpus: 48, memoryMiB: 131072, diskGiB: 2000 },
    storagePools: ['default', 'nas-nfs'],
    networks: ['default', 'lan-bridge'],
    created: ago(120 * D),
  },
  {
    id: 'ml',
    name: 'ML team',
    description: 'GPU workloads',
    members: [
      { kind: 'group', name: 'ml-team', role: 'admin' },
      { kind: 'user', name: 'demo', role: 'admin' },
      { kind: 'user', name: 'deniz', role: 'operator' },
    ],
    limits: { machines: 4, vcpus: 32, memoryMiB: 65536, diskGiB: 500 },
    storagePools: ['fast-zfs'],
    networks: ['default'],
    created: ago(40 * D),
  },
]

export const machines: DemoMachine[] = [
  // hv-01 / hv-02
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', host: HOSTS[1].id, name: 'web-01', title: 'Public web (nginx)',
    description: 'Front end for the company site. Behind the load balancer with web-02.',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 4096, diskGiB: 40, autostart: true, profile: 'web', project: 'prod',
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
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02', host: HOSTS[1].id, name: 'web-02', title: 'Public web (nginx)',
    os: 'debian', state: 'running', vcpus: 2, memoryMiB: 4096, diskGiB: 40, autostart: true, profile: 'web', project: 'prod',
    mac: '52:54:00:3a:11:02', ip: '192.168.122.12',
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03', host: SELF.id, name: 'db-01', title: 'PostgreSQL 17 primary',
    description: 'Main database. Huge pages, pinned to NUMA node 0.',
    os: 'rocky', state: 'running', vcpus: 8, memoryMiB: 32768, diskGiB: 500, autostart: true, profile: 'db', hugepages: true, project: 'prod',
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
    os: 'ubuntu', state: 'running', vcpus: 16, memoryMiB: 65536, diskGiB: 300, profile: 'gpu', gpu: true, owner: 'deniz', project: 'ml',
    mac: '52:54:00:3a:11:05', ip: '192.168.122.50', memUsedFrac: 0.7,
  }),
  machine({
    uuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06', host: SELF.id, name: 'ci-runner-03',
    os: 'debian', state: 'shutoff', vcpus: 4, memoryMiB: 8192, diskGiB: 80, profile: 'build', owner: 'deniz', project: 'ml',
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
    project: m.project,
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
    project: m.project,
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
    cpuWeight: 0,
    cpuLimit: 0,
    nested: '',
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
  { name: 'ceph-vms', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000005', type: 'rbd', source: 'rbd-replicated', active: true, autostart: true, capacityBytes: 5.18e12, allocationBytes: 1.42e12, availableBytes: 3.76e12, volumes: 7 },
  { name: 'ceph-cold', uuid: '1e0c1c9a-3d43-4c6b-9f2a-000000000006', type: 'rbd', source: 'rbd-cold-ec', active: true, autostart: true, capacityBytes: 8.13e12, allocationBytes: 0.61e12, availableBytes: 7.52e12, volumes: 2 },
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
  { address: '0000:41:00.0', vendorId: '10de', deviceId: '26b9', vendor: 'NVIDIA', name: 'NVIDIA L40S', driver: 'nvidia', telemetry: 'nvidia-smi', utilPercent: 87, memTotalMiB: 46068, memUsedMiB: 11912, tempC: 63, powerW: 268, powerLimitW: 350,
    profile: 'mdev', passThroughReady: true, mdevTypes: [{ id: 'nvidia-1155', name: 'NVIDIA L40S-12Q', api: 'vfio-pci', available: 2 }], usedBy: [] },
]

/** The other members' GPUs (ADR-0071): two whole L40S cards on hv-02, an A100 in MIG mode on hv-03. */
export const memberGPUs: Record<string, GPU[]> = {
  [HOSTS[1].id]: [
    { address: '0000:41:00.0', vendorId: '10de', deviceId: '26b9', vendor: 'NVIDIA', name: 'NVIDIA L40S', driver: 'vfio-pci', profile: 'whole', passThroughReady: true, usedBy: ['render-01'] },
    { address: '0000:42:00.0', vendorId: '10de', deviceId: '26b9', vendor: 'NVIDIA', name: 'NVIDIA L40S', driver: 'vfio-pci', profile: 'whole', passThroughReady: true, usedBy: [] },
  ],
  [HOSTS[2].id]: [
    { address: '0000:c1:00.0', vendorId: '10de', deviceId: '20b5', vendor: 'NVIDIA', name: 'NVIDIA A100 80GB PCIe', driver: 'nvidia', telemetry: 'nvidia-smi', mig: 'enabled', profile: 'mig', passThroughReady: true,
      migProfiles: [{ id: '1g.10gb', name: 'MIG 1g.10gb', giProfileId: 19, memoryMiB: 9728, count: 7, free: 4 }, { id: '3g.40gb', name: 'MIG 3g.40gb', giProfileId: 9, memoryMiB: 40192, count: 2, free: 1 }] },
  ],
}

export const gpuPools: GPUPool[] = [
  { id: 'l40s', name: 'L40S render', model: 'L40S', kind: 'whole', description: 'whole cards for rendering', created: ago(20 * D) },
  { id: 'l40s-12q', name: 'L40S 12 GB vGPU', model: 'L40S', kind: 'mdev', profile: 'nvidia-1155', perHost: 2, created: ago(20 * D) },
  { id: 'a100-1g', name: 'A100 1g.10gb', model: 'A100', kind: 'mig', profile: '1g.10gb', created: ago(6 * D) },
]

/** Every member's GPUs, with the pools each serves. */
export function clusterGPUs(): ClusterGPU[] {
  const all = HOSTS.flatMap((h) => (h.self ? gpus : (memberGPUs[h.id] ?? [])).map((g) => ({ ...g, hostId: h.id, hostName: h.name })))
  return all.map((g) => ({
    ...g,
    pools: gpuPools.filter((p) => (g.name ?? '').toLowerCase().includes(p.model.toLowerCase()) && (p.kind === 'whole' ? g.profile === 'whole' : p.kind === 'mdev' ? g.mdevTypes?.some((m) => m.id === p.profile) : g.migProfiles?.some((m) => m.id === p.profile))).map((p) => p.id),
  }))
}

/** A pool with what each member has of it. */
export function gpuPoolView(p: GPUPool): GPUPoolView {
  const hosts: GPUResource[] = []
  for (const h of HOSTS) {
    let total = 0
    let used = 0
    for (const g of clusterGPUs().filter((x) => x.hostId === h.id && x.pools.includes(p.id))) {
      if (p.kind === 'whole') {
        total++
        if (g.usedBy?.length) used++
      } else if (p.kind === 'mdev') {
        const m = g.mdevTypes?.find((x) => x.id === p.profile)
        total += (m?.available ?? 0) + 1 // gpu-train-01 holds one slice
        used += 1
      } else {
        const m = g.migProfiles?.find((x) => x.id === p.profile)
        total += m?.count ?? 0
        used += (m?.count ?? 0) - (m?.free ?? 0)
      }
    }
    if (total) hosts.push({ hostId: h.id, hostName: h.name, poolId: p.id, model: p.model, kind: p.kind, total, used, free: total - used })
  }
  return { ...p, hosts, total: hosts.reduce((a, h) => a + h.total, 0), used: hosts.reduce((a, h) => a + h.used, 0), free: hosts.reduce((a, h) => a + h.free, 0) }
}

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

// ---- Rolling cluster upgrades (ADR-0070) -----------------------------------------------------

/** The version each member runs; hv-02 is one release behind, so the cluster shows mixed. */
export const hostVersions: Record<string, string> = Object.fromEntries(HOSTS.map((h, i) => [h.id, i === 1 ? '0.5.9-demo' : '0.6.0-demo']))

const LCM_STEPS: LCMStepKind[] = ['precheck', 'enter_maintenance', 'evacuate', 'update_virtomir', 'wait_rejoin', 'exit_maintenance']
const LCM_MSG: Partial<Record<LCMStepKind, string>> = {
  precheck: 'all checks passed',
  enter_maintenance: 'no new machines are placed here',
  evacuate: '3 machines moved away',
  update_virtomir: 'update installed',
  wait_rejoin: 'back with 0.6.0-demo',
  exit_maintenance: 'machines may be placed here again',
}

/** The steps of a plan that started at start, one every few seconds (`upTo` limits them). */
export function lcmSteps(order: string[], start: number, every: number, upTo = Infinity): LCMStep[] {
  const out: LCMStep[] = []
  let at = start
  for (const id of order) {
    const h = HOSTS.find((x) => x.id === id)!
    for (const kind of LCM_STEPS) {
      at += every
      if (at > upTo) {
        out.push({ at: new Date(at - every).toISOString(), kind, host: h.name, hostId: id, message: '', state: 'running' })
        return out
      }
      out.push({ at: new Date(at).toISOString(), kind, host: h.name, hostId: id, message: LCM_MSG[kind] ?? '', state: 'done' })
    }
  }
  return out
}

const lcmOrder = [...HOSTS.filter((h) => !h.self), SELF].map((h) => h.id)
export const lcmPlans: LCMPlan[] = [
  {
    id: 'lcm-0001',
    started: ago(12 * D),
    finished: ago(12 * D - 25 * 60e3),
    state: 'succeeded',
    hostOrder: lcmOrder,
    targetVersion: '0.5.9-demo',
    osUpdates: true,
    reboot: true,
    moveBack: true,
    startedBy: 'onur',
    maintenance: [],
    steps: lcmSteps(lcmOrder, Date.parse(ago(12 * D)), 80e3),
  },
]

// ---- Catalog (ADR-0065) ----------------------------------------------------------------------

export let pricing: PricingConfig = { vcpuHour: 0.04, ramGiBHour: 0.01, diskGiBMonth: 0.08, currency: '$' }

/** Namespace-import properties are not assignable from routes.ts, so updates go through here. */
export function setPricing(p: PricingConfig) {
  pricing = p
}

export const catalogBlueprints: Blueprint[] = [
  {
    id: 'web-server-ubuntu',
    name: 'Web Server (Ubuntu 24.04)',
    description: 'Hardened nginx front end from the maintained template. Cloud-init asks for your SSH key and root password.',
    icon: 'globe',
    sourceType: 'template',
    sourceRef: 'tmpl-ubuntu-24.04',
    targetPool: 'default',
    presets: [
      { id: 'small', name: 'Small', vcpus: 2, memoryMiB: 2048, diskGiB: 20 },
      { id: 'medium', name: 'Medium', vcpus: 4, memoryMiB: 8192, diskGiB: 60 },
      { id: 'large', name: 'Large', vcpus: 8, memoryMiB: 16384, diskGiB: 120 },
    ],
    defaultPresetId: 'medium',
    allowedNetworks: ['default', 'lan-bridge'],
    cloudInitFields: [
      { key: 'sshKey', label: 'SSH public key', type: 'ssh_key', required: true, default: '' },
      { key: 'rootPassword', label: 'root password', type: 'password', required: false, default: '' },
      { key: 'envName', label: 'Environment tag', type: 'select', required: false, default: 'staging', options: ['staging', 'production'] },
    ],
    allowedProjects: [],
    requireApproval: false,
    defaultLeaseDays: 30,
    maxLeaseDays: 90,
    created: ago(30 * D),
    updated: ago(6 * D),
  },
  {
    id: 'postgres-16-appliance',
    name: 'PostgreSQL 16 Appliance',
    description: 'Pre-tuned PostgreSQL 16 on its own network segment. Orders wait for a project admin because the appliance is expensive.',
    icon: 'database',
    sourceType: 'image',
    sourceRef: 'https://dl.cloudsmith.io/acme/postgres-16/latest.zfs.gz',
    targetPool: 'fast-zfs',
    presets: [
      { id: 'db-small', name: 'db-small', vcpus: 4, memoryMiB: 16384, diskGiB: 150 },
      { id: 'db-large', name: 'db-large', vcpus: 8, memoryMiB: 32768, diskGiB: 300 },
    ],
    defaultPresetId: 'db-small',
    allowedNetworks: ['default'],
    cloudInitFields: [
      { key: 'dbName', label: 'Database name', type: 'string', required: true, default: 'app' },
      { key: 'adminPassword', label: 'postgres password', type: 'password', required: true, default: '' },
    ],
    allowedProjects: ['prod'],
    requireApproval: true,
    defaultLeaseDays: 14,
    maxLeaseDays: 30,
    created: ago(25 * D),
    updated: ago(25 * D),
  },
  {
    id: 'dev-sandbox',
    name: 'Dev Sandbox',
    description: 'Throwaway Ubuntu box for experiments; both networks allowed, 14-day maximum lease.',
    icon: 'terminal',
    sourceType: 'template',
    sourceRef: 'tmpl-ubuntu-24.04',
    targetPool: '',
    presets: [
      { id: 'dev', name: 'Dev', vcpus: 2, memoryMiB: 4096, diskGiB: 40 },
    ],
    defaultPresetId: 'dev',
    allowedNetworks: ['default'],
    cloudInitFields: [
      { key: 'sshKey', label: 'SSH public key', type: 'ssh_key', required: true, default: '' },
    ],
    allowedProjects: ['prod', 'ml'],
    requireApproval: false,
    defaultLeaseDays: 7,
    maxLeaseDays: 14,
    created: ago(10 * D),
    updated: ago(10 * D),
  },
]

/** One active order (web-02 came from the catalog), one waiting for approval, one rejected. */
export const catalogRequests: CatalogRequest[] = [
  {
    id: 'req-web-11',
    blueprintId: 'web-server-ubuntu',
    projectId: 'prod',
    requester: 'deniz',
    vmName: 'web-11',
    presetId: 'medium',
    vcpus: 4,
    memoryMiB: 8192,
    diskGiB: 60,
    network: 'default',
    fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nza… deniz@laptop' },
    leaseDays: 30,
    status: 'active',
    createdAt: ago(12 * D),
    reviewedAt: ago(12 * D),
    reviewedBy: '',
    machineUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02',
  },
  {
    id: 'req-pg-report',
    blueprintId: 'postgres-16-appliance',
    projectId: 'prod',
    requester: 'deniz',
    vmName: 'pg-report-01',
    presetId: 'db-small',
    vcpus: 4,
    memoryMiB: 16384,
    diskGiB: 150,
    network: 'default',
    fieldValues: { dbName: 'reporting' },
    leaseDays: 14,
    status: 'pending_approval',
    createdAt: ago(2 * H),
  },
  {
    id: 'req-sandbox-x',
    blueprintId: 'dev-sandbox',
    projectId: 'prod',
    requester: 'deniz',
    vmName: 'sandbox-x',
    presetId: 'dev',
    vcpus: 2,
    memoryMiB: 4096,
    diskGiB: 40,
    network: 'default',
    leaseDays: 7,
    status: 'rejected',
    createdAt: ago(5 * D),
    reviewedAt: ago(4 * D),
    reviewedBy: 'onur',
    reviewComment: 'Use the CI runner instead — sandboxes keep the whole host busy during the load test.',
  },
]

/** web-11's lease: 30 days from when it was ordered, 18 still ahead. */
export const catalogLeases: CatalogLease[] = [
  {
    requestId: 'req-web-11',
    machineUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02',
    projectId: 'prod',
    expiry: new Date(now() + 18 * D).toISOString(),
    state: 'active',
    reminderSent: false,
    lastStateChange: ago(12 * D),
  },
]

// ---- Compliance, FIPS and audit verification (ADR-0064) -------------------------------------

const BENCH = 'CIS Linux 2.0 / STIG'

type CheckInput = Pick<ComplianceCheck, 'id' | 'title' | 'category' | 'severity'> & {
  status: ComplianceCheck['status']
  detail: string
  remediation: string
}

const chk = (c: CheckInput): ComplianceCheck => ({ benchmark: BENCH, ...c })

const baseChecks: CheckInput[] = [
  {
    id: 'cis-5.2.5-ssh-root-login', title: 'SSH Root Login', category: 'SSH', severity: 'high', status: 'pass',
    detail: 'PermitRootLogin is configured to "prohibit-password"',
    remediation: 'Set PermitRootLogin prohibit-password in /etc/ssh/sshd_config and reload sshd via systemctl reload ssh.',
  },
  {
    id: 'cis-5.2.6-ssh-empty-passwords', title: 'SSH Empty Passwords', category: 'SSH', severity: 'high', status: 'pass',
    detail: "PermitEmptyPasswords is set to 'no'",
    remediation: "Set PermitEmptyPasswords no in /etc/ssh/sshd_config and reload sshd via systemctl reload ssh.",
  },
  {
    id: 'cis-5.1.1-svirt-isolation', title: 'sVirt MAC Isolation (AppArmor / SELinux)', category: 'Virtualization', severity: 'high', status: 'pass',
    detail: 'sVirt MAC isolation active: AppArmor configured in qemu.conf and enabled in kernel',
    remediation: "Set security_driver apparmor in /etc/libvirt/qemu.conf and restart libvirtd via systemctl restart libvirtd.",
  },
  {
    id: 'cis-3.4.1-host-firewall', title: 'Host Firewall', category: 'Firewall', severity: 'medium', status: 'pass',
    detail: 'Firewall daemon firewalld is active',
    remediation: 'Enable a host firewall: `systemctl enable --now nftables`, firewalld or ufw, and allow SSH and libvirt traffic.',
  },
  {
    id: 'cis-9.1.1-unattended-updates', title: 'Unattended Security Updates', category: 'System', severity: 'medium', status: 'pass',
    detail: 'Automatic security updates enabled via unattended-upgrades',
    remediation: 'Install unattended-upgrades and enable security updates: `apt install unattended-upgrades`.',
  },
  {
    id: 'cis-2.1.1-time-sync', title: 'Network Time Synchronization', category: 'System', severity: 'medium', status: 'pass',
    detail: 'Time synchronization service unit enabled: systemd-timesyncd',
    remediation: 'Enable a time synchronization service: `systemctl enable --now systemd-timesyncd` (or chrony/ntp).',
  },
  {
    id: 'cis-1.2.1-kptr-restrict', title: 'Kernel Pointer Restriction (kptr_restrict)', category: 'Kernel', severity: 'low', status: 'pass',
    detail: 'kernel.kptr_restrict is 2 (>= 1)',
    remediation: 'Add kernel.kptr_restrict=2 to /etc/sysctl.d/99-hardening.conf and run sysctl --system.',
  },
  {
    id: 'cis-1.2.2-dmesg-restrict', title: 'Kernel dmesg Restriction (dmesg_restrict)', category: 'Kernel', severity: 'low', status: 'pass',
    detail: 'kernel.dmesg_restrict is 1',
    remediation: 'Add kernel.dmesg_restrict=1 to /etc/sysctl.d/99-hardening.conf and run sysctl --system.',
  },
  {
    id: 'cis-1.2.3-rp-filter', title: 'Reverse Path Filtering (rp_filter)', category: 'Kernel', severity: 'low', status: 'pass',
    detail: 'net.ipv4.conf.all.rp_filter is 1',
    remediation: 'Add net.ipv4.conf.all.rp_filter=1 and net.ipv4.conf.default.rp_filter=1 to /etc/sysctl.d/99-hardening.conf and run sysctl --system.',
  },
  {
    id: 'cis-9.2.1-legacy-services', title: 'Legacy Insecure Services', category: 'System', severity: 'high', status: 'pass',
    detail: 'No legacy insecure services (telnet, rsh, rlogin, tftp, vsftpd) detected',
    remediation: 'Remove legacy services: `apt purge telnetd rsh-server rsh-client tftpd vsftpd` (or the DNF equivalents).',
  },
  {
    id: 'cis-5.1.2-core-dumps', title: 'Core Dumps Restricted', category: 'System', severity: 'medium', status: 'pass',
    detail: "Core dumps restricted: fs.suid_dumpable=0 and '* hard core 0' configured in limits",
    remediation: "Add fs.suid_dumpable=0 to /etc/sysctl.d/99-hardening.conf (sysctl --system) and '* hard core 0' to /etc/security/limits.conf.",
  },
]

const withStatus = (id: string, status: ComplianceCheck['status'], detail: string, remediation?: string): (c: CheckInput) => CheckInput =>
  (c) => (c.id === id ? { ...c, status, detail, remediation: remediation ?? c.remediation } : c)

/** Per host: hv-01 is hardened, hv-02 slips a bit, hv-03 must be fixed. */
export function compliance(hostID: string, refresh = false): HostCompliance {
  let cs = baseChecks
  if (hostID === HOSTS[1].id)
    cs = baseChecks
      .map(withStatus('cis-3.4.1-host-firewall', 'fail', 'No active firewall rules or active firewall daemon (nftables, firewalld, ufw) detected', 'Enable a host firewall: `systemctl enable --now firewalld` and allow SSH and libvirt traffic.'))
      .map(withStatus('cis-1.2.3-rp-filter', 'fail', 'net.ipv4.conf.all.rp_filter is 0 (expected 1)', 'Add net.ipv4.conf.all.rp_filter=1 to /etc/sysctl.d/99-hardening.conf and run sysctl --system.'))
      .map(withStatus('cis-9.1.1-unattended-updates', 'warn', 'Automatic updates enabled in /etc/apt/apt.conf.d/50unattended-upgrades but security-only: apply_updates is off'))
  if (hostID === HOSTS[2].id)
    cs = baseChecks
      .map(withStatus('cis-5.2.5-ssh-root-login', 'fail', 'PermitRootLogin directive is missing in sshd configuration'))
      .map(withStatus('cis-3.4.1-host-firewall', 'fail', 'No active firewall rules or active firewall daemon (nftables, firewalld, ufw) detected', 'Enable a host firewall: `systemctl enable --now nftables`, firewalld or ufw, and allow SSH and libvirt traffic.'))
      .map(withStatus('cis-9.1.1-unattended-updates', 'fail', 'Automatic security updates are not configured in APT or DNF', 'Install unattended-upgrades and enable security updates: `apt install unattended-upgrades`.'))
      .map(withStatus('cis-5.1.2-core-dumps', 'fail', 'Core dumps are allowed: fs.suid_dumpable=2 and no \'* hard core 0\' limit'))
  const passed = cs.filter((c) => c.status === 'pass').length
  const failed = cs.filter((c) => c.status === 'fail').length
  const warned = cs.filter((c) => c.status === 'warn').length
  return {
    hostId: hostID,
    hostName: hostByID(hostID)?.name ?? hostID,
    evaluatedAt: refresh ? new Date().toISOString() : ago(25 * 60_000),
    score: Math.round((passed * 100) / cs.length),
    passedCount: passed,
    failedCount: failed,
    warningCount: warned,
    totalCount: cs.length,
    results: cs.map(chk),
  }
}

export const demoFIPS: FIPSInfo = {
  kernelEnabled: true,
  enforced: true,
  tlsMinVersion: 'TLS 1.2',
  cipherSuites: [
    'TLS_AES_128_GCM_SHA256',
    'TLS_AES_256_GCM_SHA384',
    'TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256',
    'TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384',
    'TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256',
    'TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384',
  ],
  curves: ['P-256', 'P-384', 'P-521'],
  description: 'FIPS mode enabled: restricting TLS to FIPS 140 approved cipher suites (experimental, not tested on hardware)',
}

/** hv-03's audit log was edited by hand; its chain check fails there. */
export const demoAuditVerification: Record<string, AuditVerification> = {
  [HOSTS[0].id]: {
    valid: true,
    verifiedCount: 246,
    firstSeq: 1,
    lastSeq: 246,
    headHash: '9f2b6b4c5f0a1d3e7c8b9a2f4d6e8a0c1b3d5f7e9a0b2c4d6e8f0a1c3b5d7e9f',
  },
  [HOSTS[1].id]: {
    valid: true,
    verifiedCount: 198,
    firstSeq: 1,
    lastSeq: 198,
    headHash: '3d5f7e9a0b2c4d6e8f0a1c3b5d7e9f1b2a4c6e8d0a2b4c6e8f0a2c4e6d8b0a2c4',
  },
  [HOSTS[2].id]: {
    valid: false,
    verifiedCount: 120,
    firstSeq: 1,
    lastSeq: 246,
    headHash: '7e9a0b2c4d6e8f0a1c3b5d7e9f1b2a4c6e8d0a2b4c6e8f0a2c4e6d8b0a2c4e6d',
    tamperedSeq: 121,
    tamperedIndex: 41,
    reason: 'entry 121 hash does not follow from the previous entry (expected SHA-256 de4f01…, found 1a2b33…)',
  },
}
// Content Library (ADR-0063)
export let contentConfig: ContentHostConfig = {
  hostId: HOSTS[0].id,
  targetPool: 'default',
  autoSync: true,
}

export let contentItems: ContentItem[] = [
  {
    id: 'ci-ubuntu-2404',
    name: 'Ubuntu 24.04 LTS Cloud Image',
    type: 'image',
    description: 'Official Noble Numbat cloud-init ready qcow2 image with VirtIO drivers',
    sourceHost: HOSTS[0].id,
    keepVersions: 3,
    syncPolicy: { mode: 'all' },
    created: ago(30 * D),
    updated: ago(2 * D),
    versions: [
      {
        id: 'ver-u2404-10',
        version: '24.04.0',
        description: 'Initial Noble Numbat cloud base image',
        sha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
        size: 654311424,
        format: 'qcow2',
        sourceHost: HOSTS[0].id,
        sourcePool: 'default',
        sourceVol: 'ubuntu-24.04-base.qcow2',
        created: ago(30 * D),
      },
      {
        id: 'ver-u2404-11',
        version: '24.04.1',
        description: 'Point release 2024.1 with updated kernel and security patches',
        sha256: 'a93b4f5263d893f41295db283bf3e7b1652f19069bfa780d6fce7c01bbf53b92',
        size: 681574400,
        format: 'qcow2',
        sourceHost: HOSTS[0].id,
        sourcePool: 'default',
        sourceVol: 'ubuntu-noble-cloudimg.qcow2',
        created: ago(2 * D),
      },
    ],
  },
  {
    id: 'ci-debian-13-netinst',
    name: 'Debian 13 Minimal Netinst ISO',
    type: 'iso',
    description: 'Debian 13 (Trixie) official minimal network installer ISO',
    sourceHost: HOSTS[0].id,
    keepVersions: 3,
    syncPolicy: { mode: 'selected', hosts: [HOSTS[0].id, HOSTS[1].id] },
    created: ago(60 * D),
    updated: ago(10 * D),
    versions: [
      {
        id: 'ver-deb13-10',
        version: '13.0',
        description: 'Debian 13.0 netinst x86_64 bootable installer',
        sha256: '3a42c98d672be119435b89a812304918efbc75a3089d54e5b2257d018693bb47',
        size: 419430400,
        format: 'iso',
        sourceHost: HOSTS[0].id,
        sourcePool: 'default',
        sourceVol: 'debian-13-netinst.iso',
        created: ago(10 * D),
      },
    ],
  },
  {
    id: 'ci-k8s-worker-tpl',
    name: 'Kubernetes Worker Node Template',
    type: 'template',
    description: 'Pre-configured Kubernetes v1.31 worker node with containerd and Cilium CNI',
    sourceHost: HOSTS[1].id,
    keepVersions: 3,
    syncPolicy: { mode: 'all' },
    created: ago(15 * D),
    updated: ago(1 * D),
    versions: [
      {
        id: 'ver-k8s-12',
        version: '1.31.2',
        description: 'Updated containerd to 1.7.20 and Cilium to 1.16.1',
        sha256: 'f4b88235e16f3d99432f81156828a2a7d451270274092b34a9b6c08d1326c9aa',
        size: 8589934592,
        format: 'qcow2',
        sourceHost: HOSTS[1].id,
        sourcePool: 'default',
        sourceVol: 'k8s-node-v131.qcow2',
        templateXml: `<domain type='kvm'><name>k8s-worker-template</name><memory unit='KiB'>4194304</memory><vcpu placement='static'>4</vcpu><os><type arch='x86_64' machine='q35'>hvm</type></os></domain>`,
        created: ago(1 * D),
      },
    ],
  },
]


// ---- SDN on OVN (ADR-0072) ----------------------------------------------------------------------

export const sdnVPCs: VPCDetail[] = [
  {
    id: 'vpc-prod', name: 'Production VPC', description: 'customer-facing tiers', projectId: 'prod', ipv4Cidr: '10.0.0.0/16', defaultDeny: true,
    created: ago(30 * D), updated: ago(2 * D),
    router: { id: 'rtr-vpc-prod', vpcId: 'vpc-prod', name: 'Production VPC router', logicalRouter: 'lr-vpc-prod', routes: [{ destinationCidr: '192.168.50.0/24', nextHopIp: '10.0.1.254' }] },
    gateway: { vpcId: 'vpc-prod', externalNetwork: 'physnet1', gatewayIp: '203.0.113.1', externalIp: '203.0.113.10/24', enabled: true, gatewayChassis: 'hv-01' },
  },
  {
    id: 'vpc-staging', name: 'Staging VPC', projectId: 'prod', ipv4Cidr: '10.10.0.0/16', defaultDeny: false, created: ago(12 * D), updated: ago(12 * D),
    router: { id: 'rtr-vpc-staging', vpcId: 'vpc-staging', name: 'Staging VPC router', logicalRouter: 'lr-vpc-staging', routes: [] },
  },
]

export const sdnSubnets: Subnet[] = [
  { id: 'prod-web', vpcId: 'vpc-prod', name: 'web', cidr: '10.0.1.0/24', gatewayIp: '10.0.1.1', dhcpEnabled: true, allocationPoolStart: '10.0.1.10', allocationPoolEnd: '10.0.1.200', dnsServers: ['1.1.1.1', '9.9.9.9'], logicalSwitch: 'ls-prod-web', ovnDhcpId: '8a7b6c5d-0000-4000-8000-000000000001' },
  { id: 'prod-db', vpcId: 'vpc-prod', name: 'database', cidr: '10.0.2.0/24', gatewayIp: '10.0.2.1', dhcpEnabled: true, logicalSwitch: 'ls-prod-db', ovnDhcpId: '8a7b6c5d-0000-4000-8000-000000000002' },
  { id: 'staging-app', vpcId: 'vpc-staging', name: 'app', cidr: '10.10.1.0/24', gatewayIp: '10.10.1.1', dhcpEnabled: true, logicalSwitch: 'ls-staging-app' },
]

export const sdnPorts: SDNPort[] = [
  { id: 'vm-port-4f1c2a7e-525400a10101', subnetId: 'prod-web', vmUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', mac: '52:54:00:a1:01:01', ip: '10.0.1.11', securityGroups: ['5eb0c1a2d3e4f501'] },
  { id: 'vm-port-4f1c2a7e-525400a10201', subnetId: 'prod-web', vmUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02', mac: '52:54:00:a1:02:01', ip: '10.0.1.12', securityGroups: ['5eb0c1a2d3e4f501'] },
  { id: 'vm-port-4f1c2a7e-525400a10301', subnetId: 'prod-db', vmUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03', mac: '52:54:00:a1:03:01', ip: '10.0.2.21', securityGroups: ['5eb0c1a2d3e4f502'] },
]

export const sdnGroups: SecurityGroup[] = [
  {
    id: '5eb0c1a2d3e4f501', projectId: 'prod', vpcId: 'vpc-prod', name: 'sg-web', description: 'public web servers', defaultDeny: true,
    memberPorts: ['vm-port-4f1c2a7e-525400a10101', 'vm-port-4f1c2a7e-525400a10201'], created: ago(30 * D), updated: ago(2 * D),
    rules: [
      { id: 'r1', direction: 'egress', action: 'allow-related', priority: 100, protocol: 'any', description: 'machines may connect out' },
      { id: 'r2', direction: 'ingress', action: 'allow-related', priority: 1000, protocol: 'tcp', portMin: 80, remoteCidr: '0.0.0.0/0', description: 'HTTP' },
      { id: 'r3', direction: 'ingress', action: 'allow-related', priority: 1000, protocol: 'tcp', portMin: 443, remoteCidr: '0.0.0.0/0', description: 'HTTPS' },
    ],
  },
  {
    id: '5eb0c1a2d3e4f502', projectId: 'prod', vpcId: 'vpc-prod', name: 'sg-db', description: 'PostgreSQL, from the web tier only', defaultDeny: true,
    memberPorts: ['vm-port-4f1c2a7e-525400a10301'], created: ago(30 * D), updated: ago(30 * D),
    rules: [
      { id: 'r4', direction: 'egress', action: 'allow-related', priority: 100, protocol: 'any' },
      { id: 'r5', direction: 'ingress', action: 'allow-related', priority: 1000, protocol: 'tcp', portMin: 5432, remoteGroupId: '5eb0c1a2d3e4f501', description: 'PostgreSQL from sg-web' },
      { id: 'r6', direction: 'ingress', action: 'drop', priority: 900, protocol: 'icmp', remoteCidr: '0.0.0.0/0' },
    ],
  },
]

export const sdnFloatingIPs: FloatingIP[] = [
  { id: 'f10a1b2c3d4e5f60', projectId: 'prod', vpcId: 'vpc-prod', externalIp: '203.0.113.21', internalIp: '10.0.1.11', portId: 'vm-port-4f1c2a7e-525400a10101', description: 'web-01', created: ago(20 * D) },
  { id: 'f10a1b2c3d4e5f61', projectId: 'prod', vpcId: 'vpc-prod', externalIp: '203.0.113.22', created: ago(3 * D) },
]

export const sdnLBs: LoadBalancer[] = [
  {
    id: 'b1a2c3d4e5f60718', projectId: 'prod', vpcId: 'vpc-prod', name: 'web-front', description: 'nginx pool', vip: '10.0.1.100', port: 80, protocol: 'tcp',
    members: ['10.0.1.11:80', '10.0.1.12:80'], attachedSwitches: ['ls-prod-web'], attachedRouters: ['lr-vpc-prod'], status: 'active',
    healthCheck: { protocol: 'tcp', port: 80, intervalSeconds: 5, timeoutSeconds: 2, fall: 3, rise: 2 },
  },
]

export const sdnIPsec: IPsecConfig = { enabled: false, mode: 'cert', status: 'disabled' }

export const sdnStatus: SDNStatus = {
  host: {
    installed: true, ovsVersion: '3.5.0', ovnVersion: '25.03.0', serviceOvs: true, serviceOvnCentral: true, serviceOvnHost: true, integrationBridge: 'br-int',
    systemId: 'hv-01', encapsulationType: 'geneve', encapsulationIp: '10.20.0.11', role: 'central', centralEndpoint: 'tcp:10.20.0.11:6642',
  },
  chassis: HOSTS.map((h, i) => ({ id: h.name, hostname: h.name, encapsulationIp: `10.20.0.${11 + i}`, encapsulationType: 'geneve' })),
}

// ---- Ceph (ADR-0073) ------------------------------------------------------------------------

const TB = 1e12
export const cephConfig: ClusterConfig = {
  fsid: '4b3d7b8a-9e12-4c28-86d3-0fa4b2e8179c', bootstrapHost: 'hv-01', monEndpoints: ['10.0.0.11:6789', '10.0.0.12:6789', '10.0.0.13:6789'],
  clientUser: 'virtomir', sshPublicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGRlbW8 ceph-4b3d7b8a', maintenanceHosts: [], noout: false,
  created: ago(41 * D), updated: ago(2 * D),
}

export const cephHosts: CephHost[] = HOSTS.map((h, i) => ({ hostname: h.name, ip: `10.0.0.${11 + i}`, labels: i === 0 ? ['_admin'] : [], status: 'online', memberId: h.id }))

export const cephOSDs = HOSTS.flatMap((h, i) => [
  { id: i * 2, name: `osd.${i * 2}`, host: h.name, deviceClass: 'nvme', weight: 1.74658, up: true },
  { id: i * 2 + 1, name: `osd.${i * 2 + 1}`, host: h.name, deviceClass: 'ssd', weight: 3.49309, up: true },
])

export const cephPools: RBDPool[] = [
  { id: 2, name: 'rbd-replicated', type: 'replicated', size: 3, minSize: 2, storedBytes: 1.42 * TB, percentUsed: 27.4, maxAvailBytes: 3.76 * TB, libvirtPool: 'ceph-vms',
    compressionMode: 'none', compressedBytes: 0, compressedFromBytes: 0 },
  { id: 3, name: 'rbd-cold-ec', type: 'erasure', size: 3, minSize: 3, ecProfile: 'ec-rbd-cold-ec', dataPool: 'rbd-cold-ec-data', storedBytes: 0.61 * TB, percentUsed: 8.1, maxAvailBytes: 7.52 * TB, libvirtPool: 'ceph-cold',
    compressionMode: 'aggressive', compressionAlgorithm: 'zstd', compressedBytes: 0.21 * TB, compressedFromBytes: 0.38 * TB },
]

export const storagePolicies: StoragePolicy[] = [
  { id: 'replica-3', name: 'Replicated (3 copies)', description: 'Default for production machines', pool: 'ceph-vms', default: true, created: ago(40 * D), updated: ago(40 * D) },
  { id: 'cold-ec', name: 'Cold (erasure coded 2+1)', description: 'Archives and backups of backups', pool: 'ceph-cold', default: false, created: ago(30 * D), updated: ago(30 * D) },
  { id: 'local-fast', name: 'Local NVMe (no copies)', description: 'Scratch and CI: lost with the host', pool: 'fast-zfs', default: false, created: ago(12 * D), updated: ago(12 * D) },
]

export const cephSpareDisks: Record<string, DiskInfo[]> = Object.fromEntries(HOSTS.map((h, i) => [h.id, [
  { path: '/dev/nvme0n1', name: 'nvme0n1', sizeBytes: 1.92e12, rotational: false, deviceType: 'nvme', fsType: 'ceph_bluestore', empty: false, refusalReason: 'is already a Ceph OSD' },
  { path: '/dev/sda', name: 'sda', sizeBytes: 480e9, rotational: false, deviceType: 'ssd', partitions: ['sda1', 'sda2'], mountPoint: '/', empty: false, refusalReason: 'mounted at /; has partitions sda1, sda2' },
  ...(i === 1 ? [{ path: '/dev/sdc', name: 'sdc', sizeBytes: 3.84e12, rotational: false, deviceType: 'ssd' as const, empty: true }] : []),
  ...(i === 2 ? [{ path: '/dev/sdd', name: 'sdd', sizeBytes: 8e12, rotational: true, deviceType: 'hdd' as const, fsType: 'LVM2_member', empty: false, refusalReason: 'is an LVM physical volume' }] : []),
]]))

/** The demo cluster's telemetry, from the hosts and OSDs above. */
export function cephTelemetry(): Telemetry {
  const inMaint = new Set(cephConfig.maintenanceHosts ?? [])
  const osds = cephOSDs.map((o) => ({ ...o, up: o.up && !inMaint.has(o.host) }))
  const down = osds.filter((o) => !o.up).length
  const total = 15.7 * TB
  const used = 4.12 * TB
  const checks: Telemetry['health']['checks'] = []
  if (cephConfig.noout) checks.push({ name: 'OSDMAP_FLAGS', severity: 'HEALTH_WARN', summary: 'noout flag(s) set' })
  if (down) {
    checks.push({ name: 'OSD_DOWN', severity: 'HEALTH_WARN', summary: `${down} osds down`, detail: osds.filter((o) => !o.up).map((o) => `${o.name} (root=default,host=${o.host}) is down`) })
    checks.push({ name: 'PG_DEGRADED', severity: 'HEALTH_WARN', summary: 'Degraded data redundancy: 41820/125460 objects degraded (33.333%), 96 pgs degraded' })
  }
  return {
    fsid: cephConfig.fsid,
    health: { status: checks.length ? 'HEALTH_WARN' : 'HEALTH_OK', checks },
    daemons: { mons: 3, quorum: HOSTS.filter((h) => !inMaint.has(h.name)).map((h) => h.name), mgrUp: true, osds: osds.length, osdsUp: osds.length - down, osdsIn: osds.length, noout: cephConfig.noout },
    capacity: { totalBytes: total, usedBytes: used, availBytes: total - used, percentUsed: (used / total) * 100, pools: [] },
    osdTree: [
      { id: -1, name: 'default', type: 'root', in: false, weight: 0, children: HOSTS.map((_, i) => -2 - i) },
      ...HOSTS.map((h, i) => ({ id: -2 - i, name: h.name, type: 'host', in: false, weight: 5.24, children: [i * 2, i * 2 + 1] })),
      ...osds.map((o) => ({ id: o.id, name: o.name, type: 'osd', status: o.up ? ('up' as const) : ('down' as const), in: true, weight: o.weight, deviceClass: o.deviceClass })),
    ],
    pgs: { total: 288, states: down ? { 'active+clean': 192, 'active+undersized+degraded': 96 } : { 'active+clean': 288 } },
    collected: new Date().toISOString(),
  }
}

// ---- Storage efficiency (ADR-0074) ----------------------------------------------------------

export const storageTiers: Record<string, Tier> = { 'fast-zfs': 'nvme', default: 'ssd', 'nas-nfs': 'hdd', 'ceph-vms': 'ssd', 'ceph-cold': 'hdd' }

export const zfsEfficiency: Record<string, ZFSEfficiency> = {
  'fast-zfs': { dataset: 'fast/vms', compression: 'lz4', compressRatio: 1.48, dedup: 'off', dedupRatio: 1, usedBytes: 1.66e12, logicalUsedBytes: 2.46e12, savedBytes: 0.8e12 },
}

export const nvmeSubsystems: NVMeSubsystem[] = [
  { nqn: 'nqn.2014.08.org.nvmexpress:144d144dS4EVNF0M123456 Samsung SSD 990 PRO 2TB', transport: 'pcie', address: '0000:01:00.0', state: 'live', managed: false,
    namespaces: [{ device: '/dev/nvme0n1', nsid: 1, sizeBytes: 2e12, model: 'Samsung SSD 990 PRO 2TB', serial: 'S7DNNJ0W123456' }] },
]

export const nvmeTargets: NVMeTarget[] = [
  { portal: '192.168.77.50', port: 4420, nqn: 'nqn.2026-01.io.spdk:cnode1' },
  { portal: '192.168.77.50', port: 4420, nqn: 'nqn.2026-01.io.spdk:cnode2' },
]

// ---- Physical network (ADR-0075) ------------------------------------------------------------

export const ipPools: IPPool[] = [
  {
    id: 'prod-v20', name: 'Production VLAN 20', network: 'default', cidr: '10.20.0.0/24', gateway: '10.20.0.1', rangeStart: '10.20.0.100', rangeEnd: '10.20.0.199',
    dnsServers: ['10.20.0.53'], domain: 'prod.example', reserved: ['10.20.0.150'], created: ago(20 * D), updated: ago(3 * D),
    allocations: [
      { ip: '10.20.0.100', mac: '52:54:00:3a:10:01', vmUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03', vmName: 'db-01', hostname: 'db-01', host: 'hv-01', created: ago(19 * D) },
      { ip: '10.20.0.101', mac: '52:54:00:3a:10:02', vmUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', vmName: 'web-01', hostname: 'web-01', host: 'hv-02', created: ago(12 * D) },
      { ip: '10.20.0.120', vmName: 'haproxy VIP', host: 'hv-01', created: ago(9 * D) },
    ],
  },
  { id: 'lab-v30', name: 'Lab VLAN 30', network: 'default', cidr: '10.30.0.0/24', gateway: '10.30.0.1', rangeStart: '10.30.0.50', rangeEnd: '10.30.0.99', allocations: [], created: ago(5 * D), updated: ago(5 * D) },
]

export const dnsConfig: DNSConfig = { enabled: true, server: '10.20.0.53:53', zone: 'prod.example.', reverseZones: ['20.10.in-addr.arpa.'], keyName: 'virtomir.', algorithm: 'hmac-sha256', ttl: 300 }

export const bgpView: BGPView = {
  config: {
    enabled: true, localAs: 65011, routerId: '10.0.0.11', evpn: true, appliedAs: 65011, announce: ['10.20.0.0/24'],
    neighbors: [{ address: '10.0.0.1', remoteAs: 65000, description: 'leaf1 eth49', bfd: true }, { address: '10.0.0.2', remoteAs: 65000, description: 'leaf2 eth49', bfd: true }],
  },
  frr: { installed: true, version: '10.2.1', bgpd: true },
  sessions: [
    { address: '10.0.0.1', remoteAs: 65000, description: 'leaf1 eth49', state: 'Established', uptime: '6d02h11m', prefixesReceived: 14, prefixesSent: 3, families: ['ipv4Unicast', 'l2VpnEvpn'] },
    { address: '10.0.0.2', remoteAs: 65000, description: 'leaf2 eth49', state: 'Established', uptime: '6d02h10m', prefixesReceived: 14, prefixesSent: 3, families: ['ipv4Unicast', 'l2VpnEvpn'] },
  ],
}

export const sriovDevices: SRIOVDevice[] = [
  {
    interface: 'ens1f0', pci: '0000:41:00.0', driver: 'mlx5_core', totalVfs: 16, currentVfs: 4, persistent: true,
    vfs: [0, 1, 2, 3].map((i) => ({ index: i, pci: `0000:41:00.${i + 2}`, netdev: i < 2 ? undefined : `ens1f0v${i}`, mac: i < 2 ? undefined : `52:54:00:5f:00:0${i}` })),
  },
  { interface: 'ens1f1', pci: '0000:41:00.1', driver: 'mlx5_core', totalVfs: 16, currentVfs: 0, persistent: false, vfs: [] },
]
