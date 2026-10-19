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

/** Operations intelligence (ADR-0066). daysUntilFull -1 = not growing towards full. */
export interface Forecast {
  metric: string
  slope: number
  daysUntilFull: number
  confidence: number
  insufficientData: boolean
  seasonal: boolean
}
export interface Recommendation {
  uuid: string
  currentVcpus: number
  currentMemMiB: number
  suggestedVcpus: number
  suggestedMemMiB: number
  oversized: boolean
  undersized: boolean
  reasons: string[]
}
export interface Anomaly {
  time: string
  metric: string
  value: number
  baselineMedian: number
  z: number
}
export interface Insights {
  host: { cpu: Forecast; mem: Forecast }
  pools: { name: string; capacityBytes: number; allocationBytes: number; forecast: Forecast }[]
  machines: (Recommendation & { name: string; state: string })[]
}

export type ReportKind = 'capacity' | 'inventory' | 'backup_sla' | 'compliance'
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

export interface KSMInfo {
  supported: boolean
  active: boolean
  pagesShared: number
  pagesSharing: number
  pagesUnshared: number
  pagesVolatile: number
  savedBytes: number
  sleepMillisecs: number
  pagesToScan: number
}

export interface ZFSReplicationSpec {
  id?: string
  sourcePool: string
  sourceVolume: string
  targetHost: string
  targetPool: string
  intervalMinutes: number
  enabled: boolean
  /** Replication snapshots kept on each side (3 when 0). */
  keep?: number
}

export interface ZFSReplicationStatus {
  spec: ZFSReplicationSpec
  state: 'idle' | 'syncing' | 'failed'
  lastSync?: string
  lastSnapshot?: string
  /** Bytes the last successful pass sent. */
  bytesTransferred?: number
  error?: string
  lastAttempt?: string
}

/** Host CPUs and memory nodes, with the running machines' vCPUs on top (P5). */
export interface Topology {
  nodes: { id: number; cpus: number[]; memTotalKiB: number; memFreeKiB: number }[]
  cpus: { id: number; node: number; socket: number; core: number; online: boolean; siblings: number[]; loadPercent?: number }[]
  machines: { uuid: string; name: string; numaNodes?: string; vcpus: { cpu: number; pinned?: string; pinnedCpus?: number[] }[] }[]
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

// ---- Host network changes (ADR-0042) ---------------------------------------------------------

export interface HostIfSpec {
  kind: 'bridge' | 'bond' | 'vlan'
  name: string
  ports?: string[]
  bondMode?: string
  stp?: boolean
  parent?: string
  vlan?: number
  takeAddressFrom?: string
}

export interface HostNetPending {
  id: string
  what: string
  backend: string
  started: string
  deadline: string
}

export interface HostNetStatus {
  backend: '' | 'netplan' | 'networkmanager' | 'networkd'
  reason?: string
  pending?: HostNetPending
  created: (HostIfSpec & { backend: string; created: string })[]
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

  insights: () => api.get<Insights>('/api/v1/insights'),
  machineInsights: (uuid: string) => api.get<{ rightSize: Recommendation; anomalies: Anomaly[] }>(`/api/v1/insights/vms/${enc(uuid)}`),

  reports: async () => (await api.get<{ reports: ReportMeta[] }>('/api/v1/reports')).reports,
  generateReport: (kind: ReportKind, format: ReportFormat) => api.post<ReportMeta>('/api/v1/reports', { kind, format }),
  deleteReport: (id: string) => api.delete<void>(`/api/v1/reports/${enc(id)}`),
  /** Where to open (or, with download, save) a report. */
  reportURL: (id: string, download = false) => scoped(`/api/v1/reports/${enc(id)}${download ? '?download=1' : ''}`),
  reportSchedules: async () => (await api.get<{ schedules: ReportSchedule[] }>('/api/v1/report-schedules')).schedules,
  saveReportSchedule: (s: ReportSchedule) => api.put<ReportSchedule>('/api/v1/report-schedules', s),
  deleteReportSchedule: (id: string) => api.delete<void>(`/api/v1/report-schedules/${enc(id)}`),

  chassis: () => api.get<Chassis>('/api/v1/host/chassis'),
  ksm: () => api.get<KSMInfo>('/api/v1/host/ksm'),
  setKsm: (active: boolean, sleepMillisecs?: number, pagesToScan?: number) =>
    api.put<KSMInfo>('/api/v1/host/ksm', { active, sleepMillisecs, pagesToScan }),
  topology: () => api.get<Topology>('/api/v1/host/topology'),
  gpus: async () => (await api.get<{ gpus: GPU[] }>('/api/v1/host/gpus')).gpus,
  interfaces: async () => (await api.get<{ interfaces: HostInterface[] }>('/api/v1/host/interfaces')).interfaces,
  hostNet: () => api.get<HostNetStatus>('/api/v1/host/network'),
  createHostIf: (s: HostIfSpec) => api.post<HostNetPending>('/api/v1/host/network/interfaces', s),
  deleteHostIf: (name: string) => api.delete<HostNetPending>(`/api/v1/host/network/interfaces/${enc(name)}`),
  confirmHostNet: (id: string) => api.post<HostNetStatus>(`/api/v1/host/network/pending/${enc(id)}/confirm`),
  rollbackHostNet: (id: string) => api.post<HostNetStatus>(`/api/v1/host/network/pending/${enc(id)}/rollback`),
  /** Server-sent "packet" events (one tcpdump line each), then "end". */
  captureURL: (iface: string, filter: string, count: number, seconds: number, pcap = false) =>
    scoped(`/api/v1/host/capture?${new URLSearchParams({ iface, filter, count: String(count), seconds: String(seconds), ...(pcap ? { format: 'pcap' } : {}) })}`),

  firewall: (uuid: string) => api.get<Firewall>(`/api/v1/domains/${enc(uuid)}/firewall`),
  saveFirewall: (uuid: string, fw: Firewall) => api.put<{ liveError?: string }>(`/api/v1/domains/${enc(uuid)}/firewall`, fw),

  /** Fetches an image from a URL into a pool (background job). */
  download: (pool: string, url: string, name: string, sha256: string) =>
    api.post<Job>(`/api/v1/storage/pools/${enc(pool)}/download`, { url, name, sha256 }),

  // ZFS Asynchronous Storage Replication
  zfsReplications: async () => (await api.get<{ replications: ZFSReplicationStatus[] }>('/api/v1/storage/zfs/replications')).replications,
  createZFSReplication: (spec: ZFSReplicationSpec) => api.post<ZFSReplicationStatus>('/api/v1/storage/zfs/replications', spec),
  updateZFSReplication: (spec: ZFSReplicationSpec) => api.put<ZFSReplicationStatus>(`/api/v1/storage/zfs/replications/${enc(spec.id ?? '')}`, spec),
  deleteZFSReplication: (id: string) => api.delete<void>(`/api/v1/storage/zfs/replications/${enc(id)}`),
  syncZFSReplication: (id: string) => api.post<ZFSReplicationStatus>(`/api/v1/storage/zfs/replications/${enc(id)}/sync`),

  // DRS (Distributed Resource Scheduler) & Unified Inventory
  drsStatus: () => api.get<DRSStatus>('/api/v1/cluster/drs'),
  saveDRSConfig: (cfg: { enabled: boolean; mode: DRSMode; thresholdStdDev: number }) => api.put<void>('/api/v1/cluster/drs/config', cfg),
  addDRSRule: (rule: Partial<AffinityRule>) => api.post<AffinityRule>('/api/v1/cluster/drs/rules', rule),
  deleteDRSRule: (id: string) => api.delete<void>(`/api/v1/cluster/drs/rules/${enc(id)}`),
  applyDRSRecommendation: (id: string) => api.post<{ message: string; recommendation: DRSRecommendation }>(`/api/v1/cluster/drs/recommendations/${enc(id)}/apply`),
  /** The member DRS would put a new machine on; undefined while DRS does not place machines. */
  drsPlacement: (memoryMiB: number) => api.get<DRSPlacement | undefined>(`/api/v1/cluster/drs/placement?memoryMiB=${memoryMiB}`),
  clusterInventoryMachines: async () => {
    const res = await api.get<ClusterDomainSummary[] | { domains: ClusterDomainSummary[] }>('/api/v1/cluster/inventory/machines')
    return Array.isArray(res) ? res : (res?.domains ?? [])
  },

  // Placement Rules (ADR-0062)
  placement: () => api.get<PlacementStatus>('/api/v1/placement'),
  addPlacementRule: (rule: Partial<PlacementRule>) => api.post<PlacementRule>('/api/v1/placement/rules', rule),
  savePlacementRule: (id: string, rule: Partial<PlacementRule>) => api.put<PlacementRule>(`/api/v1/placement/rules/${enc(id)}`, rule),
  deletePlacementRule: (id: string) => api.delete<void>(`/api/v1/placement/rules/${enc(id)}`),
  setHostLabels: (hostId: string, labels: Record<string, string>) => api.put<{ labels: Record<string, string> }>(`/api/v1/placement/hosts/${enc(hostId)}/labels`, { labels }),
  placementViolations: () => api.get<PlacementViolation[]>('/api/v1/placement/violations'),
  placementCheck: (domainUuid: string, hostId: string) => api.post<PlacementCheckResult>('/api/v1/placement/check', { domainUuid, hostId }),
}

export type DRSMode = 'manual' | 'partially_automated' | 'fully_automated'

export interface DRSPlacement {
  hostId: string
  hostName: string
  cpuPct: number
  memPct: number
}

export interface AffinityRule {
  id: string
  name: string
  kind: 'vm_anti_affinity' | 'vm_affinity' | 'vm_host_affinity'
  enabled: boolean
  mandatory: boolean
  domains: string[]
  targetHost?: string
}

export interface DRSRecommendation {
  id: string
  priority: number
  domainUuid: string
  domainName: string
  sourceHostId: string
  sourceName: string
  targetHostId: string
  targetName: string
  reason: string
  expectedGain: number
  createdAt: string
}

export interface DRSHostMetric {
  id: string
  name: string
  reachable: boolean
  cpuPct: number
  memPct: number
  memUsedB: number
  memTotalB: number
  vmCount: number
}

export interface DRSStatus {
  enabled: boolean
  mode: DRSMode
  balanceScore: number
  cpuStdDev: number
  memStdDev: number
  thresholdStdDev: number
  hosts: DRSHostMetric[]
  recommendations: DRSRecommendation[]
  rules: AffinityRule[]
  lastEvaluationTime: string
}

export interface ClusterDomainSummary {
  uuid: string
  name: string
  state: string
  vcpus: number
  memoryKiB: number
  cpuPercent: number
  autostart: boolean
  hostId: string
  hostName: string
}

export type PlacementKind = 'vm_affinity' | 'vm_anti_affinity' | 'vm_host'
export type PlacementStrength = 'must' | 'should'

export interface PlacementRule {
  id: string
  name: string
  kind: 'vm_affinity' | 'vm_anti_affinity' | 'vm_host'
  enabled: boolean
  strength: 'must' | 'should'
  domains: string[]
  hosts?: string[]
  exclude?: boolean
}

export interface PlacementViolation {
  ruleId: string
  ruleName: string
  kind: 'vm_affinity' | 'vm_anti_affinity' | 'vm_host'
  hard: boolean
  domainUuids: string[]
  hostId: string
  message: string
}

export interface PlacementCheckResult {
  ok: boolean
  hard: string[]
  soft: string[]
}

export interface PlacementStatus {
  rules: PlacementRule[]
  hostLabels: Record<string, Record<string, string>>
  violations: PlacementViolation[]
}

export const PRIORITIES: Priority[] = ['critical', 'high', 'normal', 'low']

export function gib(bytes: number): string {
  return `${(bytes / 2 ** 30).toFixed(bytes >= 100 * 2 ** 30 ? 0 : 1)} GiB`
}

export function violationsByDomain(list: PlacementViolation[]): Record<string, PlacementViolation> {
  const map: Record<string, PlacementViolation> = {}
  for (const v of list) {
    for (const u of v.domainUuids) {
      if (!map[u] || (!map[u].hard && v.hard)) {
        map[u] = v
      }
    }
  }
  return map
}

export interface EvacuationCandidate {
  id: string
  freeKiB: number
  hard: string[]
  soft: string[]
}

export function rankEvacuationCandidates(candidates: EvacuationCandidate[]): EvacuationCandidate[] {
  const valid = candidates.filter((c) => c.hard.length === 0)
  valid.sort((a, b) => {
    const aSoft = a.soft.length === 0 ? 0 : 1
    const bSoft = b.soft.length === 0 ? 0 : 1
    if (aSoft !== bSoft) return aSoft - bSoft
    return b.freeKiB - a.freeKiB
  })
  return valid
}

