// Types + calls for the virtomird API (/api/v1). Mirrors internal/virt and internal/live.

import { apiLog } from './apilog.svelte'
import { router } from './router.svelte'

export type DomainState =
  | 'unknown'
  | 'running'
  | 'blocked'
  | 'paused'
  | 'shutting-down'
  | 'shutoff'
  | 'crashed'
  | 'pm-suspended'

export type Action = 'start' | 'shutdown' | 'reboot' | 'force-off' | 'pause' | 'resume' | 'reset' | 'save'

export interface HostInfo {
  hostname: string
  hypervisor: string
  hypervisorVersion: string
  libvirtVersion: string
  cpuModel: string
  cpus: number
  cpuMHz: number
  numaNodes: number
  sockets: number
  coresPerSocket: number
  threadsPerCore: number
  memoryKiB: number
}

export interface Domain {
  uuid: string
  name: string
  id?: number
  state: DomainState
  vcpus: number
  memoryKiB: number
  maxMemoryKiB: number
  cpuTimeNs: number
  persistent: boolean
  autostart: boolean
  /** Templates never start; new machines are made from them. */
  template?: boolean
  /** UUID of the template whose disks this machine's disks are overlays on. */
  linkedFrom?: string
  /** Who created it through VirtoMir (counts against their quota). */
  owner?: string
  /** The multi-tenancy project the machine belongs to (ADR-0060); empty = global. */
  project?: string
  /** Machine kind: 'vm' (default) or 'container' (LXC). */
  kind?: 'vm' | 'container'
  /** Share of the domain's own vCPUs in use (0–100). */
  cpuPercent: number
  /** Running machines, from libvirt bulk stats (absent on the first sample). */
  vcpuPercent?: number[]
  disks?: DiskLive[]
  nets?: NetLive[]
  /** Reported by the guest's balloon driver; absent when it does not report. */
  guestMemUsedKiB?: number
  guestMemTotalKiB?: number
  /** Host memory the QEMU process actually uses. */
  hostRssKiB?: number
}

export interface DiskLive {
  name: string
  readBps: number
  writeBps: number
  readIops: number
  writeIops: number
  readLatencyMs?: number
  writeLatencyMs?: number
}

export interface NetLive {
  name: string
  rxBps: number
  txBps: number
  rxPps: number
  txPps: number
  /** Dropped + errored packets per second. */
  problemsPs?: number
}

export interface Prop {
  label: string
  value: string
  /** libvirt XML name, e.g. "driver@cache". */
  tech?: string
}

export type DeviceGroup = 'storage' | 'network' | 'display' | 'console' | 'passthrough' | 'input' | 'system' | 'controller'

export interface Device {
  kind: string
  group: DeviceGroup
  title: string
  summary?: string
  bootOrder?: number
  props: Prop[]
  capacityBytes?: number
  allocationBytes?: number
  addresses?: string[]
  /** Disk: guest device name (vda); adapter: MAC. Used to act on the device. */
  target?: string
  mac?: string
  iotune?: DiskIoTune
  /** Adapter: average traffic limits in Mbit/s (0 = none). */
  bandwidth?: NICBandwidth
}

export interface GuestInfo {
  /** An agent answers; everything else is empty when it does not. */
  connected: boolean
  os: string
  kernel?: string
  hostname?: string
  timezone?: string
  users: string[]
  filesystems: { mountpoint: string; type: string; totalBytes: number; usedBytes: number }[]
}

export interface NICBandwidth {
  inMbps: number
  outMbps: number
}

export interface DiskIoTune {
  totalBytesSec?: number
  readBytesSec?: number
  writeBytesSec?: number
  totalIopsSec?: number
  readIopsSec?: number
  writeIopsSec?: number
  totalIopsSecMax?: number
  totalBytesSecMax?: number
}


/** GET /api/v1/domains/{uuid}: the machine page model. Mirrors virt.DomainDetail. */
export interface DomainDetail extends Omit<Domain, 'cpuPercent'> {
  title?: string
  description?: string
  system: {
    hypervisor: string
    osType: string
    arch: string
    machine: string
    firmware: 'bios' | 'uefi'
    secureBoot: boolean
    launchSecurity?: string

    loader?: string
    nvram?: string
    kernel?: string
    cmdline?: string
    bootOrder: string[]
    bootMenu: boolean
    emulator?: string
    clock?: string
    features: string[]
    onPoweroff?: string
    onReboot?: string
    onCrash?: string
  }
  cpu: {
    mode: string
    model?: string
    sockets: number
    dies?: number
    cores: number
    threads: number
    vcpus: number
    maxVcpus: number
    cpuset?: string
    features: number
    numaNodes: number
    pinned: boolean
  }
  memory: {
    maxKiB: number
    currentKiB: number
    hotplugKiB?: number
    hugepages: boolean
    balloon?: string
  }
  devices: Device[]
}

export interface Snapshot {
  time: string
  /** false: virtomird is up but cannot reach libvirt. */
  connected: boolean
  error?: string
  host: {
    cpuPercent: number
    memTotalKiB: number
    memUsedKiB: number
    /** Physical disk/network throughput (Linux hosts, from the second sample on). */
    io?: { diskReadBps: number; diskWriteBps: number; netRxBps: number; netTxBps: number }
  }
  domains: Domain[]
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

/** Called on every 401 so the app can show the sign-in page (set by the session store). */
let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(f: () => void) {
  onUnauthorized = f
}

interface SendOpts {
  accept?: string
  json?: unknown
  body?: BodyInit
  contentType?: string
}

/** API paths that are always about the host serving the UI: sign-in, cluster, tokens. */
const LOCAL = ['/api/v1/auth/', '/api/v1/cluster', '/api/v1/version', '/api/v1/tokens', '/api/v1/account/', '/api/v1/twofactor/']

/** Points an API path at the member being shown (ADR-0020): /api/v1/x → /api/v1/hosts/<id>/x. */
export function scoped(path: string): string {
  const host = router.host
  if (!host || path.startsWith('/api/v1/hosts/') || LOCAL.some((p) => path.startsWith(p))) return path
  return '/api/v1/hosts/' + host + path.slice('/api/v1'.length)
}

/** An API path on a given member, whichever one is shown (cluster-wide views). */
export function onHost(id: string, path: string): string {
  return '/api/v1/hosts/' + id + path.slice('/api/v1'.length)
}

/** path on member host when given (cluster-wide views), else on the member being shown. */
const at = (host: string | undefined, path: string) => (host ? onHost(host, path) : path)

/** WebSocket URL for an API path on the member being shown. */
export function wsURL(path: string): string {
  return `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${scoped(path)}`
}

async function send(method: string, path: string, opts: SendOpts = {}): Promise<Response> {
  path = scoped(path)
  const headers: Record<string, string> = { Accept: opts.accept ?? 'application/json' }
  let body = opts.body
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(opts.json)
  } else if (opts.contentType) headers['Content-Type'] = opts.contentType
  let res: Response
  const started = performance.now()
  try {
    res = await fetch(path, { method, headers, body })
  } catch {
    apiLog.add({ time: Date.now(), method, path, status: 0, ms: Math.round(performance.now() - started) })
    throw new ApiError('VirtoMir is not reachable', 0)
  }
  apiLog.add({ time: Date.now(), method, path, status: res.status, ms: Math.round(performance.now() - started) })
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = (await res.json()).error ?? msg
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && !path.startsWith('/api/v1/auth/')) onUnauthorized()
    throw new ApiError(msg, res.status)
  }
  return res
}

/** Uploads a file with progress (fetch cannot report upload progress). */
function sendFile<T>(url: string, file: File, onProgress: (fraction: number) => void, signal?: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', url)
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total)
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) return resolve(JSON.parse(xhr.responseText))
      let msg = xhr.statusText
      try {
        msg = JSON.parse(xhr.responseText).error ?? msg
      } catch {
        /* non-JSON error body */
      }
      if (xhr.status === 401) onUnauthorized()
      reject(new ApiError(msg, xhr.status))
    }
    xhr.onerror = () => reject(new ApiError('Upload failed: VirtoMir is not reachable', 0))
    xhr.onabort = () => reject(new ApiError('Upload cancelled', 0))
    signal?.addEventListener('abort', () => xhr.abort())
    xhr.send(file)
  })
}

async function request<T>(method: string, path: string, json?: unknown): Promise<T> {
  const res = await send(method, path, { json })
  const text = res.status === 204 ? '' : await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

const enc = encodeURIComponent
const domainPath = (uuid: string) => `/api/v1/domains/${enc(uuid)}`
const poolPath = (pool: string) => `/api/v1/storage/pools/${enc(pool)}`
const netPath = (name: string) => `/api/v1/networks/${enc(name)}`

// ---- Management API types (mirror internal/virt) ------------------------------------

export type Role = 'member' | 'viewer' | 'operator' | 'admin'

export interface Session {
  mode: 'pam' | 'none'
  user?: string
  role?: Role
  /** The password was right; the one-time code goes to /auth/2fa with pending. */
  needCode?: boolean
  pending?: string
}

export interface TwoFactorStatus {
  available: boolean
  enabled?: boolean
  since?: string
  recoveryLeft?: number
  pendingSetup?: boolean
}

export interface ApiToken {
  id: string
  name: string
  owner: string
  role: Role
  created: string
  expires?: string
  lastUsed?: string
}

export type Severity = 'warning' | 'critical'
export type AlertKind = 'host.cpu' | 'host.mem' | 'vm.cpu' | 'vm.crashed' | 'vm.down' | 'pool.free' | 'libvirt.down' | 'vm.anomaly'

export interface Alert {
  id: string
  ruleId: string
  kind: AlertKind
  severity: Severity
  subject: string
  subjectId?: string
  message: string
  value?: number
  started: string
  resolved?: string
  ackedBy?: string
  ackedAt?: string
}

export interface AlertRule {
  id: string
  kind: AlertKind
  enabled: boolean
  threshold?: number
  forSec: number
  target?: string
  severity: Severity
}

export interface AlertChannel {
  id: string
  name: string
  kind: 'webhook' | 'email'
  enabled: boolean
  minSeverity: Severity
  url?: string
  format?: 'json' | 'slack' | 'discord' | 'ntfy'
  to?: string
  resolved: boolean
}

export interface SMTPConfig {
  host: string
  port: number
  username?: string
  password?: string
  from: string
  tls: 'starttls' | 'tls' | 'none'
}

export interface AlertConfig {
  rules: AlertRule[]
  channels: AlertChannel[]
  smtp: SMTPConfig
}

/** Off-site target (ADR-0043); secrets come back as OFFSITE_SECRET. */
export interface OffsiteConfig {
  repository: string
  password?: string
  env?: Record<string, string>
  afterScheduled: boolean
  keepLast?: number
  keepDaily?: number
  keepWeekly?: number
  keepMonthly?: number
  keepYearly?: number
  immutabilityDays?: number
  limitUploadKiB?: number
}
export const OFFSITE_SECRET = '••••••••'

export interface OffsiteSnapshot {
  id: string
  shortId: string
  time: string
  host: string
  machine: string
  domainUuid: string
  backup: string
  path: string
  sizeBytes?: number
}

export interface BackupInfo {
  id: string
  domain: string
  domainUuid: string
  created: string
  live: boolean
  automatic: boolean
  disks: { target: string; file: string; source: string; sizeBytes: number }[]
  sizeBytes: number
  durationSec: number
  /** Only what changed since `parent` (another backup ID); it needs that backup to restore. */
  incremental?: boolean
  parent?: string
}

export interface ScheduleInfo {
  id: string
  kind: 'snapshot' | 'backup'
  domain: string
  enabled: boolean
  every: 'hours' | 'daily' | 'weekly'
  hours?: number
  at?: string
  weekday?: number
  keep: number
  keepDaily?: number
  keepWeekly?: number
  keepMonthly?: number
  keepYearly?: number
  /** Backups: 0 = always full; n > 1 = incremental with a full backup every n. */
  fullEvery?: number
  lastRun?: string
  lastError?: string
  lastResult?: string
  nextRun?: string
}

export interface ActivityEntry {
  time: string
  user: string
  token?: string
  remote?: string
  method: string
  path: string
  status: number
  error?: string
}

export interface SIEMConfig {
  enabled: boolean
  target: string
  facility: number
  tag: string
  hostname: string
  tlsInsecure?: boolean
  /** Outcome of the latest delivery; absent when it worked. */
  lastError?: string
}


// ---- Compliance & audit integrity (mirror internal/compliance, internal/activity) ----

/** Severity expresses the security impact of a compliance finding. */
export type CheckSeverity = 'critical' | 'high' | 'medium' | 'low'

/** The evaluation outcome of an individual compliance check. */
export type CheckStatus = 'pass' | 'fail' | 'warn' | 'error'

export interface ComplianceCheck {
  id: string
  title: string
  /** "SSH", "Virtualization", "Firewall", "Kernel", "System". */
  category: string
  severity: CheckSeverity
  status: CheckStatus
  detail: string
  remediation: string
  benchmark: string
}

export interface HostCompliance {
  hostId: string
  hostName: string
  evaluatedAt: string
  /** 0–100 percentage; -1 when a cluster peer could not be reached. */
  score: number
  passedCount: number
  failedCount: number
  warningCount: number
  totalCount: number
  results: ComplianceCheck[]
}

/** Host kernel FIPS state and the daemon's TLS restriction profile. Evaluation is experimental. */
export interface FIPSInfo {
  kernelEnabled: boolean
  enforced: boolean
  tlsMinVersion: string
  cipherSuites: string[]
  curves: string[]
  description: string
}

/** Tamper-evident audit log verification: the hash chain across activity.jsonl. */
export interface AuditVerification {
  valid: boolean
  verifiedCount: number
  firstSeq?: number
  lastSeq?: number
  headHash?: string
  tamperedSeq?: number
  tamperedIndex?: number
  reason?: string
}

export interface Job {
  id: string
  kind: string
  title: string
  state: 'running' | 'succeeded' | 'failed'
  message?: string
  result?: string
  started: string
  finished?: string
}

export interface DefineResult {
  appliesOnNextBoot: boolean
}

export interface DeleteResult {
  removedVolumes: string[]
  keptVolumes: { path: string; reason: string }[]
}

export interface DiskSpec {
  device?: 'disk' | 'cdrom'
  bus?: string
  path?: string
  pool?: string
  volume?: string
  sizeGiB?: number
  format?: 'qcow2' | 'raw'
}

export interface NICSpec {
  network?: string
  bridge?: string
  model?: string
  mac?: string
  /** VLAN tag for this adapter (Open vSwitch networks, macvtap, SR-IOV); 0/absent = untagged. */
  vlan?: number
}

export interface ContainerImage {
  key: string
  label: string
}

export interface CreateSpec {
  name: string
  kind?: 'vm' | 'container'
  os: 'linux' | 'windows' | 'other'
  vcpus: number
  memoryMiB: number
  firmware: 'uefi' | 'bios'
  secureBoot: boolean
  tpm: boolean
  confidential?: 'sev' | 'sev-snp' | 'tdx'
  pool: string
  diskGiB: number
  provisioning?: 'thin' | 'thick'
  diskPath?: string
  /** Cloud image to copy and grow to diskGiB. */
  baseImage?: string
  cloudInit?: CloudInit
  iso?: string
  /** A second CD with drivers (virtio-win for Windows); never booted. */
  driverIso?: string
  network?: NICSpec
  start: boolean
  /** The project the new machine belongs to (ADR-0060); empty = global. */
  project?: string
  /** Containers: a ContainerImage key and root's password. */
  image?: string
  rootPassword?: string
}

/** POST /api/v1/domains/{uuid}/deploy: a new machine from a template. */
export interface DeploySpec {
  name: string
  linked: boolean
  cloudInit?: CloudInit
  start: boolean
}

export interface CloudInit {
  hostname?: string
  user: string
  password?: string
  sshKeys?: string[]
  packages?: string[]
  userData?: string
}

/** The machine settings form; mirrors virt.Settings. */
export interface Settings {
  title: string
  description: string
  vcpus: number
  maxVcpus: number
  cpuMode: 'host-passthrough' | 'host-model' | 'maximum' | 'custom'
  cpuModel?: string
  sockets: number
  cores: number
  threads: number
  memoryMiB: number
  currentMemoryMiB: number
  bootOrder: string[]
  bootMenu: boolean
  firmware: 'uefi' | 'bios'
  secureBoot: boolean
  /** Per vCPU: host CPU set ("2", "4-5"); "" = anywhere. */
  pinning: string[]
  emulatorPin: string
  hugePages: boolean
  numaMode: '' | 'strict' | 'preferred' | 'interleave' | 'restrictive'
  numaNodes: string
  /** Share of CPU time against other machines when the host is busy; 0 = default (1024). */
  cpuWeight: number
  /** Cap per vCPU in percent of one host CPU; 0 = none. */
  cpuLimit: number
  /** Whether the guest may run machines itself; '' = the host's default. */
  nested: '' | 'on' | 'off'
}

export interface SettingsView extends Settings {
  bootDevices: { key: string; label: string }[]
  hotplugMemoryMiB?: number
}

export interface SaveResult extends DefineResult {
  appliedLive: string[]
  liveErrors: string[]
}

export interface DHCPHost {
  mac: string
  ip: string
  name?: string
}

export type HistoryRange = '1h' | '6h' | '24h' | '7d' | '30d'

export interface HistoryResult {
  start: number
  step: number
  series: Record<string, (number | null)[]>
}

export interface SnapshotInfo {
  name: string
  description?: string
  parent?: string
  state: string
  created: number
  current: boolean
  memory: boolean
  location: 'internal' | 'external'
}

export interface Pool {
  name: string
  uuid: string
  type: string
  path?: string
  active: boolean
  autostart: boolean
  capacityBytes: number
  allocationBytes: number
  availableBytes: number
  volumes: number
}

export interface Volume {
  name: string
  path: string
  type: string
  format: string
  capacityBytes: number
  allocationBytes: number
  backing?: string
  usedBy: string[]
}

export interface VNetwork {
  name: string
  uuid: string
  active: boolean
  autostart: boolean
  persistent: boolean
  mode: string
  bridge?: string
  subnet?: string
  dhcpStart?: string
  dhcpEnd?: string
  domain?: string
  subnet6?: string
  dhcp6Start?: string
  dhcp6End?: string
  usedBy: string[]
  dhcpHosts: DHCPHost[]
  vni?: number
  /** VXLAN: the other hosts' addresses. */
  peers?: string[]
}

export interface NetworkSpec {
  name: string
  mode: 'nat' | 'route' | 'isolated' | 'bridge' | 'vxlan'
  subnet?: string
  dhcp: boolean
  bridge?: string
  vni?: number
  peers?: string[]
  /** Bridge mode: the host bridge is Open vSwitch; vlan tags every machine on it. */
  ovs?: boolean
  vlan?: number
  /** IPv6 /64 (nat, route, isolated): SLAAC from the host; dhcp6 adds DHCPv6. */
  subnet6?: string
  dhcp6?: boolean
}


export interface Lease {
  mac: string
  ip: string
  hostname?: string
  expires: number
  machine?: string
}

export interface PoolSpec {
  name: string
  path: string
  /** dir (default), netfs (NFS host:export mounted at path), iscsi (portal host + target IQN), rbd (Ceph),
   *  logical (LVM volume group in source; devices make a new one), zfs (existing zpool/dataset in source). */
  type?: 'dir' | 'netfs' | 'iscsi' | 'rbd' | 'logical' | 'zfs'
  host?: string
  export?: string
  target?: string
  monitors?: string[]
  cephPool?: string
  cephUser?: string
  cephKey?: string
  source?: string
  devices?: string[]
}

// VMware migration (ADR-0061)
export interface VMwareConnection {
  url?: string
  username?: string
  password?: string
  thumbprint?: string
  insecure?: boolean
  esxi?: boolean
}

export interface VMwareTestResult {
  ok: boolean
  message: string
}

export interface VMwareDatacenter {
  name: string
}

export interface VMwareHost {
  name: string
  datacenter: string
}

export interface VMwareDisk {
  datastore: string
  capacityBytes: number
}

export interface VMwareNIC {
  network: string
  mac: string
}

export interface VMwareVM {
  id: string
  name: string
  datacenter: string
  host: string
  powerState: 'on' | 'off' | 'suspended' | string
  vcpus: number
  memoryMiB: number
  guestOs: string
  disks: VMwareDisk[]
  nics: VMwareNIC[]
}

export interface VMwareInventory {
  datacenters: VMwareDatacenter[]
  hosts: VMwareHost[]
  vms: VMwareVM[]
}

export interface VMwarePreflightItem {
  vmId: string
  issues: string[]
}

export interface VMwarePreflightResult {
  pool: string
  poolPath: string
  poolFreeBytes: number
  issues: Record<string, string[]>
  items: VMwarePreflightItem[]
}

export interface VMwareImportSpec {
  vmId: string
  pool: string
  name?: string
  networkMap?: Record<string, string>
  virtioIso?: string
}

export interface VMwareWaveSpec {
  vmIds: string[]
  pool: string
  parallel?: number
  networkMap?: Record<string, string>
  virtioIso?: string
}

export type VMwareWaveItemState = 'pending' | 'running' | 'done' | 'failed'

export interface VMwareWaveItem {
  vmId: string
  name: string
  state: VMwareWaveItemState
  message?: string
}

export interface VMwareWave {
  id: string
  items: VMwareWaveItem[]
  parallel: number
  created: string
}

export interface VMwareRetrySpec {
  pool?: string
  networkMap?: Record<string, string>
  virtioIso?: string
}

// Catalog (ADR-0065): blueprints, orders, leases, pricing and showback.
export interface SizePreset {
  id: string
  name: string
  vcpus: number
  memoryMiB: number
  diskGiB: number
}

export interface InputField {
  key: string
  label: string
  type: 'string' | 'password' | 'ssh_key' | 'number' | 'select'
  required: boolean
  default?: string
  options?: string[]
}

export interface Blueprint {
  id: string
  name: string
  description?: string
  /** Icon key rendered next to the name (globe, database, terminal …). */
  icon?: string
  sourceType: 'template' | 'image'
  sourceRef: string
  targetPool?: string
  presets: SizePreset[]
  defaultPresetId?: string
  allowedNetworks?: string[]
  cloudInitTemplate?: string
  cloudInitFields?: InputField[]
  /** SSH public keys injected into every machine provisioned from this image blueprint. */
  defaultSshKeys?: string[]
  allowedProjects?: string[]
  requireApproval: boolean
  defaultLeaseDays?: number
  maxLeaseDays?: number
  created: string
  updated: string
}

export type CatalogRequestStatus =
  | 'pending_approval'
  | 'approved'
  | 'rejected'
  | 'provisioning'
  | 'active'
  | 'expired'
  | 'failed'

export interface CatalogRequest {
  id: string
  blueprintId: string
  projectId: string
  requester: string
  vmName: string
  presetId: string
  vcpus: number
  memoryMiB: number
  diskGiB: number
  network?: string
  /** Answers to the blueprint's cloud-init questions; password answers are masked in responses. */
  fieldValues?: Record<string, string>
  /** Public SSH keys for cloud-init sign-in (one per element). */
  sshKeys?: string[]
  /** Plaintext password for cloud image blueprints; accepted on POST, never stored or returned. */
  password?: string
  leaseDays: number
  status: CatalogRequestStatus
  createdAt: string
  reviewedAt?: string
  reviewedBy?: string
  reviewComment?: string
  machineUuid?: string
  errorMessage?: string
}

/** The lease of a provisioned catalog machine (distinct from DHCP leases). */
export interface CatalogLease {
  requestId: string
  machineUuid: string
  projectId: string
  expiry: string
  graceExpiry?: string
  state: 'active' | 'reminder_sent' | 'grace_period' | 'terminated'
  reminderSent: boolean
  lastStateChange: string
}

export interface PricingConfig {
  vcpuHour: number
  ramGiBHour: number
  diskGiBMonth: number
  currency: string
}

export interface VMCost {
  uuid: string
  name: string
  projectId: string
  vcpus: number
  memoryMiB: number
  diskGiB: number
  state: string
  runtimeHours: number
  vcpuCost: number
  ramCost: number
  diskCost: number
  totalCost: number
}

export interface ProjectCost {
  projectId: string
  projectName: string
  machineCount: number
  totalVcpus: number
  totalMemoryMiB: number
  totalDiskGiB: number
  vcpuCost: number
  ramCost: number
  diskCost: number
  totalCost: number
  machines: VMCost[]
}

export interface ShowbackReport {
  period: string
  year: number
  month: number
  generatedAt: string
  currency: string
  pricing: PricingConfig
  totalCost: number
  totalMachines: number
  projects: ProjectCost[]
}

export interface HostPCI {
  name: string
  address: string
  class: string
  className: string
  vendor: string
  product: string
  vendorId: string
  productId: string
  driver?: string
  iommuGroup?: number
  group?: string[]
  usedBy: string[]
  /** Why the host itself needs it (mounted disk, interface with an address). */
  hostUse?: string
  /** Why it cannot be passed through; absent when it can. */
  blocked?: string
  /** Slices the card can be split into (NVIDIA vGPU, Intel GVT-g, …). */
  mdevTypes?: HostMDevType[]
  /** SR-IOV Physical Function indicator and VF capacity */
  sriov?: boolean
  sriovTotalVfs?: number
  sriovCurrentVfs?: number
  /** SR-IOV Virtual Function indicator and parent PF address */
  isVf?: boolean
  pfAddress?: string
}

export interface HostMDevType {
  id: string
  name: string
  api: string
  available: number
}

/** A mediated device (vGPU slice) on the host. */
export interface HostMDev {
  name: string
  uuid: string
  /** PCI address of the card it is cut from. */
  parent: string
  type: string
  typeName?: string
  active: boolean
  autostart: boolean
  usedBy: string[]
}

export type ExtraDeviceKind = 'watchdog' | 'tpm' | 'rng' | 'filesystem' | 'mdev' | 'clipboard' | 'video'

export interface DeviceSpec {
  kind: ExtraDeviceKind
  model?: string
  action?: string
  source?: string
  tag?: string
  readOnly?: boolean
  uuid?: string
  heads?: number
}

/** Kinds the Remove button sends to …/devices/detach. */
export type DetachKind =
  | 'disk'
  | 'interface'
  | 'hostdev-pci'
  | 'hostdev-usb'
  | 'hostdev-mdev'
  | 'watchdog'
  | 'tpm'
  | 'rng'
  | 'filesystem'
  | 'clipboard'

export interface DeviceResult {
  /** The machine runs and only gets the change from its next start. */
  appliesOnNextBoot: boolean
  /** Why the live change failed (the saved definition has it anyway). */
  liveError?: string
}

export interface QEMUArgs {
  args: string[]
  env: Record<string, string>
}

export interface HostUSB {
  name: string
  bus: number
  device: number
  vendorId: string
  productId: string
  vendor: string
  product: string
  usedBy: string[]
  blocked?: string
}

export interface HostHardware {
  iommu: boolean
  pci: HostPCI[]
  usb: HostUSB[]
  mdevs: HostMDev[]
}

export interface GroupRoles {
  admin: string[]
  operator: string[]
  viewer: string[]
}

export interface SigninConfig {
  ldap: {
    enabled: boolean
    url: string
    startTLS: boolean
    insecureSkipVerify: boolean
    bindDN: string
    bindPassword?: string
    userBase: string
    userFilter: string
    groupAttr: string
    roles: GroupRoles
  }
  oidc: {
    enabled: boolean
    label: string
    issuer: string
    clientId: string
    clientSecret?: string
    usernameClaim: string
    groupsClaim: string
    roles: GroupRoles
  }
}

export interface QuotaRule {
  kind: 'user' | 'group'
  name: string
  machines: number
  vcpus: number
  memoryMiB: number
  /** The full size of the owned machines' own disks; null/absent: no limit (ADR-0044). */
  diskGiB?: number | null
}

export interface MyQuota {
  unlimited: boolean
  rule?: QuotaRule
  used?: { machines: number; vcpus: number; memoryMiB: number; diskGiB?: number }
}

/** A member of a project: one user (kind "user") or a whole Linux group (kind "group"). */
export interface ProjectMember {
  kind: 'user' | 'group'
  name: string
  role: Role
}

/** What a project's machines together may use; zero means unlimited. */
export interface ProjectLimits {
  machines: number
  vcpus: number
  memoryMiB: number
  diskGiB: number
}

/** What a project's machines use now. */
export interface ProjectUsage {
  machines: number
  vcpus: number
  memoryMiB: number
  diskGiB: number
}

/** One tenant (ADR-0060): machines, storage pools and networks with members and limits. */
export interface Project {
  id: string
  name: string
  description?: string
  members: ProjectMember[]
  limits: ProjectLimits
  storagePools: string[]
  networks: string[]
  created: string
}

export interface ConfigVersion {
  id: string
  time: string
  /** Who changed it through VirtoMir; empty for changes made outside (virsh, other tools). */
  user?: string
  source: 'virtomir' | 'outside' | 'first seen'
  size: number
}

export interface ClusterMember {
  id: string
  name: string
  address: string
  fingerprint: string
  self: boolean
  reachable: boolean
  version?: string
  error?: string
}

export interface ClusterInfo {
  /** false: this host runs without TLS and cannot join others. */
  enabled: boolean
  selfId?: string
  members: ClusterMember[]
}

export interface Invite {
  code: string
  address: string
  expires: string
}

/** One row of the API reference, from GET /api/v1/openapi.json. */
export interface ApiEndpoint {
  method: string
  path: string
  summary: string
  role: string
}

interface OpenAPIDoc {
  paths: Record<string, Record<string, { summary?: string; description?: string }>>
}

// Content Library (ADR-0063)
export type ContentItemType = 'template' | 'iso' | 'image'
export type ContentSyncStatus = 'ready' | 'syncing' | 'missing'

export interface ContentVersion {
  id: string
  version: string
  description?: string
  sha256: string
  size: number
  format: string
  sourceHost: string
  sourcePool?: string
  sourceVol?: string
  templateXml?: string
  created: string
}

export interface ContentSyncPolicy {
  mode: 'all' | 'selected' | 'manual'
  hosts?: string[]
}

export interface ContentItem {
  id: string
  name: string
  type: ContentItemType
  description?: string
  sourceHost: string
  keepVersions: number
  syncPolicy: ContentSyncPolicy
  created: string
  updated: string
  versions?: ContentVersion[]
  versionCount?: number
  latestVersion?: ContentVersion
  syncStatus?: ContentSyncStatus | string
}

export interface ContentHostConfig {
  hostId?: string
  targetPool: string
  autoSync: boolean
}

export interface ContentDeploySpec {
  itemId?: string
  versionId?: string
  name: string
  pool: string
  network?: string
  vcpus?: number
  memoryMiB?: number
  diskGiB?: number
  cloudInit?: {
    hostname?: string
    user: string
    password?: string
    sshKeys?: string[]
    packages?: string[]
    userData?: string
  }
  start?: boolean
}

export interface PublishContentReq {
  templateUuid?: string
  pool?: string
  volume?: string
  id?: string
  name?: string
  type?: ContentItemType
  description?: string
  version?: string
  keepVersions?: number
  syncPolicy?: ContentSyncPolicy
  sourceHost?: string
  sourcePool?: string
  sourceVol?: string
  sha256?: string
  size?: number
  format?: string
  templateXml?: string
}

export interface PublishContentVersionReq {
  templateUuid?: string
  pool?: string
  volume?: string
  id?: string
  version?: string
  description?: string
  sha256?: string
  size?: number
  format?: string
  sourceHost?: string
  sourcePool?: string
  sourceVol?: string
  templateXml?: string
}

export const api = {
  streamURL: () => scoped('/api/v1/stream'),

  get: <T = any>(path: string) => request<T>('GET', path),
  post: <T = any>(path: string, body?: any) => request<T>('POST', path, body),
  put: <T = any>(path: string, body?: any) => request<T>('PUT', path, body),
  delete: <T = any>(path: string) => request<T>('DELETE', path),

  endpoints: async (): Promise<ApiEndpoint[]> => {
    const doc = await request<OpenAPIDoc>('GET', '/api/v1/openapi.json')
    const out: ApiEndpoint[] = []
    for (const [path, ops] of Object.entries(doc.paths)) {
      for (const [method, op] of Object.entries(ops)) {
        const role = /role: (\w+)/.exec(op.description ?? '')?.[1] ?? 'public'
        out.push({ method: method.toUpperCase(), path, summary: op.summary ?? '', role })
      }
    }
    return out.sort((a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method))
  },

  cluster: () => request<ClusterInfo>('GET', '/api/v1/cluster'),
  /** A named CPU model every cluster member supports (for migration between different hosts). */
  cpuBaseline: () => request<{ model: string; hosts: number }>('GET', '/api/v1/cluster/cpu-baseline'),
  /** Newest live snapshot of a member (cluster overview). */
  summary: (host: string) => request<Snapshot>('GET', onHost(host, '/api/v1/summary')),
  invite: () => request<Invite>('POST', '/api/v1/cluster/invite'),
  join: (code: string) => request<void>('POST', '/api/v1/cluster/join', { code }),
  removeMember: (id: string) => request<void>('DELETE', `/api/v1/cluster/members/${enc(id)}`),
  host: () => request<HostInfo>('GET', '/api/v1/host'),

  session: () => request<Session>('GET', '/api/v1/auth/session'),
  login: (user: string, password: string) => request<Session>('POST', '/api/v1/auth/login', { user, password }),
  logout: () => request<void>('POST', '/api/v1/auth/logout'),
  loginCode: (pending: string, code: string) => request<Session>('POST', '/api/v1/auth/2fa', { pending, code }),
  twoFactor: () => request<TwoFactorStatus>('GET', '/api/v1/account/2fa'),
  twoFactorSetup: () => request<{ secret: string; uri: string; qr: string }>('POST', '/api/v1/account/2fa/setup'),
  twoFactorConfirm: (code: string) => request<{ recovery: string[] }>('POST', '/api/v1/account/2fa/confirm', { code }),
  twoFactorDisable: (code: string) => request<void>('POST', '/api/v1/account/2fa/disable', { code }),
  twoFactorUsers: async () => (await request<{ users: string[] }>('GET', '/api/v1/twofactor/users')).users,
  twoFactorReset: (user: string) => request<void>('DELETE', `/api/v1/twofactor/users/${enc(user)}`),

  tokens: async (all = false) => (await request<{ tokens: ApiToken[] }>('GET', `/api/v1/tokens${all ? '?all=true' : ''}`)).tokens,
  createToken: (name: string, role: Role, ttlDays: number) =>
    request<{ token: ApiToken; secret: string }>('POST', '/api/v1/tokens', { name, role, ttlDays }),
  revokeToken: (id: string) => request<void>('DELETE', `/api/v1/tokens/${enc(id)}`),
  activity: async (q: { user?: string; target?: string; failed?: boolean; limit?: number } = {}, host?: string) => {
    const p = new URLSearchParams()
    if (q.user) p.set('user', q.user)
    if (q.target) p.set('target', q.target)
    if (q.failed) p.set('failed', 'true')
    p.set('limit', String(q.limit ?? 300))
    return (await request<{ entries: ActivityEntry[] }>('GET', at(host, `/api/v1/activity?${p}`))).entries
  },
  siemConfig: () => request<SIEMConfig>('GET', '/api/v1/activity/siem'),
  saveSiemConfig: (cfg: SIEMConfig) => request<SIEMConfig>('PUT', '/api/v1/activity/siem', cfg),
  /** Re-runs the tamper-evident chain check over the whole audit log (operator). */
  verifyAuditLog: () => request<AuditVerification>('POST', '/api/v1/activity/verify'),
  /** Where to download the audit trail (GET; an admin download). */
  exportAuditLogURL: (format: 'jsonl' | 'csv') => scoped(`/api/v1/activity/export?format=${format}`),

  // Compliance & hardening (ADR-0064)
  complianceChecks: (refresh = false) => request<HostCompliance>('GET', `/api/v1/compliance/checks${refresh ? '?refresh=1' : ''}`),
  runComplianceScan: () => request<HostCompliance>('POST', '/api/v1/compliance/checks/run'),
  complianceFIPS: () => request<FIPSInfo>('GET', '/api/v1/compliance/fips'),
  /** The host's own posture first, then every reachable cluster member's (score -1 = unreachable). */
  clusterCompliance: () => request<HostCompliance[]>('GET', '/api/v1/cluster/compliance'),


  alerts: async (host?: string) => (await request<{ alerts: Alert[] }>('GET', at(host, '/api/v1/alerts'))).alerts,
  ackAlert: (id: string, host?: string) => request<void>('POST', at(host, `/api/v1/alerts/${enc(id)}/ack`)),
  alertConfig: () => request<AlertConfig>('GET', '/api/v1/alerts/config'),
  saveAlertConfig: (c: AlertConfig, host?: string) => request<AlertConfig>('PUT', at(host, '/api/v1/alerts/config'), c),
  testChannel: (ch: AlertChannel) => request<void>('POST', '/api/v1/alerts/test', ch),

  backups: async (domain?: string, host?: string) =>
    request<{ backups: BackupInfo[]; dir: string }>('GET', at(host, `/api/v1/backups${domain ? `?domain=${enc(domain)}` : ''}`)),
  backupNow: (uuid: string, incremental = false) => request<Job>('POST', `${domainPath(uuid)}/backup`, { incremental }),
  deleteBackup: (id: string, host?: string) => request<void>('DELETE', at(host, `/api/v1/backups?id=${enc(id)}`)),
  restoreBackup: (id: string, name: string, pool: string, host?: string) =>
    request<Job>('POST', at(host, '/api/v1/backups/restore'), { id, name, pool }),
  backupSettings: () => request<{ backupDir: string }>('GET', '/api/v1/backups/settings'),
  offsite: (host?: string) => request<{ config: OffsiteConfig; status: { restic?: string; configured: boolean } }>('GET', at(host, '/api/v1/offsite')),
  saveOffsite: (c: OffsiteConfig, host?: string) =>
    request<{ config: OffsiteConfig; status: { restic?: string; configured: boolean } }>('PUT', at(host, '/api/v1/offsite'), c),
  checkOffsite: (init: boolean, host?: string) => request<{ message: string }>('POST', at(host, '/api/v1/offsite/check'), { init }),
  offsiteSnapshots: async (host?: string) => (await request<{ snapshots: OffsiteSnapshot[] }>('GET', at(host, '/api/v1/offsite/snapshots'))).snapshots,
  offsiteUpload: (backup: string, host?: string) => request<Job>('POST', at(host, '/api/v1/offsite/upload'), { backup }),
  offsiteRestore: (snapshot: string, host?: string) => request<Job>('POST', at(host, '/api/v1/offsite/restore'), { snapshot }),
  saveBackupSettings: (backupDir: string, host?: string) =>
    request<{ backupDir: string }>('PUT', at(host, '/api/v1/backups/settings'), { backupDir }),
  schedules: async (domain?: string) =>
    (await request<{ schedules: ScheduleInfo[] }>('GET', `/api/v1/schedules${domain ? `?domain=${enc(domain)}` : ''}`)).schedules,
  saveSchedule: (s: ScheduleInfo) => request<ScheduleInfo>('PUT', '/api/v1/schedules', s),
  deleteSchedule: (id: string) => request<void>('DELETE', `/api/v1/schedules/${enc(id)}`),
  runSchedule: (id: string) => request<void>('POST', `/api/v1/schedules/${enc(id)}/run`),

  job: (id: string, host?: string) => request<Job>('GET', at(host, `/api/v1/jobs/${enc(id)}`)),
  jobs: async (host?: string) =>
    (await request<{ jobs: Job[] }>('GET', at(host, '/api/v1/jobs'))).jobs,

  // Machines
  domain: (uuid: string) => request<DomainDetail>('GET', domainPath(uuid)),
  /** inactive: the persistent config used on next boot instead of the live one. */
  domainXML: async (uuid: string, inactive = false) =>
    (await send('GET', `${domainPath(uuid)}/xml${inactive ? '?inactive=true' : ''}`, { accept: 'application/xml' })).text(),
  saveXML: async (uuid: string, xml: string): Promise<DefineResult> =>
    (await send('PUT', `${domainPath(uuid)}/xml`, { body: xml, contentType: 'application/xml' })).json(),
  action: (uuid: string, action: Action) => request<void>('POST', `${domainPath(uuid)}/actions/${action}`),
  create: (spec: CreateSpec, respool?: string) =>
    request<{ uuid: string; startError?: string }>('POST', `/api/v1/domains${respool ? `?respool=${enc(respool)}` : ''}`, spec),
  /** A system container is made in the background (the image may be downloaded first). */
  createContainer: (spec: CreateSpec, respool?: string) =>
    request<Job>('POST', `/api/v1/domains${respool ? `?respool=${enc(respool)}` : ''}`, spec),
  /** Memory encryption this host can give new machines (sev, sev-snp, tdx). */
  confidentialKinds: async () => (await request<{ kinds: string[] }>('GET', '/api/v1/host/confidential')).kinds,
  containerImages: () => request<{ images: ContainerImage[]; available: boolean; reason?: string }>('GET', '/api/v1/containers/images'),
  setTemplate: (uuid: string, template: boolean) => request<void>('PUT', `${domainPath(uuid)}/template`, { template }),
  deploy: (uuid: string, spec: DeploySpec) => request<Job>('POST', `${domainPath(uuid)}/deploy`, spec),
  setAutostart: (uuid: string, enabled: boolean) => request<void>('PUT', `${domainPath(uuid)}/autostart`, { enabled }),
  rename: (uuid: string, name: string) => request<void>('POST', `${domainPath(uuid)}/rename`, { name }),
  hostDevices: () => request<HostHardware>('GET', '/api/v1/host/devices'),
  authMethods: () => request<{ ldap: boolean; oidc: boolean; oidcLabel?: string }>('GET', '/api/v1/auth/methods'),
  signinConfig: () => request<{ config: SigninConfig; redirectUrl: string }>('GET', '/api/v1/signin-config'),
  saveSigninConfig: (c: SigninConfig) => request<{ config: SigninConfig; redirectUrl: string }>('PUT', '/api/v1/signin-config', c),
  quotas: async () => (await request<{ rules: QuotaRule[] }>('GET', '/api/v1/quotas')).rules,
  saveQuotas: async (rules: QuotaRule[], host?: string) =>
    (await request<{ rules: QuotaRule[] }>('PUT', at(host, '/api/v1/quotas'), { rules })).rules,
  myQuota: () => request<MyQuota>('GET', '/api/v1/quotas/mine'),
  projects: async () => (await request<{ projects: Project[] }>('GET', '/api/v1/projects')).projects,
  createProject: (p: Project) => request<{ project: Project }>('POST', '/api/v1/projects', p),
  updateProject: (id: string, p: Project) => request<{ project: Project }>('PUT', `/api/v1/projects/${enc(id)}`, p),
  deleteProject: (id: string) => request<void>('DELETE', `/api/v1/projects/${enc(id)}`),
  /** Moves a machine into a project ("" makes it global again). */
  setMachineProject: (uuid: string, project: string) => request<void>('PUT', `${domainPath(uuid)}/project`, { project }),
  attachHostdev: (uuid: string, spec: { type: 'pci' | 'usb'; address?: string; vendorId?: string; productId?: string }) =>
    request<{ liveError: string }>('POST', `${domainPath(uuid)}/hostdevs`, spec),
  configVersions: async (uuid: string) =>
    (await request<{ versions: ConfigVersion[] }>('GET', `${domainPath(uuid)}/config-history`)).versions,
  configVersion: async (uuid: string, id: string) =>
    (await request<{ xml: string }>('GET', `${domainPath(uuid)}/config-history/${enc(id)}`)).xml,
  revertConfig: (uuid: string, id: string) =>
    request<{ appliesOnNextBoot: boolean }>('POST', `${domainPath(uuid)}/config-history/${enc(id)}/revert`),
  clone: (uuid: string, name: string) => request<Job>('POST', `${domainPath(uuid)}/clone`, { name }),
  /** Moves a machine to another cluster member: copied when stopped, live-migrated when running. */
  move: (uuid: string, host: string, pool: string) => request<Job>('POST', `${domainPath(uuid)}/move`, { host, pool }),
  /** Storage pools of a given member. */
  poolsOn: async (host: string | undefined) => (await request<{ pools: Pool[] }>('GET', at(host, '/api/v1/storage/pools'))).pools,
  remove: (uuid: string, removeStorage: boolean) =>
    request<DeleteResult>('DELETE', `${domainPath(uuid)}${removeStorage ? '?storage=remove' : ''}`),

  changeMedia: (uuid: string, target: string, source: string) =>
    request<void>('PUT', `${domainPath(uuid)}/media`, { target, source }),
  attachDisk: (uuid: string, spec: DiskSpec) => request<{ target: string }>('POST', `${domainPath(uuid)}/disks`, spec),
  attachNIC: (uuid: string, spec: NICSpec) => request<void>('POST', `${domainPath(uuid)}/interfaces`, spec),
  detach: (uuid: string, ref: { kind: DetachKind; target?: string; mac?: string }) =>
    request<DeviceResult>('POST', `${domainPath(uuid)}/devices/detach`, ref),
  addDevice: (uuid: string, spec: DeviceSpec) => request<DeviceResult>('POST', `${domainPath(uuid)}/devices`, spec),
  qemuArgs: (uuid: string) => request<QEMUArgs>('GET', `${domainPath(uuid)}/qemu-args`),
  saveQEMUArgs: (uuid: string, q: QEMUArgs) => request<DefineResult>('PUT', `${domainPath(uuid)}/qemu-args`, q),
  createMDev: (parent: string, type: string) => request<{ uuid: string }>('POST', '/api/v1/host/mdevs', { parent, type }),
  deleteMDev: (uuid: string) => request<void>('DELETE', `/api/v1/host/mdevs/${enc(uuid)}`),

  snapshots: async (uuid: string) =>
    (await request<{ snapshots: SnapshotInfo[] }>('GET', `${domainPath(uuid)}/snapshots`)).snapshots,
  createSnapshot: (uuid: string, name: string, description: string) =>
    request<{ name: string }>('POST', `${domainPath(uuid)}/snapshots`, { name, description }),
  revertSnapshot: (uuid: string, name: string) => request<void>('POST', `${domainPath(uuid)}/snapshots/${enc(name)}/revert`),
  deleteSnapshot: (uuid: string, name: string) => request<void>('DELETE', `${domainPath(uuid)}/snapshots/${enc(name)}`),

  settings: (uuid: string) => request<SettingsView>('GET', `${domainPath(uuid)}/settings`),
  previewSettings: (uuid: string, s: Settings) =>
    request<{ before: string; after: string }>('POST', `${domainPath(uuid)}/settings/preview`, s),
  saveSettings: (uuid: string, s: Settings) => request<SaveResult>('PUT', `${domainPath(uuid)}/settings`, s),
  resizeDisk: (uuid: string, target: string, sizeGiB: number) =>
    request<void>('POST', `${domainPath(uuid)}/disks/${enc(target)}/resize`, { sizeGiB }),
  guestInfo: (uuid: string) => request<GuestInfo>('GET', `${domainPath(uuid)}/guest`),
  setGuestPassword: (uuid: string, user: string, password: string) =>
    request<void>('POST', `${domainPath(uuid)}/guest/password`, { user, password }),
  setInterfaceBandwidth: (uuid: string, mac: string, bw: NICBandwidth) =>
    request<void>('PUT', `${domainPath(uuid)}/interfaces/${enc(mac)}/bandwidth`, bw),
  /** Moves a disk into another pool of the same host; a running machine keeps running. */
  moveDisk: (uuid: string, target: string, pool: string) =>
    request<Job>('POST', `${domainPath(uuid)}/disks/${enc(target)}/move`, { pool }),
  setDiskQoS: (uuid: string, target: string, qos: DiskIoTune) =>
    request<void>('PUT', `${domainPath(uuid)}/disks/${enc(target)}/qos`, qos),


  history: (series: string[], range: HistoryRange, host?: string) =>
    request<HistoryResult>('GET', at(host, `/api/v1/history?range=${range}&series=${series.map(enc).join(',')}`)),

  // Storage
  pools: async () => (await request<{ pools: Pool[] }>('GET', '/api/v1/storage/pools')).pools,
  createPool: (spec: PoolSpec) => request<void>('POST', '/api/v1/storage/pools', spec),
  /** The same pool on every cluster member; each host reports its own result. */
  createClusterNetwork: async (spec: NetworkSpec) =>
    (await request<{ results: { host: string; ok: boolean; error?: string }[] }>('POST', '/api/v1/cluster/networks', spec)).results,
  createClusterPool: async (spec: PoolSpec) =>
    (await request<{ results: { host: string; ok: boolean; error?: string }[] }>('POST', '/api/v1/cluster/pools', spec)).results,
  deletePool: (name: string) => request<void>('DELETE', poolPath(name)),
  poolXML: async (name: string) => (await send('GET', `${poolPath(name)}/xml`, { accept: 'application/xml' })).text(),
  savePoolXML: async (name: string, xml: string): Promise<{ restartNeeded: boolean }> =>
    (await send('PUT', `${poolPath(name)}/xml`, { body: xml, contentType: 'application/xml' })).json(),
  networkXML: async (name: string) => (await send('GET', `${netPath(name)}/xml`, { accept: 'application/xml' })).text(),
  saveNetworkXML: async (name: string, xml: string): Promise<{ restartNeeded: boolean }> =>
    (await send('PUT', `${netPath(name)}/xml`, { body: xml, contentType: 'application/xml' })).json(),
  poolAction: (name: string, action: 'start' | 'stop' | 'refresh') => request<void>('POST', `${poolPath(name)}/actions/${action}`),
  poolAutostart: (name: string, enabled: boolean) => request<void>('PUT', `${poolPath(name)}/autostart`, { enabled }),
  volumes: async (pool: string) => (await request<{ volumes: Volume[] }>('GET', `${poolPath(pool)}/volumes`)).volumes,
  createVolume: (pool: string, name: string, sizeGiB: number, format: 'qcow2' | 'raw', provisioning?: 'thin' | 'thick') =>
    request<Volume>('POST', `${poolPath(pool)}/volumes`, { name, sizeGiB, format, provisioning }),
  resizeVolume: (pool: string, name: string, sizeGiB: number) =>
    request<void>('POST', `${poolPath(pool)}/volumes/${enc(name)}/resize`, { sizeGiB }),
  deleteVolume: (pool: string, name: string) => request<void>('DELETE', `${poolPath(pool)}/volumes/${enc(name)}`),
  /** Uploads with progress (fetch cannot report upload progress). */
  upload: (pool: string, file: File, name: string, onProgress: (fraction: number) => void, signal?: AbortSignal) =>
    sendFile<Volume>(scoped(`${poolPath(pool)}/upload?name=${enc(name)}`), file, onProgress, signal),
  /** An OVA appliance: its disks are converted into the pool and the machine created (stopped). */
  importOVA: (pool: string, file: File, name: string, onProgress: (fraction: number) => void, signal?: AbortSignal) =>
    sendFile<{ uuid: string }>(scoped(`${poolPath(pool)}/import-ova?name=${enc(name)}`), file, onProgress, signal),
  /** A machine from a libvirt domain XML (name and UUID must be free on this host). */
  importXML: (xml: string) => request<{ uuid: string }>('POST', '/api/v1/domains/import', { xml }),

  // Networks
  networks: async () => (await request<{ networks: VNetwork[] }>('GET', '/api/v1/networks')).networks,
  createNetwork: (spec: NetworkSpec) => request<void>('POST', '/api/v1/networks', spec),
  deleteNetwork: (name: string) => request<void>('DELETE', netPath(name)),
  networkAction: (name: string, action: 'start' | 'stop') => request<void>('POST', `${netPath(name)}/actions/${action}`),
  networkAutostart: (name: string, enabled: boolean) => request<void>('PUT', `${netPath(name)}/autostart`, { enabled }),
  addDHCPHost: (name: string, h: DHCPHost) => request<void>('POST', `${netPath(name)}/dhcp-hosts`, h),
  deleteDHCPHost: (name: string, mac: string) => request<void>('DELETE', `${netPath(name)}/dhcp-hosts/${enc(mac)}`),
  leases: async (name: string) => (await request<{ leases: Lease[] }>('GET', `${netPath(name)}/leases`)).leases,

  // Catalog (ADR-0065)
  catalogBlueprints: async (project?: string): Promise<Blueprint[] & { blueprints: Blueprint[] }> => {
    const res = await request<{ blueprints: Blueprint[] }>('GET', `/api/v1/catalog/blueprints${project ? `?project=${enc(project)}` : ''}`)
    const list = res.blueprints as Blueprint[] & { blueprints: Blueprint[] }
    list.blueprints = res.blueprints
    return list
  },
  createBlueprint: async (bp: Partial<Blueprint>) =>
    (await request<{ blueprint: Blueprint }>('POST', '/api/v1/catalog/blueprints', bp)).blueprint,
  updateBlueprint: async (id: string, bp: Partial<Blueprint>) => {
    await request<{ blueprint: Blueprint }>('PUT', `/api/v1/catalog/blueprints/${enc(id)}`, bp)
  },
  deleteBlueprint: (id: string) => request<void>('DELETE', `/api/v1/catalog/blueprints/${enc(id)}`),
  catalogRequests: async (params: { project?: string; status?: string; requester?: string } = {}): Promise<CatalogRequest[] & { requests: CatalogRequest[] }> => {
    const p = new URLSearchParams()
    if (params.project) p.set('project', params.project)
    if (params.status) p.set('status', params.status)
    if (params.requester) p.set('requester', params.requester)
    const q = p.toString()
    const res = await request<{ requests: CatalogRequest[] }>('GET', `/api/v1/catalog/requests${q ? `?${q}` : ''}`)
    const list = res.requests as CatalogRequest[] & { requests: CatalogRequest[] }
    list.requests = res.requests
    return list
  },
  /** One request, with its lease when the machine is provisioned. */
  catalogRequest: (id: string) => request<{ request: CatalogRequest; lease?: CatalogLease }>('GET', `/api/v1/catalog/requests/${enc(id)}`),
  createCatalogRequest: (req: Partial<CatalogRequest>) => request<CatalogRequest>('POST', '/api/v1/catalog/requests', req),
  /** Approving starts the catalog-provision job (202). */
  approveCatalogRequest: async (id: string, comment?: string): Promise<Job & { jobId: string }> => {
    const j = await request<Job>('POST', `/api/v1/catalog/requests/${enc(id)}/approve`, { comment })
    return Object.assign(j, { jobId: j.id })
  },
  rejectCatalogRequest: (id: string, comment: string) =>
    request<void>('POST', `/api/v1/catalog/requests/${enc(id)}/reject`, { comment }),
  extendCatalogLease: (id: string, days: number) =>
    request<CatalogLease>('POST', `/api/v1/catalog/requests/${enc(id)}/extend`, { days }),
  /** Cancels a pending order (requester) or deletes the record (admin). */
  deleteCatalogRequest: (id: string) => request<void>('DELETE', `/api/v1/catalog/requests/${enc(id)}`),
  catalogPricing: () => request<PricingConfig>('GET', '/api/v1/catalog/pricing'),
  saveCatalogPricing: (cfg: PricingConfig) => request<void>('PUT', '/api/v1/catalog/pricing', cfg),
  catalogShowback: async (params: { year?: number; month?: number; project?: string; format?: 'json' | 'csv' | 'html' } = {}) => {
    const p = new URLSearchParams()
    if (params.year !== undefined) p.set('year', String(params.year))
    if (params.month !== undefined) p.set('month', String(params.month))
    if (params.project) p.set('project', params.project)
    if (params.format && params.format !== 'json') p.set('format', params.format)
    const q = p.toString()
    const path = `/api/v1/catalog/showback${q ? `?${q}` : ''}`
    if (params.format === 'csv' || params.format === 'html') {
      const res = await send('GET', path, { accept: params.format === 'csv' ? 'text/csv' : 'text/html' })
      return await res.text()
    }
    return await request<ShowbackReport>('GET', path)
  },

  // VMware migration (ADR-0061)
  vmwareConnection: (host?: string) => request<VMwareConnection>('GET', at(host, '/api/v1/vmware/connection')),
  vmwareSaveConnection: (c: VMwareConnection, host?: string) => request<VMwareConnection>('PUT', at(host, '/api/v1/vmware/connection'), c),
  vmwareDeleteConnection: (host?: string) => request<void>('DELETE', at(host, '/api/v1/vmware/connection')),
  vmwareTest: (c?: VMwareConnection, host?: string) => request<VMwareTestResult>('POST', at(host, '/api/v1/vmware/connection/test'), c ?? {}),
  vmwareInventory: (host?: string) => request<VMwareInventory>('GET', at(host, '/api/v1/vmware/inventory')),
  vmwarePreflight: (spec: { vmIds: string[]; pool: string }, host?: string) =>
    request<VMwarePreflightResult>('POST', at(host, '/api/v1/vmware/preflight'), spec),
  vmwareImport: (spec: VMwareImportSpec, host?: string) => request<Job>('POST', at(host, '/api/v1/vmware/import'), spec),
  vmwareWave: (id: string, host?: string) => request<VMwareWave>('GET', at(host, `/api/v1/vmware/waves/${enc(id)}`)),
  vmwareWaves: async (host?: string) => (await request<{ waves: VMwareWave[] }>('GET', at(host, '/api/v1/vmware/waves'))).waves,
  vmwareCreateWave: (spec: VMwareWaveSpec, host?: string) => request<Job>('POST', at(host, '/api/v1/vmware/waves'), spec),
  vmwareRetry: (id: string, spec?: VMwareRetrySpec, host?: string) =>
    request<Job>('POST', at(host, `/api/v1/vmware/waves/${enc(id)}/retry`), spec ?? {}),

  // Content Library (ADR-0063)
  contentItems: async (host?: string) =>
    (await request<{ items: ContentItem[] }>('GET', at(host, '/api/v1/content/items'))).items,
  contentItem: (id: string, host?: string) =>
    request<ContentItem>('GET', at(host, `/api/v1/content/items/${enc(id)}`)),
  publishContent: (req: PublishContentReq, host?: string) =>
    request<{ item: ContentItem; results?: { host: string; ok: boolean; error?: string }[] }>('POST', at(host, '/api/v1/content/items'), req),
  updateContent: (id: string, req: Partial<ContentItem>, host?: string) =>
    request<{ item: ContentItem; results?: { host: string; ok: boolean; error?: string }[] }>('PUT', at(host, `/api/v1/content/items/${enc(id)}`), req),
  deleteContent: (id: string, host?: string) =>
    request<void>('DELETE', at(host, `/api/v1/content/items/${enc(id)}`)),
  publishContentVersion: (id: string, req: PublishContentVersionReq, host?: string) =>
    request<{ item: ContentItem; version: ContentVersion; results?: { host: string; ok: boolean; error?: string }[] }>('POST', at(host, `/api/v1/content/items/${enc(id)}/versions`), req),
  deleteContentVersion: (id: string, vid: string, host?: string) =>
    request<void>('DELETE', at(host, `/api/v1/content/items/${enc(id)}/versions/${enc(vid)}`)),
  syncContent: (id: string, vid?: string, host?: string) =>
    request<Job>('POST', at(host, `/api/v1/content/items/${enc(id)}/sync`), { versionId: vid }),
  contentConfig: (host?: string) =>
    request<ContentHostConfig>('GET', at(host, '/api/v1/content/config')),
  saveContentConfig: (cfg: ContentHostConfig, host?: string) =>
    request<ContentHostConfig>('PUT', at(host, '/api/v1/content/config'), cfg),
  deployFromContent: (id: string, spec: ContentDeploySpec, host?: string) =>
    request<Job>('POST', at(host, `/api/v1/content/items/${enc(id)}/deploy`), spec),
}

/** Human message for any error thrown by api calls. */
export function errorText(e: unknown): string {
  const s = e instanceof ApiError ? e.message : String(e)
  return s.charAt(0).toUpperCase() + s.slice(1)
}
