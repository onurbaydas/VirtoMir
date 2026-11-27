import { describe, expect, it } from 'vitest'
import { compareURL, gitRef } from './versions'

describe('versions', () => {
  it('turns versions into git refs', () => {
    expect(gitRef('v1.1.1')).toBe('v1.1.1')
    expect(gitRef('v1.2.0-rc.1')).toBe('v1.2.0-rc.1')
    expect(gitRef('v1.1.1-29-g0248c1c')).toBe('0248c1c')
    expect(gitRef('v1.2.0-rc.1-3-gabcdef0')).toBe('abcdef0')
    expect(gitRef('v1.1.1-29-g0248c1c-dirty')).toBe('')
    expect(gitRef('dev')).toBe('')
    expect(gitRef('1.1.1; rm -rf /')).toBe('')
  })
  it('links the comparison', () => {
    expect(compareURL('onurbaydas/VirtoMir', 'v1.1.1-29-g0248c1c', 'v1.1.1-40-g9a8b7c6')).toBe('https://github.com/onurbaydas/VirtoMir/compare/0248c1c...9a8b7c6')
    expect(compareURL('onurbaydas/VirtoMir', 'v1.1.0', 'v1.1.1')).toBe('https://github.com/onurbaydas/VirtoMir/compare/v1.1.0...v1.1.1')
    expect(compareURL('onurbaydas/VirtoMir', 'dev', 'v1.1.1')).toBe('')
    expect(compareURL('', 'v1.1.0', 'v1.1.1')).toBe('')
  })
})
