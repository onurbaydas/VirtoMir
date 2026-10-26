// IPv4 subnet arithmetic for the network forms: is a subnet valid, does it overlap one the
// host already uses, and which 192.168.X.0/24 is still free.

export interface V4Net {
  base: number // network address as an unsigned 32-bit number
  bits: number
}

/** Parses "192.168.1.5/24" (host bits allowed) into its network; null when invalid. */
export function parseV4(cidr: string): V4Net | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr.trim())
  if (!m) return null
  const o = m.slice(1, 5).map(Number)
  const bits = Number(m[5])
  if (o.some((x) => x > 255) || bits > 32) return null
  const addr = ((o[0] << 24) | (o[1] << 16) | (o[2] << 8) | o[3]) >>> 0
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
  return { base: (addr & mask) >>> 0, bits }
}

/** True when the two networks share any address. */
export function overlaps(a: V4Net, b: V4Net): boolean {
  const bits = Math.min(a.bits, b.bits)
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
  return ((a.base & mask) >>> 0) === ((b.base & mask) >>> 0)
}

/** The first entry of `used` (CIDR strings with a label) that overlaps `cidr`, if any. */
export function clash(cidr: string, used: { cidr: string; label: string }[]): { cidr: string; label: string } | null {
  const n = parseV4(cidr)
  if (!n) return null
  for (const u of used) {
    const o = parseV4(u.cidr)
    if (o && overlaps(n, o)) return u
  }
  return null
}

/** A free 192.168.X.0/24 (X from `start`), avoiding every used network; null when none is left. */
export function freeV4(used: string[], start = 100): string | null {
  const nets = used.map(parseV4).filter((x): x is V4Net => !!x)
  for (let x = start; x < 255; x++) {
    const cand = parseV4(`192.168.${x}.0/24`)!
    if (!nets.some((u) => overlaps(cand, u))) return `192.168.${x}.0/24`
  }
  for (let x = 1; x < start; x++) {
    const cand = parseV4(`192.168.${x}.0/24`)!
    if (!nets.some((u) => overlaps(cand, u))) return `192.168.${x}.0/24`
  }
  return null
}
