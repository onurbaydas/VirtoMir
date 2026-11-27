// CSV files for "Export" buttons: RFC 4180 quoting, downloaded in the browser.

/** csvField quotes a value when it holds a comma, a quote or a line break. */
export const csvField = (s: string) => (/[",\r\n]/.test(s) ? '"' + s.replaceAll('"', '""') + '"' : s)

/** toCSV joins rows into CSV text, one line per row. */
export const toCSV = (rows: string[][]) => rows.map((r) => r.map(csvField).join(',')).join('\n') + '\n'

/** downloadCSV hands rows to the browser as <name>-<date>.csv. */
export function downloadCSV(name: string, rows: string[][]) {
  const blob = new Blob([toCSV(rows)], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${name}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}
