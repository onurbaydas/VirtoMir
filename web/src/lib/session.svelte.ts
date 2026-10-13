// Who is signed in. Any 401 from the API flips the app to the sign-in page.

import { api, setUnauthorizedHandler, type Role, type Session } from './api'

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 }

class SessionStore {
  /** null until the first check finished. */
  info = $state<Session | null>(null)
  /** Signed in, or sign-in disabled (development mode). */
  ready = $derived(this.info !== null && (this.info.mode === 'none' || !!this.info.user))
  role = $derived<Role>(this.info?.role ?? (this.info?.mode === 'none' ? 'admin' : 'viewer'))

  /** Whether the signed-in user's role includes `need`. */
  can(need: Role): boolean {
    return RANK[this.role] >= RANK[need]
  }

  /** Tooltip for controls the role does not allow ('' when allowed). */
  why(need: Role): string {
    return this.can(need) ? '' : `Needs the ${need} role — you are ${this.role === 'viewer' ? 'a viewer' : `an ${this.role}`}.`
  }

  constructor() {
    setUnauthorizedHandler(() => {
      if (this.info?.mode === 'pam') this.info = { mode: 'pam' }
    })
  }

  async check() {
    try {
      this.info = await api.session()
    } catch {
      // 401 means "pam, not signed in"; anything else (daemon down) is retried by the caller.
      this.info = { mode: 'pam' }
    }
  }

  async login(user: string, password: string) {
    this.info = await api.login(user, password)
  }

  async logout() {
    try {
      await api.logout()
    } finally {
      this.info = { mode: this.info?.mode ?? 'pam' }
    }
  }
}

export const session = new SessionStore()
