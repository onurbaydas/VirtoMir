// Types + calls for HA (ADR-0031), resource pools and reports (ADR-0032), machine firewalls
// (ADR-0033) and the host views (chassis, GPUs, interfaces, packet capture).
// Mirrors internal/ha, internal/respool, internal/reports, internal/hostinfo, internal/virt/firewall.go.

import { api, scoped, type Job } from './api'

const enc = encodeURIComponent

// ---- High availability ----------------------------------------------------------------------

export type FenceDriver = '' | 'ipmi' | 'redfish' | 'manual'

export interface FenceAgent {
  nodeId?: string
  driver: FenceDriver
  endpoint: string
  username?: string
  /** "********" when set; send it back unchanged to keep it. */
  password?: string
  insecureTls?: boolean
}

export type Priority = 'critical' | 'high' | 'normal' | 'low'

export interface VMPolicy {
  uuid: string
  name: string
  enabled: boolean
  priority: Priority
  restartDelaySec: number
  maxRetries: number
  targetNode?: string
}

export interface HAConfig {
  version: number
  autoFailover: boolean
  heartbeatIntervalSec: number
  failoverTimeoutSec: number
  witness?: string
  watchdog: boolean
  watchdogTimeoutSec: number
  fenceAgents: Record<string, FenceAgent>
  vmPolicies: Record<string, VMPolicy>
}

export interface HASettings {
  autoFailover: boolean
  heartbeatIntervalSec: number
  failoverTimeoutSec: number
  witness: string
  watchdog: boolean
  watchdogTimeoutSec: number
  fenceAgents: Record<string, FenceAgent>
}

export interface HAReport {
  nodeId: string
  name: string
  time: string
  quorate: boolean
  configVersion: number
  watchdogArmed: boolean
  watchdogTimeoutSec: number
  memTotalKiB: number
  memFreeKiB: number
}

export interface FenceResult {
  nodeId: string
  driver: string
  action: 'off' | 'reboot' | 'status'
  power?: 'on' | 'off' | ''
  message: string
  time: string
}

export interface HANode {
  id: string
  name: string
  self: boolean
  status: 'online' | 'suspect' | 'dead'
  lastSeen?: string
  report?: HAReport
  recovery?: '' | 'fencing' | 'waiting-confirmation' | 'waiting-watchdog' | 'waiting-auto' | 'restarting' | 'done' | 'failed'
  fence?: FenceResult
  machines: number
}

export interface HAQuorum {
  quorate: boolean
  votes: number
  totalVotes: number
  required: number
  witness?: string
  witnessReachable: boolean
  coordinator?: string
}

export interface HAMachine extends VMPolicy {
  owner?: string
  ownerName?: string
  running: boolean
  epoch: number
  updated?: string
  known: boolean
}

export interface HAEvent {
  time: string
  level: 'info' | 'warn' | 'error'
  node?: string
  machine?: string
  message: string
}

export interface HAStatus {
  selfId: string
  clustered: boolean
  quorum: HAQuorum
  nodes: HANode[]
  machines: HAMachine[]
  config: HAConfig
  watchdogArmed: boolean
  watchdogError?: string
  events: HAEvent[]
}

export interface PlanStep {
  order: number
  uuid: string
  name: string
  priority: Priority
  delaySec: number
  target?: string
  problem?: string
}

export interface Simulation {
  node: { id: string; name: string }
  quorate: boolean
  fencing: string
  steps: PlanStep[] | null
  canRecover: boolean
}

// ---- Resource pools -------------------------------------------------------------------------

export interface ResourcePool {
  id: string
  name: string
  parentId?: string
  description?: string
  cpuLimit: number
  memoryLimitMiB: number
  storageLimits: Record<string, number>
  machines: string[]
  createdAt: string
}

export interface PoolUsage {
  poolId: string
  machines: number
  vcpus: number
  memoryMiB: number
  storageBytes: Record<string, number>
}

export interface PoolView extends ResourcePool {
  usage: PoolUsage
}

// ---- Reports --------------------------------------------------------------------------------

export type ReportKind = 'capacity' | 'inventory' | 'backup_sla'
export type ReportFormat = 'html' | 'csv' | 'json'

export interface ReportMeta {
  id: string
  kind: ReportKind
  title: string
  format: ReportFormat
  generatedAt: string
  sizeBytes: number
  summary: string
  by: string
  scheduleId?: string
}

export interface ReportSchedule {
  id?: string
  name: string
  kind: ReportKind
  format: ReportFormat
  enabled: boolean
  every: 'daily' | 'weekly' | 'monthly'
  at: string
  keepDays: number
  notify: boolean
  lastRun?: string
  lastError?: string
  nextRun?: string
}

// ---- Host -----------------------------------------------------------------------------------

export interface Sensor {
  chip: string
  label: string
  kind: 'temperature' | 'fan' | 'power' | 'voltage'
  value: number
  unit: string
  max?: number
  crit?: number
}

export interface Chassis {
  vendor?: string
  product?: string
  version?: string
  serial?: string
  boardVendor?: string
  boardName?: string
  biosVendor?: string
  biosVersion?: string
  biosDate?: string
  chassisType?: string
  virtual: boolean
  os?: string
  kernel?: string
  uptimeSec?: number
  bootTime?: string
  sensors: Sensor[]
  bmc?: { driver: string; url: string }
}

export interface GPU {
  address: string
  vendorId: string
  deviceId: string
  vendor: string
  name?: string
  driver?: string
  telemetry?: '' | 'nvidia-smi' | 'amdgpu'
  utilPercent?: number
  memTotalMiB?: number
  memUsedMiB?: number
  tempC?: number
  powerW?: number
  powerLimitW?: number
  mig?: 'enabled' | 'disabled'
}

export interface HostInterface {
  name: string
  kind: 'physical' | 'bond' | 'bridge' | 'vlan' | 'tap' | 'veth' | 'loopback' | 'virtual'
  mac?: string
  mtu?: number
  state: string
  speedMbps?: number
  driver?: string
  master?: string
  members?: string[]
  bondMode?: string
  vlanId?: number
  parent?: string
  addresses?: string[]
  rxBytes: number
  txBytes: number
}

// ---- Firewall -------------------------------------------------------------------------------

export interface FirewallRule {
  direction: 'in' | 'out'
  action: 'accept' | 'drop' | 'reject'
  protocol: 'all' | 'tcp' | 'udp' | 'icmp'
  remote?: string
  ports?: string
  comment?: string
}

export interface Firewall {
  enabled: boolean
  defaultIn: 'accept' | 'drop'
  defaultOut: 'accept' | 'drop'
  antiSpoof: boolean
  rules: FirewallRule[]
  adapters?: string[]
  unsupported?: string[]
}

// ---- Calls ----------------------------------------------------------------------------------

export const ent = {
  haStatus: () => api.get<HAStatus>('/api/v1/ha/status'),
  haSettings: (s: HASettings) => api.put<HAStatus>('/api/v1/ha/settings', s),
  haSetPolicy: (p: VMPolicy) => api.put<VMPolicy>(`/api/v1/ha/vms/${enc(p.uuid)}`, p),
  haDeletePolicy: (uuid: string) => api.delete<void>(`/api/v1/ha/vms/${enc(uuid)}`),
  haFence: (node: string, action: 'status' | 'off' | 'reboot') => api.post<FenceResult>(`/api/v1/ha/nodes/${enc(node)}/fence`, { action }),
  haConfirmOff: (node: string) => api.post<void>(`/api/v1/ha/nodes/${enc(node)}/confirm-off`),
  haSimulate: (node: string) => api.post<Simulation>(`/api/v1/ha/nodes/${enc(node)}/simulate`),

  respools: async () => (await api.get<{ pools: PoolView[] }>('/api/v1/respools')).pools,
  createRespool: (p: Partial<ResourcePool>) => api.post<ResourcePool>('/api/v1/respools', p),
  updateRespool: (p: ResourcePool) => api.put<void>(`/api/v1/respools/${enc(p.id)}`, p),
  deleteRespool: (id: string) => api.delete<void>(`/api/v1/respools/${enc(id)}`),
  /** pool "" takes the machine out of every pool. */
  assignRespool: (uuid: string, pool: string) => api.put<void>(`/api/v1/domains/${enc(uuid)}/respool`, { pool }),

  reports: async () => (await api.get<{ reports: ReportMeta[] }>('/api/v1/reports')).reports,
  generateReport: (kind: ReportKind, format: ReportFormat) => api.post<ReportMeta>('/api/v1/reports', { kind, format }),
  deleteReport: (id: string) => api.delete<void>(`/api/v1/reports/${enc(id)}`),
  /** Where to open (or, with download, save) a report. */
  reportURL: (id: string, download = false) => scoped(`/api/v1/reports/${enc(id)}${download ? '?download=1' : ''}`),
  reportSchedules: async () => (await api.get<{ schedules: ReportSchedule[] }>('/api/v1/report-schedules')).schedules,
  saveReportSchedule: (s: ReportSchedule) => api.put<ReportSchedule>('/api/v1/report-schedules', s),
  deleteReportSchedule: (id: string) => api.delete<void>(`/api/v1/report-schedules/${enc(id)}`),

  chassis: () => api.get<Chassis>('/api/v1/host/chassis'),
  gpus: async () => (await api.get<{ gpus: GPU[] }>('/api/v1/host/gpus')).gpus,
  interfaces: async () => (await api.get<{ interfaces: HostInterface[] }>('/api/v1/host/interfaces')).interfaces,
  /** Server-sent "packet" events (one tcpdump line each), then "end". */
  captureURL: (iface: string, filter: string, count: number, seconds: number, pcap = false) =>
    scoped(`/api/v1/host/capture?${new URLSearchParams({ iface, filter, count: String(count), seconds: String(seconds), ...(pcap ? { format: 'pcap' } : {}) })}`),

  firewall: (uuid: string) => api.get<Firewall>(`/api/v1/domains/${enc(uuid)}/firewall`),
  saveFirewall: (uuid: string, fw: Firewall) => api.put<{ liveError?: string }>(`/api/v1/domains/${enc(uuid)}/firewall`, fw),

  /** Fetches an image from a URL into a pool (background job). */
  download: (pool: string, url: string, name: string, sha256: string) =>
    api.post<Job>(`/api/v1/storage/pools/${enc(pool)}/download`, { url, name, sha256 }),
}

export const PRIORITIES: Priority[] = ['critical', 'high', 'normal', 'low']

export function gib(bytes: number): string {
  return `${(bytes / 2 ** 30).toFixed(bytes >= 100 * 2 ** 30 ? 0 : 1)} GiB`
}
