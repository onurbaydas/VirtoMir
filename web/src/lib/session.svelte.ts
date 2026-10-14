// Who is signed in. Any 401 from the API flips the app to the sign-in page.

import { api, setUnauthorizedHandler, type Domain, type MyQuota, type Role, type Session } from './api'

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 }

class SessionStore {
  /** null until the first check finished. */
  info = $state<Session | null>(null)
  /** Signed in, or sign-in disabled (development mode). */
  ready = $derived(this.info !== null && (this.info.mode === 'none' || !!this.info.user))
  role = $derived<Role>(this.info?.role ?? (this.info?.mode === 'none' ? 'admin' : 'viewer'))
  /** The user's quota (ADR-0022): operators with one may create and manage their own machines. */
  quota = $state<MyQuota | null>(null)
  /** May create machines: admins, and operators with a quota. */
  canCreate = $derived(this.can('admin') || (this.role === 'operator' && !!this.quota?.rule))

  /** May change settings, rename and delete d: admins, or the operator who owns it. */
  canManage(d: Pick<Domain, 'owner'> | null | undefined): boolean {
    return this.can('admin') || (this.canCreate && !!d?.owner && d.owner === this.info?.user)
  }

  /** Tooltip when canManage is false. */
  whyManage(d: Pick<Domain, 'owner'> | null | undefined): string {
    if (this.canManage(d)) return ''
    return this.canCreate ? 'Only its owner or an admin may change this machine.' : this.why('admin')
  }

  async loadQuota() {
    try {
      this.quota = await api.myQuota()
    } catch {
      this.quota = null
    }
  }

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

  /** Signs in; with two-factor sign-in on, returns the pending sign-in that waits for a code. */
  async login(user: string, password: string): Promise<string | null> {
    const res = await api.login(user, password)
    if (res.needCode && res.pending) return res.pending
    this.info = res
    return null
  }

  async loginCode(pending: string, code: string) {
    this.info = await api.loginCode(pending, code)
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
