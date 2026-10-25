// Bare-metal onboarding (ADR-0077): hosts VirtoMir installs over the network. Types and
// calls for /api/v1/baremetal; mirrors internal/baremetal and internal/api/baremetal.go.

import { api } from './api'

const enc = encodeURIComponent

export interface BMC {
  driver: '' | 'redfish' | 'ipmi'
  endpoint?: string
  username?: string
  /** Write-only: empty keeps the stored one. */
  password?: string
  insecureTls?: boolean
}

export interface StaticNet {
  address: string
  gateway?: string
  dns?: string[]
}

export interface BMEvent {
  at: string
  message: string
  error?: boolean
}

export interface BMHost {
  id: string
  name: string
  mac: string
  bmc: BMC
  disk?: string
  static?: StaticNet
  sshKeys?: string[]
  state: 'registered' | 'installing' | 'installed' | 'joining' | 'joined' | 'failed'
  message?: string
  events: BMEvent[]
  created: string
  updated: string
}

export interface HostInput {
  name: string
  mac: string
  bmc: BMC
  disk?: string
  static?: StaticNet
  sshKeys?: string[]
  /** Of the user virtomir; empty keeps the stored one. */
  password?: string
}

export interface NetbootSettings {
  enabled: boolean
  address: string
  port: number
  mode: 'external' | 'proxy'
  interface?: string
  mirror: string
  repo: string
}

export interface BMView {
  enabled: boolean
  settings: NetbootSettings
  hosts: BMHost[]
  status: { installerReady: boolean; listening: boolean; error?: string; ipmitool: boolean }
}

export const baremetal = {
  get: () => api.get<BMView>('/api/v1/baremetal'),
  setSettings: (s: NetbootSettings) => api.put<void>('/api/v1/baremetal/settings', s),
  fetchInstaller: () => api.post<void>('/api/v1/baremetal/installer', {}),
  add: (h: HostInput) => api.post<BMHost>('/api/v1/baremetal/hosts', h),
  update: (id: string, h: HostInput) => api.put<BMHost>(`/api/v1/baremetal/hosts/${enc(id)}`, h),
  delete: (id: string) => api.delete<void>(`/api/v1/baremetal/hosts/${enc(id)}`),
  install: (id: string) => api.post<BMHost>(`/api/v1/baremetal/hosts/${enc(id)}/install`, {}),
  cancel: (id: string) => api.post<BMHost>(`/api/v1/baremetal/hosts/${enc(id)}/cancel`, {}),
  power: (id: string, action: 'status' | 'off' | 'reboot' | 'pxe') =>
    api.post<{ power?: string; message: string }>(`/api/v1/baremetal/hosts/${enc(id)}/power`, { action }),
}

/** Something happens to the host now: the page follows it. */
export const moving = (h: BMHost) => ['installing', 'installed', 'joining'].includes(h.state)

/** Why a host form cannot be sent yet ('' = it can). */
export function hostProblem(h: HostInput, isNew: boolean): string {
  if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(h.name)) return 'name'
  if (!/^([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}$/.test(h.mac.trim())) return 'mac'
  if (isNew && !h.password && !(h.sshKeys ?? []).length) return 'access'
  if (h.password && h.password.length < 8) return 'password'
  if (h.bmc.driver && !h.bmc.endpoint) return 'bmc'
  return ''
}
