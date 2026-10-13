// Follows a background job (e.g. a clone) until it finishes and tells the user.

import { api, type Job } from './api'
import { toasts } from './toast.svelte'

export function track(job: Job, opts: { success: string; onSuccess?: (j: Job) => void }) {
  const id = toasts.push({ kind: 'info', title: job.title, detail: 'Working…', sticky: true })
  const poll = async () => {
    let j: Job
    try {
      j = await api.job(job.id)
    } catch {
      setTimeout(poll, 3000)
      return
    }
    if (j.state === 'running') {
      toasts.update(id, { detail: j.message || 'Working…' })
      setTimeout(poll, 1000)
      return
    }
    toasts.dismiss(id)
    if (j.state === 'succeeded') {
      toasts.push({ kind: 'info', title: opts.success })
      opts.onSuccess?.(j)
    } else {
      toasts.push({ kind: 'error', title: `${job.title} failed`, detail: j.message })
    }
  }
  setTimeout(poll, 800)
}
