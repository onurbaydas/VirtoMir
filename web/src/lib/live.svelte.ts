// Live state: the snapshot stream from virtomird, a short metric history for charts,
// and in-flight VM actions.

import { api, ApiError, type Action, type Domain, type DomainState, type HostInfo, type Snapshot } from './api'
import { toasts } from './toast.svelte'

/** Points kept for live charts (1 per second → one minute). */
export const HISTORY = 60
/** Shared x axis for every chart. */
export const XS = Array.from({ length: HISTORY }, (_, i) => i)

type Series = (number | null)[]
const emptySeries = (): Series => Array<number | null>(HISTORY).fill(null)
function push(s: Series, v: number | null) {
  s.shift()
  s.push(v)
}

export interface IOSeries {
  diskRead: Series
  diskWrite: Series
  netRx: Series
  netTx: Series
}

function sum<T>(xs: T[] | undefined, f: (x: T) => number): number {
  return (xs ?? []).reduce((n, x) => n + f(x), 0)
}

/** Labels for actions: button text and the in-progress text. */
export const ACTIONS: Record<Action, { label: string; progress: string }> = {
  start: { label: 'Start', progress: 'Starting…' },
  shutdown: { label: 'Shut down', progress: 'Shutting down…' },
  reboot: { label: 'Reboot', progress: 'Rebooting…' },
  'force-off': { label: 'Force off', progress: 'Powering off…' },
  pause: { label: 'Pause', progress: 'Pausing…' },
  resume: { label: 'Resume', progress: 'Resuming…' },
}

/** State an action is finished at; reboot has no visible end state. */
const TARGET: Partial<Record<Action, DomainState[]>> = {
  start: ['running', 'blocked'],
  shutdown: ['shutoff'],
  'force-off': ['shutoff'],
  pause: ['paused'],
  resume: ['running', 'blocked'],
}

/** A guest that ignores ACPI shutdown for this long gets a "force off?" nudge. */
const SHUTDOWN_NUDGE_MS = 45_000
const REBOOT_SHOW_MS = 4_000

interface Pending {
  action: Action
  since: number
  nudged?: boolean
}

class Live {
  /** EventSource is open (virtomird reachable). */
  streamOpen = $state(false)
  snapshot = $state.raw<Snapshot | null>(null)
  host = $state.raw<HostInfo | null>(null)
  /** Time of the last snapshot received (ms). */
  lastUpdate = $state(0)
  /** Bumped on every snapshot; charts redraw on it. */
  tick = $state(0)
  pending = $state<Record<string, Pending>>({})

  readonly hostCpu = emptySeries()
  readonly hostMem = emptySeries()
  readonly #domCpu = new Map<string, Series>()
  readonly #domIO = new Map<string, IOSeries>()

  #es: EventSource | null = null
  #lastTime = 0

  domainCpu(uuid: string): Series {
    let s = this.#domCpu.get(uuid)
    if (!s) {
      s = emptySeries()
      this.#domCpu.set(uuid, s)
    }
    return s
  }

  /** Disk and network throughput history (bytes/s, summed over devices). */
  domainIO(uuid: string): IOSeries {
    let s = this.#domIO.get(uuid)
    if (!s) {
      s = { diskRead: emptySeries(), diskWrite: emptySeries(), netRx: emptySeries(), netTx: emptySeries() }
      this.#domIO.set(uuid, s)
    }
    return s
  }

  start() {
    if (this.#es) return
    const es = new EventSource(api.streamURL)
    this.#es = es
    es.onopen = () => {
      this.streamOpen = true
      void this.loadHost()
    }
    es.onerror = () => (this.streamOpen = false) // EventSource retries by itself
    es.addEventListener('snapshot', (e) => this.#onSnapshot(JSON.parse((e as MessageEvent).data)))
  }

  async loadHost() {
    try {
      this.host = await api.host()
    } catch {
      /* shown via the connection banner; retried on the next reconnect */
    }
  }

  #onSnapshot(s: Snapshot) {
    // Each (re)connect replays the server's backlog; skip what we already have.
    const t = Date.parse(s.time)
    if (t <= this.#lastTime) return
    this.#lastTime = t

    const wasConnected = this.snapshot?.connected
    s.domains.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }))
    this.snapshot = s
    this.lastUpdate = Date.now()
    if (s.connected && wasConnected === false) void this.loadHost()

    if (s.connected) {
      push(this.hostCpu, s.host.cpuPercent)
      push(this.hostMem, s.host.memTotalKiB ? (s.host.memUsedKiB / s.host.memTotalKiB) * 100 : null)
    } else {
      push(this.hostCpu, null)
      push(this.hostMem, null)
    }

    const seen = new Set<string>()
    for (const d of s.domains) {
      seen.add(d.uuid)
      push(this.domainCpu(d.uuid), isActive(d.state) ? d.cpuPercent : null)
      const io = this.domainIO(d.uuid)
      const on = isActive(d.state) && d.disks !== undefined
      push(io.diskRead, on ? sum(d.disks, (x) => x.readBps) : null)
      push(io.diskWrite, on ? sum(d.disks, (x) => x.writeBps) : null)
      const netOn = isActive(d.state) && d.nets !== undefined
      push(io.netRx, netOn ? sum(d.nets, (x) => x.rxBps) : null)
      push(io.netTx, netOn ? sum(d.nets, (x) => x.txBps) : null)
    }
    for (const id of this.#domCpu.keys()) if (!seen.has(id)) this.#domCpu.delete(id)
    for (const id of this.#domIO.keys()) if (!seen.has(id)) this.#domIO.delete(id)

    this.#settlePending(s.domains)
    this.tick++
  }

  #settlePending(domains: Domain[]) {
    const now = Date.now()
    for (const [uuid, p] of Object.entries(this.pending)) {
      const d = domains.find((x) => x.uuid === uuid)
      const target = TARGET[p.action]
      if (!d || (target && target.includes(d.state)) || (p.action === 'reboot' && now - p.since > REBOOT_SHOW_MS)) {
        delete this.pending[uuid]
        continue
      }
      if (p.action === 'shutdown' && !p.nudged && now - p.since > SHUTDOWN_NUDGE_MS) {
        p.nudged = true
        toasts.push({
          kind: 'warn',
          title: `${d.name} is still running`,
          detail: 'The guest did not react to the shutdown request.',
          action: { label: 'Force off', danger: true, run: () => this.act(d, 'force-off') },
        })
      }
    }
  }

  async act(d: Domain, action: Action) {
    this.pending[d.uuid] = { action, since: Date.now() }
    try {
      await api.action(d.uuid, action)
    } catch (e) {
      delete this.pending[d.uuid]
      toasts.push({
        kind: 'error',
        title: `${ACTIONS[action].label} failed for ${d.name}`,
        detail: e instanceof ApiError ? e.message : String(e),
      })
    }
  }
}

export function isActive(s: DomainState): boolean {
  return s === 'running' || s === 'blocked' || s === 'shutting-down'
}

export const live = new Live()
