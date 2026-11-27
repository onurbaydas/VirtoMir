import { describe, expect, it } from 'vitest'

// The client imports the router, which reads browser globals.
;(globalThis as any).window = { addEventListener: () => {} }
;(globalThis as any).location = { pathname: '/', hash: '', search: '', origin: 'http://localhost' }

const { cidrContains, hostCount, isCIDR, isIPv4, parseRoutes, ruleText } = await import('./sdn')

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

describe('parseRoutes', () => {
  it('reads both spellings and skips blank lines', () => {
    expect(parseRoutes('192.168.50.0/24 via 10.0.1.254\n\n  10.9.0.0/16 10.0.1.253  ')).toEqual({
      routes: [
        { destinationCidr: '192.168.50.0/24', nextHopIp: '10.0.1.254' },
        { destinationCidr: '10.9.0.0/16', nextHopIp: '10.0.1.253' },
      ],
    })
    expect(parseRoutes('').routes).toEqual([])
  })
  it('names the first bad line', () => {
    expect(parseRoutes('192.168.50.0/24 via').bad).toEqual({ line: 1, text: '192.168.50.0/24 via', why: 'shape' })
    expect(parseRoutes('10.0.0.0/8 10.0.1.1\n192.168.50.1/24 via 10.0.1.254').bad).toMatchObject({ line: 2, why: 'cidr' })
    expect(parseRoutes('192.168.50.0/24 via 10.0.1.300').bad?.why).toBe('hop')
    expect(parseRoutes('10.0.0.0/8 10.0.1.1\n10.0.0.0/8 via 10.0.1.2').bad).toMatchObject({ line: 2, why: 'twice' })
  })
})
