import { describe, expect, it } from 'vitest'
import { clash, freeV4, overlaps, parseV4 } from './netcalc'

describe('netcalc', () => {
  it('parses networks and rejects nonsense', () => {
    expect(parseV4('192.168.1.5/24')).toEqual(parseV4('192.168.1.0/24'))
    expect(parseV4('10.0.0.0/8')?.bits).toBe(8)
    expect(parseV4('300.1.1.1/24')).toBeNull()
    expect(parseV4('192.168.1.0/33')).toBeNull()
    expect(parseV4('fd00::/64')).toBeNull()
  })

  it('finds overlaps in both directions', () => {
    expect(overlaps(parseV4('192.168.0.0/16')!, parseV4('192.168.122.0/24')!)).toBe(true)
    expect(overlaps(parseV4('192.168.122.0/24')!, parseV4('192.168.0.0/16')!)).toBe(true)
    expect(overlaps(parseV4('192.168.1.0/24')!, parseV4('192.168.2.0/24')!)).toBe(false)
  })

  it('names what a subnet clashes with', () => {
    const used = [
      { cidr: '192.168.1.20/24', label: 'eth0' },
      { cidr: '192.168.122.1/24', label: 'default' },
    ]
    expect(clash('192.168.1.0/24', used)?.label).toBe('eth0')
    expect(clash('192.168.100.0/24', used)).toBeNull()
  })

  it('suggests a free /24 around the host LAN', () => {
    expect(freeV4(['192.168.100.1/24', '192.168.101.0/24'])).toBe('192.168.102.0/24')
    // A /16 LAN takes every 192.168.X: nothing left.
    expect(freeV4(['192.168.0.10/16'])).toBeNull()
  })
})
