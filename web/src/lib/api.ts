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

async function request<T>(method: string, path: string): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, { method, headers: { Accept: 'application/json' } })
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
  return (res.status === 204 || res.status === 202 ? undefined : await res.json()) as T
}

export const api = {
  host: () => request<HostInfo>('GET', '/api/v1/host'),
  action: (uuid: string, action: Action) =>
    request<void>('POST', `/api/v1/domains/${encodeURIComponent(uuid)}/actions/${action}`),
  streamURL: '/api/v1/stream',
}
