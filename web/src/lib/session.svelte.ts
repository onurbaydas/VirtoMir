// Who is signed in. Any 401 from the API flips the app to the sign-in page.

import { api, setUnauthorizedHandler, type Session } from './api'

class SessionStore {
  /** null until the first check finished. */
  info = $state<Session | null>(null)
  /** Signed in, or sign-in disabled (development mode). */
  ready = $derived(this.info !== null && (this.info.mode === 'none' || !!this.info.user))

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
