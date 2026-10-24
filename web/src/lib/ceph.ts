// Hyper-converged Ceph and storage policies (ADR-0073): types and calls for /api/v1/ceph
// and /api/v1/storage/policies. Mirrors internal/ceph and internal/api/ceph.go. The Ceph
// calls go to the member that manages the cluster (host), policies to any member.

import { api, onHost, type Job } from './api'

const enc = encodeURIComponent

export interface ClusterConfig {
  fsid: string
  bootstrapHost: string
  monEndpoints: string[]
  clientUser: string
  sshPublicKey: string
  maintenanceHosts: string[] | null
  noout: boolean
  created: string
  updated: string
}

export interface HealthCheck {
  name: string
  severity: 'HEALTH_WARN' | 'HEALTH_ERR'
  summary: string
  detail?: string[]
}

export interface PoolCapacity {
  id: number
  name: string
  storedBytes: number
  objects: number
  percentUsed: number
  maxAvailBytes: number
}

export interface OSDNode {
  id: number
  name: string
  type: string
  status?: 'up' | 'down'
  in: boolean
  weight: number
  deviceClass?: string
  children?: number[]
}

export interface Telemetry {
  fsid: string
  health: { status: 'HEALTH_OK' | 'HEALTH_WARN' | 'HEALTH_ERR'; checks: HealthCheck[] }
  daemons: { mons: number; quorum: string[]; mgrUp: boolean; osds: number; osdsUp: number; osdsIn: number; noout: boolean }
  capacity: { totalBytes: number; usedBytes: number; availBytes: number; percentUsed: number; pools: PoolCapacity[] }
  osdTree: OSDNode[]
  pgs: { total: number; states: Record<string, number> }
  collected: string
}

export interface CephStatus {
  installed: boolean
  cephadm: boolean
  managed: boolean
  managedBy?: { id: string; name: string; fsid: string }
  config?: ClusterConfig
  telemetry?: Telemetry
  error?: string
}

export interface DiskInfo {
  path: string
  name: string
  sizeBytes: number
  rotational: boolean
  deviceType: 'ssd' | 'nvme' | 'hdd'
  mountPoint?: string
  fsType?: string
  partitions?: string[]
  empty: boolean
  refusalReason?: string
}

export interface CephHost {
  hostname: string
  ip: string
  labels: string[]
  status: 'online' | 'offline' | 'maintenance'
  memberId?: string
}

export interface BootstrapSpec {
  monIp: string
  clusterNetwork?: string
  initialOsdDisk?: string
  singleHostDefaults: boolean
}

export interface PoolSpec {
  name: string
  type: 'replicated' | 'erasure'
  replicaSize?: number
  ecK?: number
  ecM?: number
  deviceClass?: '' | 'ssd' | 'hdd' | 'nvme'
  pgNum?: number
  libvirtPool?: string
}

export interface RBDPool {
  id: number
  name: string
  type: 'replicated' | 'erasure'
  size: number
  minSize: number
  ecProfile?: string
  dataPool?: string
  storedBytes: number
  percentUsed: number
  maxAvailBytes: number
  libvirtPool?: string
  /** BlueStore compression of the pool holding the data (ADR-0074). */
  compressionMode: 'none' | 'passive' | 'aggressive' | 'force'
  compressionAlgorithm?: string
  compressedBytes: number
  compressedFromBytes: number
}

export interface StoragePolicy {
  id: string
  name: string
  description?: string
  pool: string
  default: boolean
  created?: string
  updated?: string
}

export interface HostResult {
  host: string
  ok: boolean
  error?: string
}

/** The Ceph calls, on member host (undefined: the member being shown). */
export function cephOn(host?: string) {
  const p = (path: string) => (host ? onHost(host, path) : path)
  return {
    status: () => api.get<CephStatus>(p('/api/v1/ceph/status')),
    disks: async () => (await api.get<{ disks: DiskInfo[] }>(p('/api/v1/ceph/disks'))).disks,
    hosts: async () => (await api.get<{ hosts: CephHost[] }>(p('/api/v1/ceph/hosts'))).hosts,
    bootstrap: (spec: BootstrapSpec) => api.post<Job>(p('/api/v1/ceph/bootstrap'), spec),
    adopt: () => api.post<ClusterConfig>(p('/api/v1/ceph/adopt'), {}),
    addHost: (member: string) => api.post<{ hostname: string; ip: string }>(p('/api/v1/ceph/hosts'), { member }),
    addOSD: (hostname: string, disk: string) => api.post<Job>(p('/api/v1/ceph/osds'), { hostname, disk }),
    setNoout: (enabled: boolean) => api.put<void>(p('/api/v1/ceph/noout'), { enabled }),
    maintenance: (action: 'enter' | 'exit', hostname: string) => api.post<void>(p(`/api/v1/ceph/maintenance/${action}`), { hostname }),
    pools: async () => (await api.get<{ pools: RBDPool[] }>(p('/api/v1/ceph/pools'))).pools,
    createPool: (spec: PoolSpec) => api.post<{ name: string; results?: HostResult[] }>(p('/api/v1/ceph/pools'), spec),
    deletePool: (name: string) => api.delete<void>(p(`/api/v1/ceph/pools/${enc(name)}`)),
    setCompression: (name: string, mode: string, algorithm: string) => api.put<void>(p(`/api/v1/ceph/pools/${enc(name)}/compression`), { mode, algorithm }),
  }
}

export const policies = {
  list: async () => (await api.get<{ policies: StoragePolicy[] }>('/api/v1/storage/policies')).policies,
  create: (p: StoragePolicy) => api.post<StoragePolicy>('/api/v1/storage/policies', p),
  update: (id: string, p: StoragePolicy) => api.put<StoragePolicy>(`/api/v1/storage/policies/${enc(id)}`, p),
  delete: (id: string) => api.delete<void>(`/api/v1/storage/policies/${enc(id)}`),
}

/** The CRUSH tree as nested nodes: hosts with their OSDs under each root. */
export function crushTree(nodes: OSDNode[]): { node: OSDNode; children: { node: OSDNode; children: OSDNode[] }[] }[] {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const kids = (n: OSDNode) => (n.children ?? []).map((id) => byId.get(id)).filter((x): x is OSDNode => !!x).sort((a, b) => a.name.localeCompare(b.name, 'en'))
  return nodes
    .filter((n) => n.type === 'root')
    .map((root) => ({ node: root, children: kids(root).map((h) => ({ node: h, children: kids(h) })) }))
}

/** A pool's protection in short: "3×" (copies) or "EC 4+2". Ceph sets an erasure-coded
 *  pool's min_size to k+1 and its size to k+m. */
export function protection(p: RBDPool): string {
  if (p.type === 'replicated') return `${p.size}×`
  const k = p.minSize - 1
  return k > 0 && k < p.size ? `EC ${k}+${p.size - k}` : `EC (${p.size})`
}

/** Usable share of raw space a pool spec gives: 1/size for copies, k/(k+m) for erasure coding. */
export function efficiency(spec: Pick<PoolSpec, 'type' | 'replicaSize' | 'ecK' | 'ecM'>): number {
  if (spec.type === 'erasure') {
    const k = spec.ecK ?? 4
    const m = spec.ecM ?? 2
    return k / (k + m)
  }
  return 1 / (spec.replicaSize ?? 3)
}
