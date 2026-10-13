// Types + calls for the virtomird API (/api/v1). Mirrors internal/virt and internal/live.

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
  /** Share of the domain's own vCPUs in use (0–100). */
  cpuPercent: number
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
  host: { cpuPercent: number; memTotalKiB: number; memUsedKiB: number }
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

async function send(method: string, path: string, accept = 'application/json'): Promise<Response> {
  let res: Response
  try {
    res = await fetch(path, { method, headers: { Accept: accept } })
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
    throw new ApiError(msg, res.status)
  }
  return res
}

async function request<T>(method: string, path: string): Promise<T> {
  const res = await send(method, path)
  return (res.status === 204 || res.status === 202 ? undefined : await res.json()) as T
}

const domainPath = (uuid: string) => `/api/v1/domains/${encodeURIComponent(uuid)}`

export const api = {
  host: () => request<HostInfo>('GET', '/api/v1/host'),
  domain: (uuid: string) => request<DomainDetail>('GET', domainPath(uuid)),
  /** inactive: the persistent config used on next boot instead of the live one. */
  domainXML: async (uuid: string, inactive = false) =>
    (await send('GET', `${domainPath(uuid)}/xml${inactive ? '?inactive=true' : ''}`, 'application/xml')).text(),
  action: (uuid: string, action: Action) =>
    request<void>('POST', `/api/v1/domains/${encodeURIComponent(uuid)}/actions/${action}`),
  streamURL: '/api/v1/stream',
}
