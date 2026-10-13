const KiB_PER_MiB = 1024
const KiB_PER_GiB = 1024 * 1024
const KiB_PER_TiB = 1024 * 1024 * 1024

function trim(n: number, digits: number): string {
  return n.toFixed(digits).replace(/\.0+$/, '')
}

/** 262144 → "256 MiB", 8137556 → "7.8 GiB". Binary units, like libvirt. */
export function formatKiB(kib: number): string {
  if (kib >= KiB_PER_TiB) return `${trim(kib / KiB_PER_TiB, 1)} TiB`
  if (kib >= KiB_PER_GiB) return `${trim(kib / KiB_PER_GiB, 1)} GiB`
  if (kib >= KiB_PER_MiB) return `${Math.round(kib / KiB_PER_MiB)} MiB`
  return `${kib} KiB`
}

export function formatBytes(b: number): string {
  return b < 1024 ? `${b} B` : formatKiB(Math.round(b / 1024))
}

/** Throughput, binary units like the rest of VirtoMir: 1536 → "1.5 KiB/s". */
export function formatBps(b: number): string {
  if (b < 1024) return `${Math.round(b)} B/s`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let v = b / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 ? trim(v, 1) : Math.round(v)} ${units[i]}/s`
}

/** Small rates (IOPS, packets/s): one decimal below 10, whole numbers above. */
export function formatRate(n: number): string {
  if (n === 0) return '0'
  if (n < 10) return trim(n, 1)
  if (n < 10_000) return Math.round(n).toString()
  return `${trim(n / 1000, 1)}k`
}

/** Below 10 % one decimal ("3.4 %"), otherwise whole numbers ("42 %"). */
export function formatPercent(p: number): string {
  return p < 10 ? p.toFixed(1) : Math.round(p).toString()
}

export function formatMHz(mhz: number): string {
  return mhz >= 1000 ? `${trim(mhz / 1000, 2)} GHz` : `${mhz} MHz`
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

/** "5 s ago", "3 min ago" for connection-loss messages. */
export function ago(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s} s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  return `${Math.round(m / 60)} h ago`
}
