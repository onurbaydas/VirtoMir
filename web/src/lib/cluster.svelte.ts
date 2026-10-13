// Cluster members (ADR-0020), polled in the background while signed in. Which member the
// pages are about lives in the router (/hosts/<id>/…); this store knows who exists.

import { api, type ClusterInfo, type ClusterMember } from './api'
import { router } from './router.svelte'

class ClusterStore {
  info = $state.raw<ClusterInfo | null>(null)
  members = $derived(this.info?.members ?? [])
  clustered = $derived(this.members.length > 1)
  /** The member the pages show: the one in the URL, or the host serving the UI. */
  current = $derived<ClusterMember | undefined>(
    this.members.find((m) => (router.host ? m.id === router.host : m.self)),
  )

  #timer: ReturnType<typeof setInterval> | undefined

  start() {
    if (this.#timer) return
    void this.refresh()
    this.#timer = setInterval(() => void this.refresh(), 20_000)
  }

  stop() {
    clearInterval(this.#timer)
    this.#timer = undefined
  }

  async refresh() {
    try {
      this.info = await api.cluster()
    } catch {
      /* the connection banner covers outages */
    }
  }

  /**
   * Runs fn on every reachable member (e.g. the same setting everywhere) and says per host
   * whether it worked. On a standalone host it runs once, for this host.
   */
  async everywhere(fn: (host: string | undefined) => Promise<unknown>): Promise<{ host: string; error?: string }[]> {
    if (!this.clustered) {
      await fn(undefined)
      return [{ host: this.current?.name ?? 'this host' }]
    }
    return Promise.all(
      this.members
        .filter((m) => m.reachable)
        .map(async (m) => {
          try {
            await fn(m.id)
            return { host: m.name }
          } catch (e) {
            return { host: m.name, error: e instanceof Error ? e.message : String(e) }
          }
        }),
    )
  }

  /** Shows the pages for member id (the serving host drops the /hosts prefix). */
  open(id: string) {
    const m = this.members.find((x) => x.id === id)
    router.switchHost(!m || m.self ? null : id)
  }
}

export const cluster = new ClusterStore()
