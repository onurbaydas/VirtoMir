// Line diff (LCS) for showing what a form change does to the XML. Definitions are a few
// hundred lines, so the O(n·m) table is fine.

export interface DiffLine {
  kind: 'same' | 'add' | 'del'
  text: string
}

export function diffLines(a: string, b: string): DiffLine[] {
  const x = a.replace(/\s+$/, '').split('\n')
  const y = b.replace(/\s+$/, '').split('\n')
  const n = x.length
  const m = y.length
  const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = x[i].trim() === y[j].trim() ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (x[i].trim() === y[j].trim()) {
      out.push({ kind: 'same', text: y[j] })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) out.push({ kind: 'del', text: x[i++] })
    else out.push({ kind: 'add', text: y[j++] })
  }
  while (i < n) out.push({ kind: 'del', text: x[i++] })
  while (j < m) out.push({ kind: 'add', text: y[j++] })
  return out
}

/** Only changed lines with `context` unchanged lines around them; gaps become null. */
export function hunks(lines: DiffLine[], context = 2): (DiffLine | null)[] {
  const keep = new Array(lines.length).fill(false)
  lines.forEach((l, i) => {
    if (l.kind === 'same') return
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true
  })
  const out: (DiffLine | null)[] = []
  let gap = false
  lines.forEach((l, i) => {
    if (keep[i]) {
      out.push(l)
      gap = false
    } else if (!gap) {
      out.push(null)
      gap = true
    }
  })
  return out
}
