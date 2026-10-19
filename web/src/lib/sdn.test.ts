import { describe, expect, it } from 'vitest'

// The client imports the router, which reads browser globals.
;(globalThis as any).window = { addEventListener: () => {} }
;(globalThis as any).location = { pathname: '/', hash: '', search: '', origin: 'http://localhost' }

const { cidrContains, hostCount, isCIDR, isIPv4, ruleText } = await import('./sdn')

describe('SDN helpers', () => {
  it('validates IPv4 networks', () => {
    expect(isCIDR('10.0.0.0/16')).toBe(true)
    expect(isCIDR('10.0.1.0/24')).toBe(true)
    expect(isCIDR('0.0.0.0/0')).toBe(true)
    expect(isCIDR('10.0.1.5/24')).toBe(false) // host bits set
    expect(isCIDR('10.0.1.0')).toBe(false)
    expect(isCIDR('10.0.1.0/33')).toBe(false)
    expect(isCIDR('256.0.0.0/8')).toBe(false)
    expect(isCIDR('10.0.1.0/30', 29)).toBe(false)
    expect(isIPv4('10.0.1.1')).toBe(true)
    expect(isIPv4('10.0.01.1')).toBe(false)
  })
  it('knows which network holds which', () => {
    expect(cidrContains('10.0.0.0/16', '10.0.1.0/24')).toBe(true)
    expect(cidrContains('10.0.0.0/16', '10.1.0.0/24')).toBe(false)
    expect(cidrContains('10.0.1.0/24', '10.0.0.0/16')).toBe(false)
    expect(hostCount('10.0.1.0/24')).toBe(254)
  })
  it('describes rules', () => {
    expect(ruleText({ direction: 'ingress', action: 'allow', priority: 100, protocol: 'tcp', portMin: 80, portMax: 443, remoteCidr: '0.0.0.0/0' })).toBe('tcp 80–443 from 0.0.0.0/0')
    expect(ruleText({ direction: 'ingress', action: 'allow', priority: 100, protocol: 'tcp', portMin: 5432, remoteGroupId: 'abc' }, () => 'web')).toBe('tcp 5432 from group web')
    expect(ruleText({ direction: 'egress', action: 'allow-related', priority: 100, protocol: 'any' })).toBe('any to anywhere')
  })
})
