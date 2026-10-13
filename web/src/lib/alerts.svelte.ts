// Active alerts, polled in the background: drives the sidebar badge and pops a toast when
// something new starts firing.

import { api, type Alert } from './api'
import { toasts } from './toast.svelte'

class AlertStore {
  list = $state.raw<Alert[]>([])
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

  /** Another cluster member: its alerts are not news, so no toasts for the first load. */
  reset() {
    this.list = []
    this.#seen.clear()
    this.#first = true
    void this.refresh()
  }

  async refresh() {
    try {
      const list = await api.alerts()
      for (const a of list) {
        if (!a.resolved && !this.#seen.has(a.id) && !this.#first) {
          toasts.push({ kind: a.severity === 'critical' ? 'error' : 'warn', title: a.subject, detail: a.message })
        }
        this.#seen.add(a.id)
      }
      this.#first = false
      this.list = list
    } catch {
      /* the connection banner covers outages */
    }
  }
}

export const alerts = new AlertStore()
