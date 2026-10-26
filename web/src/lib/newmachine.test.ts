import { describe, expect, it } from 'vitest'
import { createBlocker, type CreateInput } from './newmachine'

const ok: CreateInput = {
  kind: 'vm',
  name: 'web-1',
  takenNames: ['db-1'],
  vcpus: 2,
  memGiB: 2,
  source: 'iso',
  withDisk: true,
  iso: '/var/lib/libvirt/images/debian.iso',
  baseImage: '',
  existingDisk: '',
  ciUser: 'admin',
  ciPassword: '',
  ciKeys: [],
  containersAvailable: true,
  rootPassword: '',
}
const blocker = (over: Partial<CreateInput>) => createBlocker({ ...ok, ...over })

describe('createBlocker', () => {
  it('lets a complete machine through', () => {
    expect(createBlocker(ok)).toBe('')
  })

  it('names the first problem, in order', () => {
    expect(blocker({ name: '  ' })).toMatch(/name/)
    expect(blocker({ name: '-bad' })).toMatch(/letters, digits/)
    expect(blocker({ name: 'db-1' })).toMatch(/already exists/)
    expect(blocker({ vcpus: 0 })).toMatch(/vCPU/)
    expect(blocker({ memGiB: Number.NaN })).toMatch(/vCPU/)
  })

  it('needs something to boot from', () => {
    expect(blocker({ withDisk: false, iso: '' })).toMatch(/disk or installation media/)
    expect(blocker({ withDisk: true, iso: '' })).toBe('') // empty disk: allowed, the wizard only warns
    expect(blocker({ source: 'cloud', baseImage: '' })).toMatch(/cloud image/)
    expect(blocker({ source: 'disk', existingDisk: '' })).toMatch(/disk image/)
  })

  it('checks cloud-init sign-in', () => {
    const cloud = { source: 'cloud' as const, baseImage: '/img/debian.qcow2' }
    expect(blocker({ ...cloud, ciUser: 'Admin' })).toMatch(/lowercase/)
    expect(blocker({ ...cloud })).toMatch(/password or an SSH key/)
    expect(blocker({ ...cloud, ciKeys: ['ssh-ed25519 AAAA'] })).toBe('')
  })

  it('checks containers', () => {
    expect(blocker({ kind: 'container', containersAvailable: false })).toMatch(/not available/)
    expect(blocker({ kind: 'container', rootPassword: 'short' })).toMatch(/8 characters/)
    expect(blocker({ kind: 'container', rootPassword: 'long enough' })).toBe('')
  })
})
