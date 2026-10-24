// Physical network (ADR-0075): SR-IOV, BGP through FRR, IP pools and dynamic DNS. Types
// and calls for /api/v1/physnet; mirrors internal/physnet and internal/api/physnet.go.

import { api } from './api'

const enc = encodeURIComponent

export interface VF {
  index: number
  pci: string
  netdev?: string
  mac?: string
}

export interface SRIOVDevice {
  interface: string
  pci: string
  driver: string
  totalVfs: number
  currentVfs: number
  vfs: VF[]
  persistent: boolean
}

export interface BGPNeighbor {
  address: string
  remoteAs: number
  description?: string
  password?: string
  bfd?: boolean
}

export interface BGPConfig {
  enabled: boolean
  localAs: number
  routerId: string
  neighbors: BGPNeighbor[]
  announce: string[]
  evpn: boolean
  appliedAs?: number
}

export interface BGPSession {
  address: string
  remoteAs: number
  description?: string
  state: string
  uptime?: string
  prefixesReceived: number
  prefixesSent: number
  families: string[]
}

export interface BGPView {
  config: BGPConfig
  frr: { installed: boolean; version?: string; bgpd: boolean }
  sessions: BGPSession[]
  error?: string
}

export interface IPAllocation {
  ip: string
  mac?: string
  vmUuid?: string
  vmName?: string
  hostname?: string
  host?: string
  created: string
}

export interface IPPool {
  id: string
  name: string
  description?: string
  network?: string
  cidr: string
  gateway?: string
  rangeStart: string
  rangeEnd: string
  dnsServers?: string[]
  domain?: string
  reserved?: string[]
  allocations: IPAllocation[]
  created?: string
  updated?: string
}

export interface DNSConfig {
  enabled: boolean
  server: string
  zone: string
  reverseZones?: string[]
  keyName?: string
  algorithm?: string
  secret?: string
  ttl?: number
}

export const physnet = {
  sriov: async () => (await api.get<{ devices: SRIOVDevice[] }>('/api/v1/physnet/sriov')).devices,
  setVFs: (iface: string, vfs: number, persistent: boolean) => api.put<void>(`/api/v1/physnet/sriov/${enc(iface)}`, { vfs, persistent }),
  bgp: () => api.get<BGPView>('/api/v1/physnet/bgp'),
  setBGP: (c: BGPConfig) => api.put<void>('/api/v1/physnet/bgp', c),
  pools: async () => (await api.get<{ pools: IPPool[] }>('/api/v1/physnet/ip-pools')).pools,
  createPool: (p: Partial<IPPool>) => api.post<IPPool>('/api/v1/physnet/ip-pools', p),
  updatePool: (id: string, p: Partial<IPPool>) => api.put<IPPool>(`/api/v1/physnet/ip-pools/${enc(id)}`, p),
  deletePool: (id: string) => api.delete<void>(`/api/v1/physnet/ip-pools/${enc(id)}`),
  check: (id: string, ip: string) => api.post<{ free: boolean; reason: string }>(`/api/v1/physnet/ip-pools/${enc(id)}/check`, { ip }),
  reserve: (id: string, name: string, ip?: string) => api.post<IPAllocation>(`/api/v1/physnet/ip-pools/${enc(id)}/allocations`, { name, ip }),
  release: (id: string, ip: string) => api.delete<void>(`/api/v1/physnet/ip-pools/${enc(id)}/allocations/${enc(ip)}`),
  dns: () => api.get<{ config: DNSConfig; hasSecret: boolean }>('/api/v1/physnet/dns'),
  setDNS: (c: DNSConfig) => api.put<void>('/api/v1/physnet/dns', c),
  testDNS: () => api.post<{ ok: boolean; message: string }>('/api/v1/physnet/dns/test', {}),
}

const v4 = (s: string): number | null => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s.trim())
  if (!m || m.slice(1).some((x) => Number(x) > 255)) return null
  return m.slice(1).reduce((a, x) => a * 256 + Number(x), 0)
}

/** How many addresses an IPv4 range holds (0 when it is not one). */
export function rangeSize(start: string, end: string): number {
  const a = v4(start)
  const b = v4(end)
  return a === null || b === null || b < a ? 0 : b - a + 1
}

/** Share of a pool's range in use, in percent. */
export function poolUse(p: IPPool): number {
  const n = rangeSize(p.rangeStart, p.rangeEnd)
  return n ? Math.min(100, ((p.allocations.length + (p.reserved?.length ?? 0)) / n) * 100) : 0
}
