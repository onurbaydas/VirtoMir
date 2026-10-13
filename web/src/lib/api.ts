// Types + calls for the virtomird API (/api/v1). Mirrors internal/virt and internal/live.

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

export type Action = 'start' | 'shutdown' | 'reboot' | 'force-off' | 'pause' | 'resume'

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
const LOCAL = ['/api/v1/auth/', '/api/v1/cluster', '/api/v1/version', '/api/v1/tokens']

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
  try {
    res = await fetch(path, { method, headers, body })
  } catch {
    throw new ApiError('VirtoMir is not reachable', 0)
  }
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

export type Role = 'viewer' | 'operator' | 'admin'

export interface Session {
  mode: 'pam' | 'none'
  user?: string
  role?: Role
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
export type AlertKind = 'host.cpu' | 'host.mem' | 'vm.cpu' | 'vm.crashed' | 'vm.down' | 'pool.free' | 'libvirt.down'

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

export interface CreateSpec {
  name: string
  os: 'linux' | 'windows' | 'other'
  vcpus: number
  memoryMiB: number
  firmware: 'uefi' | 'bios'
  secureBoot: boolean
  tpm: boolean
  pool: string
  diskGiB: number
  diskPath?: string
  /** Cloud image to copy and grow to diskGiB. */
  baseImage?: string
  cloudInit?: CloudInit
  iso?: string
  network?: NICSpec
  start: boolean
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
  usedBy: string[]
  dhcpHosts: DHCPHost[]
}

export interface NetworkSpec {
  name: string
  mode: 'nat' | 'route' | 'isolated' | 'bridge'
  subnet?: string
  dhcp: boolean
  bridge?: string
  /** Bridge mode: the host bridge is Open vSwitch; vlan tags every machine on it. */
  ovs?: boolean
  vlan?: number
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
  /** dir (default), netfs (NFS host:export mounted at path), iscsi (portal host + target IQN), rbd (Ceph). */
  type?: 'dir' | 'netfs' | 'iscsi' | 'rbd'
  host?: string
  export?: string
  target?: string
  monitors?: string[]
  cephPool?: string
  cephUser?: string
  cephKey?: string
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
}

export interface MyQuota {
  unlimited: boolean
  rule?: QuotaRule
  used?: { machines: number; vcpus: number; memoryMiB: number }
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

export const api = {
  streamURL: () => scoped('/api/v1/stream'),

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

  alerts: async (host?: string) => (await request<{ alerts: Alert[] }>('GET', at(host, '/api/v1/alerts'))).alerts,
  ackAlert: (id: string, host?: string) => request<void>('POST', at(host, `/api/v1/alerts/${enc(id)}/ack`)),
  alertConfig: () => request<AlertConfig>('GET', '/api/v1/alerts/config'),
  saveAlertConfig: (c: AlertConfig, host?: string) => request<AlertConfig>('PUT', at(host, '/api/v1/alerts/config'), c),
  testChannel: (ch: AlertChannel) => request<void>('POST', '/api/v1/alerts/test', ch),

  backups: async (domain?: string, host?: string) =>
    request<{ backups: BackupInfo[]; dir: string }>('GET', at(host, `/api/v1/backups${domain ? `?domain=${enc(domain)}` : ''}`)),
  backupNow: (uuid: string) => request<Job>('POST', `${domainPath(uuid)}/backup`),
  deleteBackup: (id: string, host?: string) => request<void>('DELETE', at(host, `/api/v1/backups?id=${enc(id)}`)),
  restoreBackup: (id: string, name: string, pool: string, host?: string) =>
    request<Job>('POST', at(host, '/api/v1/backups/restore'), { id, name, pool }),
  backupSettings: () => request<{ backupDir: string }>('GET', '/api/v1/backups/settings'),
  saveBackupSettings: (backupDir: string, host?: string) =>
    request<{ backupDir: string }>('PUT', at(host, '/api/v1/backups/settings'), { backupDir }),
  schedules: async (domain?: string) =>
    (await request<{ schedules: ScheduleInfo[] }>('GET', `/api/v1/schedules${domain ? `?domain=${enc(domain)}` : ''}`)).schedules,
  saveSchedule: (s: ScheduleInfo) => request<ScheduleInfo>('PUT', '/api/v1/schedules', s),
  deleteSchedule: (id: string) => request<void>('DELETE', `/api/v1/schedules/${enc(id)}`),
  runSchedule: (id: string) => request<void>('POST', `/api/v1/schedules/${enc(id)}/run`),

  job: (id: string, host?: string) => request<Job>('GET', at(host, `/api/v1/jobs/${enc(id)}`)),

  // Machines
  domain: (uuid: string) => request<DomainDetail>('GET', domainPath(uuid)),
  /** inactive: the persistent config used on next boot instead of the live one. */
  domainXML: async (uuid: string, inactive = false) =>
    (await send('GET', `${domainPath(uuid)}/xml${inactive ? '?inactive=true' : ''}`, { accept: 'application/xml' })).text(),
  saveXML: async (uuid: string, xml: string): Promise<DefineResult> =>
    (await send('PUT', `${domainPath(uuid)}/xml`, { body: xml, contentType: 'application/xml' })).json(),
  action: (uuid: string, action: Action) => request<void>('POST', `${domainPath(uuid)}/actions/${action}`),
  create: (spec: CreateSpec) => request<{ uuid: string; startError?: string }>('POST', '/api/v1/domains', spec),
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
  detach: (uuid: string, ref: { kind: 'disk' | 'interface' | 'hostdev-pci' | 'hostdev-usb'; target?: string; mac?: string }) =>
    request<void>('POST', `${domainPath(uuid)}/devices/detach`, ref),

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
  poolAction: (name: string, action: 'start' | 'stop' | 'refresh') => request<void>('POST', `${poolPath(name)}/actions/${action}`),
  poolAutostart: (name: string, enabled: boolean) => request<void>('PUT', `${poolPath(name)}/autostart`, { enabled }),
  volumes: async (pool: string) => (await request<{ volumes: Volume[] }>('GET', `${poolPath(pool)}/volumes`)).volumes,
  createVolume: (pool: string, name: string, sizeGiB: number, format: 'qcow2' | 'raw') =>
    request<Volume>('POST', `${poolPath(pool)}/volumes`, { name, sizeGiB, format }),
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
}

/** Human message for any error thrown by api calls. */
export function errorText(e: unknown): string {
  const s = e instanceof ApiError ? e.message : String(e)
  return s.charAt(0).toUpperCase() + s.slice(1)
}
