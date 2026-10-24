// Storage efficiency, tiers and NVMe/TCP (ADR-0074): types and calls. Mirrors
// internal/storageeff and internal/api/storageeff.go.

import { api, type Job } from './api'
import type { RBDPool } from './ceph'

const enc = encodeURIComponent

export type Tier = 'nvme' | 'ssd' | 'hdd'
export const TIERS: Tier[] = ['nvme', 'ssd', 'hdd']

export interface ZFSEfficiency {
  dataset: string
  compression: string
  compressRatio: number
  dedup: string
  dedupRatio: number
  usedBytes: number
  logicalUsedBytes: number
  savedBytes: number
}

export interface PoolEfficiency {
  pool: string
  kind: 'zfs' | 'ceph' | 'none'
  zfs?: ZFSEfficiency
  ceph?: RBDPool
  note?: string
}

export interface ZFSSettings {
  compression?: string
  dedup?: string
  acknowledgeDedupRam?: boolean
}

export interface NVMeTarget {
  portal: string
  port?: number
  nqn: string
}

export interface NVMeNamespace {
  device: string
  nsid: number
  sizeBytes: number
  model: string
  serial: string
}

export interface NVMeSubsystem {
  nqn: string
  transport: string
  address?: string
  state: string
  namespaces: NVMeNamespace[]
  managed: boolean
}

export const eff = {
  pool: (pool: string) => api.get<PoolEfficiency>(`/api/v1/storage/pools/${enc(pool)}/efficiency`),
  setZFS: (pool: string, s: ZFSSettings) => api.put<ZFSEfficiency>(`/api/v1/storage/pools/${enc(pool)}/zfs`, s),
  setCephCompression: (pool: string, mode: string, algorithm: string) => api.put<void>(`/api/v1/ceph/pools/${enc(pool)}/compression`, { mode, algorithm }),
  sparsify: (uuid: string, target: string) => api.post<Job>(`/api/v1/domains/${enc(uuid)}/disks/${enc(target)}/sparsify`),
  moveToTier: (uuid: string, target: string, tier: Tier) => api.post<Job>(`/api/v1/domains/${enc(uuid)}/disks/${enc(target)}/tier`, { tier }),
  tiers: async () => (await api.get<{ tiers: Record<string, Tier> }>('/api/v1/storage/tiers')).tiers,
  setTier: (pool: string, tier: Tier | '') => api.put<void>(`/api/v1/storage/tiers/${enc(pool)}`, { tier }),
  nvme: async () => (await api.get<{ subsystems: NVMeSubsystem[] }>('/api/v1/storage/nvme')).subsystems,
  discover: async (portal: string, port?: number) => (await api.post<{ targets: NVMeTarget[] }>('/api/v1/storage/nvme/discover', { portal, port })).targets,
  connect: (t: NVMeTarget) => api.post<void>('/api/v1/storage/nvme/connect', t),
  disconnect: (nqn: string) => api.post<void>('/api/v1/storage/nvme/disconnect', { nqn }),
}

/** "1.48×" */
export const ratioText = (r: number) => `${(r || 1).toFixed(2)}×`

/** What a share of the stored data compression saved, in percent (0 when nothing). */
export function savedPct(stored: number, saved: number): number {
  const before = stored + saved
  return before > 0 ? (saved / before) * 100 : 0
}
