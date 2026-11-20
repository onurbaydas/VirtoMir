import { describe, expect, it } from 'vitest'
import { FEEDBACK_FORM, feedbackURL, setupLine } from './feedback'

describe('feedback', () => {
  it('names the versions it knows, in order', () => {
    expect(setupLine({ version: 'v1.2.1', os: 'Fedora Linux 44', libvirt: '12.0.0', qemu: '10.2.2' })).toBe(
      'VirtoMir 1.2.1 · Fedora Linux 44 · libvirt 12.0.0 · QEMU 10.2.2',
    )
    expect(setupLine({ libvirt: '10.0.0' })).toBe('libvirt 10.0.0')
  })

  it('fills the setup field of the form, or opens it empty', () => {
    expect(feedbackURL({})).toBe(FEEDBACK_FORM)
    const u = new URL(feedbackURL({ version: '1.2.1', os: 'Ubuntu 24.04 LTS' }))
    expect(u.searchParams.get('template')).toBe('quick_feedback.yml')
    expect(u.searchParams.get('setup')).toBe('VirtoMir 1.2.1 · Ubuntu 24.04 LTS')
  })
})
