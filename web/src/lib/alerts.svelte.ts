// Active alerts, polled in the background: drives the sidebar badge and pops a toast when
// something new starts firing. In a cluster it collects every member's alerts (each host
// evaluates its own rules), tagged with the host they came from.

import { api, type Alert } from './api'
import { cluster } from './cluster.svelte'
import { toasts } from './toast.svelte'

export interface HostAlert extends Alert {
  /** Cluster member the alert lives on (undefined on a standalone host). */
  hostId?: string
  hostName?: string
  /** Unique across hosts. */
  key: string
}

class AlertStore {
  list = $state.raw<HostAlert[]>([])
  active = $derived(this.list.filter((a) => !a.resolved))
  /** Active and not yet acknowledged: the number on the badge. */
  open = $derived(this.active.filter((a) => !a.ackedBy).length)
  critical = $derived(this.active.some((a) => a.severity === 'critical' && !a.ackedBy))

  #timer: ReturnType<typeof setInterval> | undefined
  #seen = new Set<string>()
  #first = true

  start() {
    if (this.#timer) return
    void this.refresh()
    this.#timer = setInterval(() => void this.refresh(), 15_000)
  }

  stop() {
    clearInterval(this.#timer)
    this.#timer = undefined
  }

  /** Starts over without toasting everything that is already known. */
  reset() {
    this.list = []
    this.#seen.clear()
    this.#first = true
    void this.refresh()
  }

  async #fetch(): Promise<HostAlert[]> {
    if (!cluster.clustered) return (await api.alerts()).map((a) => ({ ...a, key: a.id }))
    const parts = await Promise.all(
      cluster.members
        .filter((m) => m.reachable)
        .map(async (m) => {
          try {
            return (await api.alerts(m.id)).map((a) => ({ ...a, hostId: m.id, hostName: m.name, key: `${m.id}:${a.id}` }))
          } catch {
            return []
          }
        }),
    )
    return parts.flat().sort((a, b) => Date.parse(b.started) - Date.parse(a.started))
  }

  async refresh() {
    try {
      const list = await this.#fetch()
      for (const a of list) {
        if (!a.resolved && !this.#seen.has(a.key) && !this.#first) {
          toasts.push({
            kind: a.severity === 'critical' ? 'error' : 'warn',
            title: a.hostName ? `${a.hostName}: ${a.subject}` : a.subject,
            detail: a.message,
          })
        }
        this.#seen.add(a.key)
      }
      this.#first = false
      this.list = list
    } catch {
      /* the connection banner covers outages */
    }
  }
}

export const alerts = new AlertStore()
