// The quick feedback form on GitHub (.github/ISSUE_TEMPLATE/quick_feedback.yml), opened with
// the setup already filled in: versions only, never host names, addresses or serial numbers.

export const FEEDBACK_FORM = 'https://github.com/onurbaydas/VirtoMir/issues/new?template=quick_feedback.yml'

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

export function feedbackURL(s: FeedbackSetup): string {
  const line = setupLine(s)
  return line ? `${FEEDBACK_FORM}&setup=${encodeURIComponent(line)}` : FEEDBACK_FORM
}
