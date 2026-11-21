// The feedback form (Tally: no account needed), opened with the setup already filled in its
// hidden fields: versions only, never host names, addresses or serial numbers. People with a
// GitHub account can use the same questions as an issue form (.github/ISSUE_TEMPLATE/quick_feedback.yml).

export const FEEDBACK_FORM = 'https://tally.so/r/db904z'
export const GITHUB_FEEDBACK_FORM = 'https://github.com/onurbaydas/VirtoMir/issues/new?template=quick_feedback.yml'

export interface FeedbackSetup {
  version?: string
  os?: string
  libvirt?: string
  qemu?: string
}

/** "VirtoMir 1.2.1 · Fedora Linux 44 · libvirt 12.0.0 · QEMU 10.2.2", leaving out what is unknown. */
export function setupLine(s: FeedbackSetup): string {
  return [
    s.version && `VirtoMir ${s.version.replace(/^v/, '')}`,
    s.os,
    s.libvirt && `libvirt ${s.libvirt}`,
    s.qemu && `QEMU ${s.qemu}`,
  ]
    .filter(Boolean)
    .join(' · ')
}

/** The form's address; source says where it was opened (app, demo). */
export function feedbackURL(s: FeedbackSetup, source: 'app' | 'demo'): string {
  const q = new URLSearchParams({ source })
  const line = setupLine(s)
  if (line) q.set('setup', line)
  return `${FEEDBACK_FORM}?${q}`
}
