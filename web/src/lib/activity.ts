// Turns raw API activity (method + path) into sentences people can read.

import type { ActivityEntry } from './api'

interface Rule {
  method: string
  re: RegExp
  /** $1… are the regex groups; {vm} is replaced by the machine's name. */
  text: string
}

const RULES: Rule[] = [
  { method: 'POST', re: /^\/api\/v1\/auth\/login$/, text: 'signed in' },
  { method: 'POST', re: /^\/api\/v1\/domains$/, text: 'created a machine' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/start$/, text: 'started {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/shutdown$/, text: 'shut down {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/reboot$/, text: 'rebooted {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/force-off$/, text: 'forced {vm} off' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/pause$/, text: 'paused {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/actions\/resume$/, text: 'resumed {vm}' },
  { method: 'DELETE', re: /^\/api\/v1\/domains\/([^/]+)$/, text: 'deleted {vm}' },
  { method: 'PUT', re: /^\/api\/v1\/domains\/([^/]+)\/xml$/, text: 'edited the XML of {vm}' },
  { method: 'PUT', re: /^\/api\/v1\/domains\/([^/]+)\/settings$/, text: 'changed the settings of {vm}' },
  { method: 'PUT', re: /^\/api\/v1\/domains\/([^/]+)\/autostart$/, text: 'changed autostart of {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/rename$/, text: 'renamed {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/clone$/, text: 'cloned {vm}' },
  { method: 'PUT', re: /^\/api\/v1\/domains\/([^/]+)\/media$/, text: 'changed the CD/DVD of {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/disks$/, text: 'added storage to {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/disks\/([^/]+)\/resize$/, text: 'grew disk $2 of {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/interfaces$/, text: 'added a network adapter to {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/devices\/detach$/, text: 'removed a device from {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/snapshots$/, text: 'took a snapshot of {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/snapshots\/([^/]+)\/revert$/, text: 'reverted {vm} to snapshot $2' },
  { method: 'DELETE', re: /^\/api\/v1\/domains\/([^/]+)\/snapshots\/([^/]+)$/, text: 'deleted snapshot $2 of {vm}' },
  { method: 'PUT', re: /^\/api\/v1\/domains\/([^/]+)\/template$/, text: 'changed the template status of {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/deploy$/, text: 'created a machine from template {vm}' },
  { method: 'POST', re: /^\/api\/v1\/domains\/([^/]+)\/backup$/, text: 'backed up {vm}' },
  { method: 'POST', re: /^\/api\/v1\/backups\/restore$/, text: 'restored a backup' },
  { method: 'DELETE', re: /^\/api\/v1\/backups$/, text: 'deleted a backup' },
  { method: 'PUT', re: /^\/api\/v1\/backups\/settings$/, text: 'changed the backup location' },
  { method: 'PUT', re: /^\/api\/v1\/schedules$/, text: 'changed an automatic snapshot or backup schedule' },
  { method: 'DELETE', re: /^\/api\/v1\/schedules\/([^/]+)$/, text: 'removed a schedule' },
  { method: 'POST', re: /^\/api\/v1\/schedules\/([^/]+)\/run$/, text: 'ran a schedule now' },
  { method: 'POST', re: /^\/api\/v1\/storage\/pools$/, text: 'created a storage pool' },
  { method: 'DELETE', re: /^\/api\/v1\/storage\/pools\/([^/]+)$/, text: 'removed pool $1' },
  { method: 'POST', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/actions\/(\w+)$/, text: '$2 pool $1' },
  { method: 'PUT', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/autostart$/, text: 'changed autostart of pool $1' },
  { method: 'POST', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/volumes$/, text: 'created a volume in $1' },
  { method: 'DELETE', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/volumes\/([^/]+)$/, text: 'deleted volume $2 from $1' },
  { method: 'POST', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/volumes\/([^/]+)\/resize$/, text: 'grew volume $2 in $1' },
  { method: 'POST', re: /^\/api\/v1\/storage\/pools\/([^/]+)\/upload$/, text: 'uploaded an image to $1' },
  { method: 'POST', re: /^\/api\/v1\/networks$/, text: 'created a network' },
  { method: 'DELETE', re: /^\/api\/v1\/networks\/([^/]+)$/, text: 'deleted network $1' },
  { method: 'POST', re: /^\/api\/v1\/networks\/([^/]+)\/actions\/(\w+)$/, text: '$2 network $1' },
  { method: 'PUT', re: /^\/api\/v1\/networks\/([^/]+)\/autostart$/, text: 'changed autostart of network $1' },
  { method: 'POST', re: /^\/api\/v1\/networks\/([^/]+)\/dhcp-hosts$/, text: 'reserved an address on $1' },
  { method: 'DELETE', re: /^\/api\/v1\/networks\/([^/]+)\/dhcp-hosts\/([^/]+)$/, text: 'removed the reservation for $2 on $1' },
  { method: 'POST', re: /^\/api\/v1\/tokens$/, text: 'created an API token' },
  { method: 'DELETE', re: /^\/api\/v1\/tokens\/([^/]+)$/, text: 'revoked an API token' },
]

const VERBS: Record<string, string> = { start: 'started', stop: 'stopped', refresh: 'rescanned' }

export interface Described {
  /** Text before and after the machine name (after is '' when there is no machine). */
  before: string
  after: string
  /** Machine UUID and display name, when the action is about one machine. */
  machine?: string
  machineName?: string
}

/** "started web-1" for an entry; names maps machine UUIDs to names. */
export function describe(e: ActivityEntry, names: Map<string, string>): Described {
  for (const r of RULES) {
    if (r.method !== e.method) continue
    const m = r.re.exec(e.path)
    if (!m) continue
    const groups = m.slice(1).map(decodeURIComponent)
    const machine = r.text.includes('{vm}') ? groups[0] : undefined
    let text = r.text.replace(/\$(\d)/g, (_, i) => {
      const g = groups[Number(i) - 1] ?? ''
      return VERBS[g] ?? g
    })
    if (!machine) return { before: text, after: '' }
    const [before, after = ''] = text.split('{vm}')
    return { before, after, machine, machineName: names.get(machine) ?? `a deleted machine (${machine.slice(0, 8)})` }
  }
  return { before: `${e.method} ${e.path}`, after: '' }
}
