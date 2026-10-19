// The demo's pretend virtomird: answers /api/v1 requests from the in-memory world. Reads come
// from world.ts; the common changes (power actions, snapshots, new machines, clones, rename,
// delete, schedules, alerts) are applied to it, everything else answers "done" without effect.

import type {
  Blueprint,
  CatalogRequest,
  LCMPlan,
  ContentHostConfig,
  ContentItem,
  ContentVersion,
  CreateSpec,
  DeploySpec,
  DomainState,
  Job,
  PricingConfig,
  Project,
  ScheduleInfo,
  ShowbackReport,
  SnapshotInfo,
  VMwareConnection,
  VMwareInventory,
  VMwareWave,
} from '../api'
import type { PlacementRule, PlacementViolation } from '../enterprise'
import * as w from './world'

export interface Reply {
  status: number
  body?: string
  type?: string
}

const json = (v: unknown, status = 200): Reply => ({ status, body: JSON.stringify(v), type: 'application/json' })
const xml = (s: string): Reply => ({ status: 200, body: s, type: 'application/xml; charset=utf-8' })
const none = (): Reply => ({ status: 204 })
const notFound = (what = 'no such API endpoint'): Reply => json({ error: what }, 404)
const DEMO_ONLY = 'Not available in the demo: this needs a real host.'

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()

// ---- Catalog (ADR-0065) helpers -------------------------------------------------------------

/** Really create the ordered machine + lease (used by instant orders and by the approve job). */
function provision(r: CatalogRequest, bp?: Blueprint) {
  const b = bp ?? w.catalogBlueprints.find((x) => x.id === r.blueprintId)
  if (!b) {
    r.status = 'failed'
    r.errorMessage = `the blueprint ${r.blueprintId} was deleted before this order could be provisioned`
    return
  }
  r.status = 'provisioning'
  const m = addMachine(w.SELF.id, {
    name: r.vmName,
    os: 'ubuntu',
    state: 'running',
    vcpus: r.vcpus,
    memoryMiB: r.memoryMiB,
    diskGiB: r.diskGiB,
    profile: b.sourceType === 'image' ? 'db' : 'web',
    network: r.network || 'default',
    project: r.projectId,
  })
  r.machineUuid = m.uuid
  w.catalogLeases.push({
    requestId: r.id,
    machineUuid: m.uuid,
    projectId: r.projectId,
    expiry: new Date(Date.now() + (r.leaseDays || b.defaultLeaseDays || 30) * 86400e3).toISOString(),
    state: 'active',
    reminderSent: false,
    lastStateChange: new Date().toISOString(),
  })
  r.status = 'active'
}

const MONTH_HOURS = 732

/** A stable 0…1 fraction per machine uuid, so demo showback numbers don't jump between renders. */
function uuidFraction(uuid: string): number {
  let h = 0
  for (const ch of uuid) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return (h % 1000) / 1000
}

/** Rough per-machine usage estimate: running machines all month, idle ones a fraction. */
function runtimeHours(m: w.DemoMachine): number {
  const f = uuidFraction(m.uuid)
  return m.state === 'running' || m.state === 'blocked' || m.state === 'paused'
    ? MONTH_HOURS * (0.55 + 0.4 * f)
    : MONTH_HOURS * 0.2 * (0.4 + f)
}

const round2 = (v: number) => Math.round(v * 100) / 100

/** Escape untrusted text before it goes into the exported HTML (XSS). */
const escapeHtml = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')

/** One CSV cell: quoted RFC 4180 style, and a value that would start a spreadsheet formula
 *  (= + - @ tab CR) is prefixed with an apostrophe so it can only be read as text. */
const csvCell = (v: string | number) => {
  let s = String(v)
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`
  return `"${s.replace(/"/g, '""')}"`
}

/** Password-type answers never leave the API: every request response carries them masked,
 *  exactly like the real host renders its other secrets back to the UI. */
function maskedRequest(r: CatalogRequest): CatalogRequest {
  const fields = w.catalogBlueprints.find((x) => x.id === r.blueprintId)?.cloudInitFields ?? []
  if (!r.fieldValues || !fields.some((f) => f.type === 'password' && r.fieldValues![f.key] !== undefined)) return r
  const values: Record<string, string> = {}
  for (const [k, v] of Object.entries(r.fieldValues)) values[k] = fields.find((x) => x.key === k)?.type === 'password' ? '••••••••' : v
  return { ...r, fieldValues: values }
}

/** The checks every stored blueprint passes, so ordering can always resolve a size preset. */
function validateBlueprint(next: Blueprint): string | null {
  if (!next?.name?.trim()) return 'a blueprint needs a name'
  if (!next?.presets?.length) return 'a blueprint needs at least one size preset'
  if (next.presets.some((p) => !p?.id || !p?.name?.trim() || !(p.vcpus > 0) || !(p.memoryMiB > 0) || !(p.diskGiB > 0)))
    return 'every preset needs a name, vCPUs, memory and disk'
  if (next.defaultPresetId && !next.presets.some((p) => p.id === next.defaultPresetId))
    return 'the default preset must be one of the presets'
  if (next.maxLeaseDays && next.defaultLeaseDays && next.defaultLeaseDays > next.maxLeaseDays)
    return 'default lease days cannot exceed the maximum lease days'
  return null
}

/** The monthly showback report, computed from the demo world. */
function showback(q: URLSearchParams): Reply {
  const year = Number(q.get('year')) || new Date().getFullYear()
  const month = Number(q.get('month')) || new Date().getMonth() + 1
  const onlyProject = q.get('project') || ''
  const pc = w.pricing

  const machines = w.machines.filter((m) => !m.template && (!onlyProject || m.project === onlyProject))
  const byProject = new Map<string, w.DemoMachine[]>()
  for (const m of machines) {
    const list = byProject.get(m.project ?? '') ?? []
    list.push(m)
    byProject.set(m.project ?? '', list)
  }

  const projects = [...byProject.entries()].map(([pid, list]) => {
    const ms = list.map((m) => {
      const hours = runtimeHours(m)
      const vcpuCost = round2(hours * m.vcpus * pc.vcpuHour)
      const ramCost = round2(hours * (m.memoryMiB / 1024) * pc.ramGiBHour)
      const diskCost = round2(m.diskGiB * pc.diskGiBMonth)
      return {
        uuid: m.uuid,
        name: m.name,
        projectId: pid,
        vcpus: m.vcpus,
        memoryMiB: m.memoryMiB,
        diskGiB: m.diskGiB,
        state: m.state,
        runtimeHours: Math.round(hours),
        vcpuCost,
        ramCost,
        diskCost,
        totalCost: round2(vcpuCost + ramCost + diskCost),
      }
    })
    const sum = (pick: (x: (typeof ms)[number]) => number) => round2(ms.reduce((acc, x) => acc + pick(x), 0))
    return {
      projectId: pid,
      projectName: w.projects.find((p) => p.id === pid)?.name ?? (pid || 'No project'),
      machineCount: ms.length,
      totalVcpus: ms.reduce((acc, x) => acc + x.vcpus, 0),
      totalMemoryMiB: ms.reduce((acc, x) => acc + x.memoryMiB, 0),
      totalDiskGiB: sum((x) => x.diskGiB),
      vcpuCost: sum((x) => x.vcpuCost),
      ramCost: sum((x) => x.ramCost),
      diskCost: sum((x) => x.diskCost),
      totalCost: sum((x) => x.totalCost),
      machines: ms,
    }
  })
  const rep: ShowbackReport = {
    period: `${year}-${String(month).padStart(2, '0')}`,
    year,
    month,
    generatedAt: new Date().toISOString(),
    currency: pc.currency,
    pricing: pc,
    totalCost: round2(projects.reduce((acc, p) => acc + p.totalCost, 0)),
    totalMachines: machines.length,
    projects,
  }

  const format = (q.get('format') || 'json').toLowerCase()
  // Money formatted with the configured pricing currency symbol, not a fixed USD locale.
  const money = (v: number) => `${escapeHtml(pc.currency)}${v.toFixed(2)}`
  if (format === 'csv') {
    const lines = ['project,machine,project name,vcpus,memory MiB,disk GiB,state,runtime hours,vCPU cost,ram cost,disk cost,total cost']
    for (const p of rep.projects) for (const m of p.machines) lines.push([p.projectId, m.name, p.projectName, m.vcpus, m.memoryMiB, m.diskGiB, m.state, m.runtimeHours, m.vcpuCost, m.ramCost, m.diskCost, m.totalCost].map(csvCell).join(','))
    const csv = lines.join('\n')
    return { status: 200, body: csv, type: 'text/csv; charset=utf-8' }
  }
  if (format === 'html') {
    const rows = rep.projects
      .map((p) => `<tr><td>${escapeHtml(p.projectName)}</td><td>${p.machineCount}</td><td>${money(p.totalCost)}</td></tr>`)
      .join('')
    const html = `<!doctype html><html><head><meta charset="utf-8"><title>Showback ${escapeHtml(rep.period)}</title></head>
<body style="font:14px system-ui;margin:2rem"><h1>Showback ${escapeHtml(rep.period)}</h1><p>${rep.totalMachines} machines · total ${money(rep.totalCost)}</p>
<table border="1" cellpadding="6" style="border-collapse:collapse"><tr><th>Project</th><th>Machines</th><th>Cost</th></tr>${rows}</table></body></html>`
    return { status: 200, body: html, type: 'text/html; charset=utf-8' }
  }
  return json(rep)
}

// ---- Background jobs ------------------------------------------------------------------------

const jobs = new Map<string, Job>()
let jobSeq = 0
let reqSeq = 0

/** A job that finishes after ms, running done() at the end (e.g. adding the cloned machine). */
function job(kind: string, title: string, ms: number, done?: () => string | void): Job {
  const j: Job = { id: `job-${++jobSeq}`, kind, title, state: 'running', message: 'Working…', started: new Date().toISOString() }
  jobs.set(j.id, j)
  setTimeout(() => {
    const result = done?.()
    Object.assign(j, { state: 'succeeded', message: 'Done', finished: new Date().toISOString(), result: result || undefined })
  }, ms)
  return j
}

// ---- Placement rules & fault domains (ADR-0062) ---------------------------------------------

export let demoPlacementRules: PlacementRule[] = [
  {
    id: 'a1b2c3d4e5f60001',
    name: 'Separate web frontends',
    kind: 'vm_anti_affinity',
    enabled: true,
    strength: 'must',
    domains: ['4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01', '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e02'],
  },
  {
    id: 'a1b2c3d4e5f60002',
    name: 'Keep ERP off hv-01',
    kind: 'vm_host',
    enabled: true,
    strength: 'must',
    exclude: true,
    hosts: [w.HOSTS[0].id],
    domains: ['9c8d7e6f-5a4b-4c3d-8e2f-9a8b7c6d5e03'],
  },
  {
    id: 'a1b2c3d4e5f60003',
    name: 'Prefer databases on hv-02',
    kind: 'vm_host',
    enabled: true,
    strength: 'should',
    exclude: false,
    hosts: [w.HOSTS[1].id],
    domains: ['4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03'],
  },
]

export let demoHostLabels: Record<string, Record<string, string>> = {
  [w.HOSTS[0].id]: { rack: 'r1', zone: 'zone-a' },
  [w.HOSTS[1].id]: { rack: 'r1', zone: 'zone-a' },
  [w.HOSTS[2].id]: { rack: 'r2', zone: 'zone-b' },
}

export function getDemoViolations(): PlacementViolation[] {
  const violations: PlacementViolation[] = []
  const runningVMs = w.machines.filter((m) => m.state === 'running' || m.state === 'blocked' || m.state === 'shutting-down')
  const vmsByUUID = new Map(runningVMs.map((m) => [m.uuid, m]))

  for (const r of demoPlacementRules) {
    if (!r.enabled) continue
    const hard = r.strength === 'must'

    switch (r.kind) {
      case 'vm_anti_affinity': {
        const hostVMs = new Map<string, string[]>()
        for (const u of r.domains) {
          const m = vmsByUUID.get(u)
          if (m) {
            const list = hostVMs.get(m.host) || []
            list.push(u)
            hostVMs.set(m.host, list)
          }
        }
        for (const [hostId, vms] of hostVMs.entries()) {
          if (vms.length > 1) {
            violations.push({
              ruleId: r.id,
              ruleName: r.name,
              kind: r.kind,
              hard,
              domainUuids: vms,
              hostId,
              message: `anti-affinity rule "${r.name}" violated: multiple VMs (${vms.join(', ')}) are running on host ${hostId}`,
            })
          }
        }
        break
      }
      case 'vm_affinity': {
        const running = r.domains.filter((u) => vmsByUUID.has(u))
        const hosts = new Set(running.map((u) => vmsByUUID.get(u)!.host))
        if (hosts.size > 1) {
          violations.push({
            ruleId: r.id,
            ruleName: r.name,
            kind: r.kind,
            hard,
            domainUuids: running,
            hostId: '',
            message: `affinity rule "${r.name}" violated: VMs (${running.join(', ')}) are spread across multiple hosts (${[...hosts].join(', ')})`,
          })
        }
        break
      }
      case 'vm_host': {
        for (const u of r.domains) {
          const m = vmsByUUID.get(u)
          if (!m) continue
          if (r.exclude) {
            if (r.hosts?.includes(m.host)) {
              violations.push({
                ruleId: r.id,
                ruleName: r.name,
                kind: r.kind,
                hard,
                domainUuids: [u],
                hostId: m.host,
                message: `vm-host rule "${r.name}" violated: VM ${u} is running on excluded host ${m.host}`,
              })
            }
          } else {
            if (r.hosts && !r.hosts.includes(m.host)) {
              violations.push({
                ruleId: r.id,
                ruleName: r.name,
                kind: r.kind,
                hard,
                domainUuids: [u],
                hostId: m.host,
                message: `vm-host rule "${r.name}" violated: VM ${u} is on host ${m.host}, which is not in allowed hosts (${r.hosts.join(', ')})`,
              })
            }
          }
        }
        break
      }
    }
  }

  return violations
}

export function demoPlacementCheck(domainUuid: string, targetHost: string): { ok: boolean; hard: string[]; soft: string[] } {
  const hard: string[] = []
  const soft: string[] = []
  const runningVMs = w.machines.filter((m) => m.state === 'running' || m.state === 'blocked' || m.state === 'shutting-down')
  const vmsByUUID = new Map(runningVMs.map((m) => [m.uuid, m]))

  for (const r of demoPlacementRules) {
    if (!r.enabled || !r.domains.includes(domainUuid)) continue
    const isHard = r.strength === 'must'

    switch (r.kind) {
      case 'vm_anti_affinity': {
        const conflicting: string[] = []
        for (const u of r.domains) {
          if (u !== domainUuid && vmsByUUID.get(u)?.host === targetHost) {
            conflicting.push(u)
          }
        }
        if (conflicting.length > 0) {
          const msg = `anti-affinity rule "${r.name}": placing ${domainUuid} on host ${targetHost} conflicts with ${conflicting.join(', ')} already on that host`
          if (isHard) hard.push(msg)
          else soft.push(msg)
        }
        break
      }
      case 'vm_affinity': {
        const others = r.domains.filter((u) => u !== domainUuid && vmsByUUID.has(u))
        const otherHosts = new Set(others.map((u) => vmsByUUID.get(u)!.host).filter((h) => h !== targetHost))
        if (otherHosts.size > 0) {
          const msg = `affinity rule "${r.name}": placing ${domainUuid} on host ${targetHost} separates it from other hosts (${[...otherHosts].join(', ')})`
          if (isHard) hard.push(msg)
          else soft.push(msg)
        }
        break
      }
      case 'vm_host': {
        if (r.exclude) {
          if (r.hosts?.includes(targetHost)) {
            const msg = `vm-host rule "${r.name}": host ${targetHost} is excluded for VM ${domainUuid}`
            if (isHard) hard.push(msg)
            else soft.push(msg)
          }
        } else {
          if (r.hosts && !r.hosts.includes(targetHost)) {
            const msg = `vm-host rule "${r.name}": host ${targetHost} is not in allowed hosts (${r.hosts?.join(', ')}) for VM ${domainUuid}`
            if (isHard) hard.push(msg)
            else soft.push(msg)
          }
        }
        break
      }
    }
  }

  return { ok: hard.length === 0, hard, soft }
}

// ---- VMware demo state ----------------------------------------------------------------------

let demoConnection: VMwareConnection = {
  url: 'https://vcenter.demo.internal',
  username: 'administrator@vsphere.local',
  password: '••••••••',
  thumbprint: 'AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD',
  insecure: true,
}

const demoInventory: VMwareInventory = {
  datacenters: [{ name: 'Datacenter-Frankfurt' }, { name: 'Datacenter-Dublin' }],
  hosts: [
    { name: 'esx-fra-01.corp', datacenter: 'Datacenter-Frankfurt' },
    { name: 'esx-dub-01.corp', datacenter: 'Datacenter-Dublin' },
  ],
  vms: [
    {
      id: 'vm-101',
      name: 'web-frontend',
      datacenter: 'Datacenter-Frankfurt',
      host: 'esx-fra-01.corp',
      powerState: 'off',
      vcpus: 2,
      memoryMiB: 4096,
      guestOs: 'Ubuntu Linux (64-bit)',
      disks: [{ datastore: 'datastore1', capacityBytes: 25 * 1024 ** 3 }],
      nics: [{ network: 'VM Network', mac: '00:50:56:01:01:01' }],
    },
    {
      id: 'vm-102',
      name: 'api-backend',
      datacenter: 'Datacenter-Frankfurt',
      host: 'esx-fra-01.corp',
      powerState: 'off',
      vcpus: 4,
      memoryMiB: 8192,
      guestOs: 'Debian GNU/Linux 12 (64-bit)',
      disks: [{ datastore: 'datastore1', capacityBytes: 40 * 1024 ** 3 }],
      nics: [{ network: 'prod-web', mac: '00:50:56:01:01:02' }],
    },
    {
      id: 'vm-103',
      name: 'db-primary',
      datacenter: 'Datacenter-Frankfurt',
      host: 'esx-fra-01.corp',
      powerState: 'on',
      vcpus: 8,
      memoryMiB: 16384,
      guestOs: 'Red Hat Enterprise Linux 9 (64-bit)',
      disks: [{ datastore: 'datastore2', capacityBytes: 100 * 1024 ** 3 }],
      nics: [{ network: 'prod-web', mac: '00:50:56:01:01:03' }],
    },
    {
      id: 'vm-104',
      name: 'win-dc-01',
      datacenter: 'Datacenter-Dublin',
      host: 'esx-dub-01.corp',
      powerState: 'off',
      vcpus: 4,
      memoryMiB: 8192,
      guestOs: 'Microsoft Windows Server 2022 (64-bit)',
      disks: [{ datastore: 'datastore3', capacityBytes: 50 * 1024 ** 3 }],
      nics: [{ network: 'VM Network', mac: '00:50:56:02:01:01' }],
    },
    {
      id: 'vm-105',
      name: 'monitoring-node',
      datacenter: 'Datacenter-Dublin',
      host: 'esx-dub-01.corp',
      powerState: 'suspended',
      vcpus: 2,
      memoryMiB: 4096,
      guestOs: 'CentOS 7 (64-bit)',
      disks: [{ datastore: 'datastore3', capacityBytes: 20 * 1024 ** 3 }],
      nics: [{ network: 'VM Network', mac: '00:50:56:02:01:02' }],
    },
  ],
}

let demoWaves: VMwareWave[] = []

// ---- Machines -------------------------------------------------------------------------------

const ACTION_RESULT: Record<string, { via?: DomainState; to: DomainState; ms: number }> = {
  start: { to: 'running', ms: 1500 },
  shutdown: { via: 'shutting-down', to: 'shutoff', ms: 3500 },
  'force-off': { to: 'shutoff', ms: 500 },
  pause: { to: 'paused', ms: 400 },
  resume: { to: 'running', ms: 400 },
  reboot: { to: 'running', ms: 2500 },
  reset: { to: 'running', ms: 800 },
  save: { to: 'shutoff', ms: 2500 },
}

function act(m: w.DemoMachine, action: string): Reply {
  const r = ACTION_RESULT[action]
  if (!r) return json({ error: `unknown action ${action}` }, 400)
  if (m.template && action === 'start') return json({ error: `${m.name} is a template; deploy a machine from it instead.` }, 409)
  if (r.via) m.state = r.via
  setTimeout(() => (m.state = r.to), r.ms)
  return none()
}

let macSeq = 0x40
const newUUID = () =>
  'xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx'.replace(/x/g, () => Math.floor(Math.random() * 16).toString(16))

function addMachine(host: string, from: Partial<w.DemoMachine> & { name: string }): w.DemoMachine {
  const m: w.DemoMachine = {
    uuid: newUUID(),
    host,
    os: 'ubuntu',
    state: 'shutoff',
    vcpus: 2,
    memoryMiB: 2048,
    diskGiB: 20,
    autostart: false,
    profile: 'light',
    network: 'default',
    mac: `52:54:00:3a:12:${(macSeq++).toString(16).padStart(2, '0')}`,
    firmware: 'uefi',
    secureBoot: false,
    display: 'vnc',
    cpu: 0,
    cpuTimeNs: 0,
    memUsedFrac: 0.3,
    snapshots: [],
    firewall: { enabled: false, defaultIn: 'accept', defaultOut: 'accept', antiSpoof: true, rules: [], adapters: ['vnet9'] },
    qemuArgs: { args: [], env: {} },
    ...from,
  }
  w.machines.push(m)
  return m
}

function domainRoute(host: w.DemoHost, method: string, rest: string[], q: URLSearchParams, body: any): Reply {
  const m = w.byUUID(rest[0])
  if (!m || m.host !== host.id) return notFound('no such machine')
  const sub = rest.slice(1).join('/')
  const op = `${method} ${sub}`

  switch (true) {
    case op === 'GET ':
      return json(w.detail(m))
    case op === 'GET xml':
      return xml(w.domainXML(m))
    case op === 'PUT xml':
      return json({ appliesOnNextBoot: m.state === 'running' })
    case op === 'GET settings':
      return json(w.settings(m))
    case op === 'GET guest':
      return json(
        m.state !== 'running'
          ? { connected: false, os: '', users: [], filesystems: [] }
          : {
              connected: true,
              os: m.os === 'windows' ? 'Microsoft Windows Server 2025 Standard' : 'Ubuntu 24.04.3 LTS',
              kernel: m.os === 'windows' ? '10.0.26100' : '6.8.0-85-generic',
              hostname: m.name,
              timezone: 'UTC',
              users: [m.os === 'windows' ? 'Administrator' : 'ubuntu'],
              filesystems: [{ mountpoint: m.os === 'windows' ? 'C:\\' : '/', type: m.os === 'windows' ? 'NTFS' : 'ext4', totalBytes: m.diskGiB * 2 ** 30, usedBytes: m.diskGiB * 0.37 * 2 ** 30 }],
            },
      )
    case op === 'POST guest/password':
      return none()
    case op === 'POST settings/preview': {
      const before = w.domainXML(m)
      const after = before
        .replace(/<vcpu placement='static'>\d+<\/vcpu>/, `<vcpu placement='static'>${body?.vcpus ?? m.vcpus}</vcpu>`)
        .replace(/<memory unit='KiB'>\d+<\/memory>/, `<memory unit='KiB'>${(body?.memoryMiB ?? m.memoryMiB) * 1024}</memory>`)
        .replace(/<currentMemory unit='KiB'>\d+<\/currentMemory>/, `<currentMemory unit='KiB'>${(body?.currentMemoryMiB ?? m.memoryMiB) * 1024}</currentMemory>`)
      return json({ before, after })
    }
    case op === 'PUT settings': {
      const live = m.state === 'running'
      if (body?.title !== undefined) m.title = body.title || undefined
      if (body?.description !== undefined) m.description = body.description || undefined
      const changed: string[] = []
      if (body?.vcpus && body.vcpus !== m.vcpus) (m.vcpus = body.vcpus), changed.push('vCPUs')
      if (body?.memoryMiB && body.memoryMiB !== m.memoryMiB) (m.memoryMiB = body.memoryMiB), changed.push('memory')
      return json({ appliesOnNextBoot: false, appliedLive: live ? changed : [], liveErrors: [] })
    }
    case op === 'GET snapshots':
      return json({ snapshots: m.snapshots })
    case op === 'POST snapshots': {
      const name = (body?.name as string) || `snap-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}`
      const parent = m.snapshots.find((s) => s.current)
      m.snapshots.forEach((s) => (s.current = false))
      const s: SnapshotInfo = { name, description: body?.description || undefined, parent: parent?.name, state: m.state, created: Date.now() / 1000, current: true, memory: m.state === 'running', location: 'internal' }
      m.snapshots.push(s)
      return json({ name })
    }
    case method === 'POST' && rest.length === 4 && rest[1] === 'snapshots' && rest[3] === 'revert': {
      m.snapshots.forEach((s) => (s.current = s.name === rest[2]))
      return none()
    }
    case method === 'DELETE' && rest.length === 3 && rest[1] === 'snapshots':
      m.snapshots = m.snapshots.filter((s) => s.name !== rest[2]).map((s) => (s.parent === rest[2] ? { ...s, parent: undefined } : s))
      return none()
    case method === 'POST' && rest[1] === 'actions':
      return act(m, rest[2])
    case op === 'PUT autostart':
      m.autostart = !!body?.enabled
      return none()
    case op === 'POST rename':
      if (!body?.name) return json({ error: 'give it a name' }, 422)
      m.name = body.name
      return none()
    case op === 'PUT template':
      if (m.state !== 'shutoff') return json({ error: 'Shut the machine down first.' }, 409)
      m.template = !!body?.template
      return none()
    case op === 'DELETE ': {
      w.machines.splice(w.machines.indexOf(m), 1)
      const removeStorage = q.get('storage') === 'remove'
      return json({ removedVolumes: removeStorage ? [`/var/lib/libvirt/images/${m.name}.qcow2`] : [], keptVolumes: [] })
    }
    case op === 'POST clone': {
      const name = body?.name || `${m.name}-clone`
      return json(job('clone', `Clone ${m.name} as ${name}`, 4000, () => addMachine(m.host, { ...m, uuid: newUUID(), name, state: 'shutoff', snapshots: [], autostart: false }).uuid))
    }
    case op === 'POST deploy': {
      const spec = body as DeploySpec
      return json(
        job('deploy', `Deploy ${spec.name} from ${m.name}`, spec.linked ? 2000 : 5000, () =>
          addMachine(m.host, { name: spec.name, os: m.os, vcpus: m.vcpus, memoryMiB: m.memoryMiB, diskGiB: m.diskGiB, linkedFrom: spec.linked ? m.uuid : undefined, state: spec.start ? 'running' : 'shutoff', profile: 'web' }).uuid,
        ),
      )
    }
    case op === 'POST backup':
      return json(job('backup', `Back up ${m.name}`, 4000, () => {
        const created = new Date().toISOString()
        w.backups.unshift({ id: `${created.slice(0, 10)}-${m.name}-${Date.now()}`, domain: m.name, domainUuid: m.uuid, created, live: m.state === 'running', automatic: false, disks: [{ target: 'vda', file: 'vda.qcow2', source: `/var/lib/libvirt/images/${m.name}.qcow2`, sizeBytes: m.diskGiB * 2 ** 30 * 0.4 }], sizeBytes: m.diskGiB * 2 ** 30 * 0.4, durationSec: 4 })
      }))
    case op === 'POST move': {
      const target = w.HOSTS.find((h) => h.id === body?.host)
      return json(
        job('move', `${m.state === 'running' ? 'Live-migrate' : 'Move'} ${m.name} to ${target?.name ?? 'another host'}`, 6000, () => {
          if (target) m.host = target.id
        }),
      )
    }
    case op === 'GET config-history':
      return json({ versions: w.configVersions(m) })
    case method === 'GET' && rest[1] === 'config-history':
      return json({ xml: w.configVersionXML(m, rest[2]) })
    case method === 'POST' && rest[1] === 'config-history':
      return json({ appliesOnNextBoot: m.state === 'running' })
    case op === 'GET firewall':
      return json(m.firewall)
    case op === 'PUT firewall':
      m.firewall = body
      return json({})
    case op === 'GET qemu-args':
      return json(m.qemuArgs)
    case op === 'PUT qemu-args':
      m.qemuArgs = body
      return json({ appliesOnNextBoot: m.state === 'running' })
    case op === 'GET console/info':
      return json({ running: m.state === 'running' || m.state === 'paused', serial: m.state === 'running' || m.state === 'paused' ? ['pty'] : [], display: m.display, heads: 1 })
    case op === 'PUT display':
      m.display = body?.protocol === 'spice' ? 'spice' : 'vnc'
      return none()
    case op === 'PUT respool':
      for (const p of w.respools) p.machines = p.machines.filter((u) => u !== m.uuid)
      w.respools.find((p) => p.id === body?.pool)?.machines.push(m.uuid)
      return none()
    case op === 'PUT project':
      m.project = body?.project || undefined
      return none()
    case op === 'PUT media' || op === 'POST disks' || op === 'POST interfaces' || op === 'POST hostdevs' || op === 'POST devices' || op === 'POST devices/detach':
      return json({ appliesOnNextBoot: false, target: 'vdb', liveError: '' })
    case method === 'POST' && rest[1] === 'disks' && rest[3] === 'resize':
      m.diskGiB = body?.sizeGiB ?? m.diskGiB
      return none()
    case method === 'PUT' && rest[1] === 'interfaces' && rest[3] === 'bandwidth':
      return none()
    case method === 'POST' && rest[1] === 'disks' && rest[3] === 'move':
      return json(job('disk-move', `Move ${rest[2]} to ${body?.pool ?? 'another pool'}`, 3000))
  }
  return method === 'GET' ? notFound() : json({})
}

function createMachine(host: w.DemoHost, spec: CreateSpec): Reply {
  if (w.machines.some((m) => m.host === host.id && m.name === spec.name)) return json({ error: `A machine named ${spec.name} already exists.` }, 409)
  const m = addMachine(host.id, {
    name: spec.name,
    os: spec.os === 'windows' ? 'windows' : 'ubuntu',
    vcpus: spec.vcpus,
    memoryMiB: spec.memoryMiB,
    diskGiB: spec.diskGiB,
    firmware: spec.firmware,
    secureBoot: spec.secureBoot,
    tpm: spec.tpm,
    network: spec.network?.network ?? 'default',
    project: spec.project,
    state: 'shutoff',
    profile: 'web',
  })
  if (spec.start) setTimeout(() => (m.state = 'running'), 1500)
  return json({ uuid: m.uuid })
}

// ---- History --------------------------------------------------------------------------------

const RANGES: Record<string, { span: number; step: number }> = {
  '1h': { span: 3600, step: 10 },
  '6h': { span: 21600, step: 10 },
  '24h': { span: 86400, step: 60 },
  '7d': { span: 604800, step: 600 },
  '30d': { span: 2592000, step: 3600 },
}

function history(host: w.DemoHost, q: URLSearchParams): Reply {
  const r = RANGES[q.get('range') ?? '']
  if (!r) return json({ error: 'range must be one of 1h, 6h, 24h, 7d, 30d' }, 400)
  const n = Math.floor(r.span / r.step)
  const end = Math.floor(Date.now() / 1000 / r.step) * r.step
  const start = end - (n - 1) * r.step
  const series: Record<string, (number | null)[]> = {}
  for (const name of (q.get('series') ?? '').split(',').filter(Boolean)) {
    const vm = /^vm\.([0-9a-f-]{36})\.(.+)$/.exec(name)
    const m = vm ? w.byUUID(vm[1]) : undefined
    const metric = vm ? vm[2] : name.slice('host.'.length)
    const seed = [...name + host.id].reduce((a, c) => a + c.charCodeAt(0), 0)
    let base: number
    switch (metric) {
      case 'cpu':
        base = m ? Math.max(3, m.cpu || 20) : host.baseCpu + 6 + w.onHost(host.id).filter((x) => x.state === 'running').length * 3
        break
      case 'mem':
        base = m ? m.memUsedFrac * 100 : 20 + w.onHost(host.id).filter((x) => x.state === 'running').length * 5
        break
      case 'disk.read':
      case 'disk.write':
        base = (m ? 4e6 : 40e6) * (metric === 'disk.read' ? 1.4 : 1)
        break
      default:
        base = (m ? 3e6 : 30e6) * (metric === 'net.rx' ? 1.3 : 1)
    }
    const pct = metric === 'cpu' || metric === 'mem'
    series[name] = Array.from({ length: n }, (_, i) => {
      const t = start + i * r.step
      // A working-day curve plus noise; stopped machines leave a gap.
      if (m && m.state !== 'running' && i > n * 0.7) return null
      const day = Math.sin(((t / 3600 + seed) % 24) / 24 * 2 * Math.PI - Math.PI / 2) * 0.35 + 1
      const noise = 1 + (Math.sin(t / 97 + seed) + Math.sin(t / 13 + seed * 3)) * 0.12
      const v = base * day * noise
      return pct ? Math.max(0, Math.min(100, v)) : Math.max(0, v)
    })
  }
  return json({ start, step: r.step, series })
}

// ---- Router ---------------------------------------------------------------------------------

const HOST_RE = /^\/api\/v1\/hosts\/([0-9a-f]{16})(\/.*)$/

/** A running demo plan moves on by itself: one step every 3 seconds; when it is done every
 * member runs the target version. */
function advanceLCM(plan: LCMPlan): LCMPlan {
  if (plan.state !== 'running') return plan
  const steps = w.lcmSteps(plan.hostOrder, Date.parse(plan.started), 3000, Date.now())
  const total = plan.hostOrder.length * 6
  if (steps.length === total && steps[total - 1].state === 'done') {
    Object.assign(plan, { state: 'succeeded', finished: steps[total - 1].at, steps })
    for (const id of plan.hostOrder) w.hostVersions[id] = plan.targetVersion
    return plan
  }
  return { ...plan, steps }
}

export function handle(method: string, url: URL, body: any): Reply {
  let path = url.pathname
  let host = w.SELF
  const hm = HOST_RE.exec(path)
  if (hm) {
    const h = w.HOSTS.find((x) => x.id === hm[1])
    if (!h) return json({ error: 'no such host in this cluster' }, 404)
    host = h
    path = '/api/v1' + hm[2]
  }
  const q = url.searchParams
  const op = `${method} ${path}`
  const parts = path.split('/').slice(3).map(decodeURIComponent) // after /api/v1

  switch (op) {
    // Session and account
    case 'GET /api/v1/version':
      return json({ version: '0.6.0-demo' })
    case 'GET /api/v1/auth/session':
      return json(session)
    case 'POST /api/v1/auth/login':
      session = { mode: 'pam', user: body?.user || 'demo', role: 'admin' }
      return json(session)
    case 'POST /api/v1/auth/logout':
      session = { mode: 'pam' }
      return none()
    case 'GET /api/v1/auth/methods':
      return json({ ldap: true, oidc: true, oidcLabel: 'Sign in with Keycloak' })
    case 'GET /api/v1/account/2fa':
      return json({ available: true, enabled: true, since: ago(60 * 86400e3), recoveryLeft: 8 })
    case 'GET /api/v1/twofactor/users':
      return json({ users: ['onur', 'deniz'] })
    case 'GET /api/v1/tokens':
      return json({
        tokens: [
          { id: 'tok-1', name: 'ci-bot', owner: 'onur', role: 'operator', created: ago(30 * 86400e3), expires: new Date(Date.now() + 60 * 86400e3).toISOString(), lastUsed: ago(6 * 3600e3) },
          { id: 'tok-2', name: 'terraform', owner: 'onur', role: 'admin', created: ago(12 * 86400e3), lastUsed: ago(2 * 86400e3) },
        ],
      })
    case 'POST /api/v1/tokens':
      return json({ token: { id: 'tok-new', name: body?.name ?? 'token', owner: 'demo', role: body?.role ?? 'viewer', created: new Date().toISOString() }, secret: 'vmt_demo_0000000000000000000000000000' })
    case 'GET /api/v1/quotas/mine':
      return json({ unlimited: true })
    case 'GET /api/v1/quotas':
      return json({
        rules: [
          { kind: 'group', name: 'ml-team', machines: 6, vcpus: 48, memoryMiB: 196608, diskGiB: 2000 },
          { kind: 'user', name: 'deniz', machines: 3, vcpus: 24, memoryMiB: 65536, diskGiB: 500 },
        ],
      })
    case 'GET /api/v1/signin-config':
      return json({
        redirectUrl: `${location.origin}/api/v1/auth/oidc/callback`,
        config: {
          ldap: { enabled: true, url: 'ldaps://dc01.example.com', startTLS: false, insecureSkipVerify: false, bindDN: 'CN=virtomir,OU=Service,DC=example,DC=com', bindPassword: '••••••••', userBase: 'OU=Staff,DC=example,DC=com', userFilter: '(sAMAccountName=%s)', groupAttr: 'memberOf', roles: { admin: ['VirtoMir-Admins'], operator: ['VirtoMir-Operators'], viewer: ['Domain Users'] } },
          oidc: { enabled: true, label: 'Sign in with Keycloak', issuer: 'https://sso.example.com/realms/company', clientId: 'virtomir', clientSecret: '••••••••', usernameClaim: 'preferred_username', groupsClaim: 'groups', roles: { admin: ['virtomir-admins'], operator: [], viewer: [] } },
        },
      })
    case 'GET /api/v1/update':
      return json({ current: '0.6.0-demo', repo: 'onurbaydas/VirtoMir', available: false, installable: false, reason: 'This is the online demo; install VirtoMir on a host to get updates.', checkedAt: new Date().toISOString() })
    case 'GET /api/v1/setup':
      return json({
        done: true,
        arch: 'x86_64',
        backupDir: '/var/lib/virtomir/backups',
        checks: [
          { id: 'kvm', ok: true, needed: true, title: 'KVM acceleration', detail: '/dev/kvm is there (AMD-V).' },
          { id: 'pool', ok: true, needed: true, title: 'A storage pool', detail: '4 pools, default at /var/lib/libvirt/images.' },
          { id: 'network', ok: true, needed: true, title: 'A network for machines', detail: 'default (NAT, 192.168.122.0/24) starts with the host.' },
          { id: 'backups', ok: true, needed: false, title: 'A backup location', detail: '/var/lib/virtomir/backups' },
          { id: 'image', ok: true, needed: false, title: 'An installer or cloud image', detail: '5 images in pool iso.' },
        ],
      })
    case 'GET /api/v1/openapi.json':
      return json({
        paths: {
          '/api/v1/domains': { get: { summary: 'List machines', description: 'role: viewer' }, post: { summary: 'Create a machine', description: 'role: admin' } },
          '/api/v1/domains/{uuid}': { get: { summary: 'Machine details', description: 'role: viewer' }, delete: { summary: 'Delete a machine', description: 'role: admin' } },
          '/api/v1/domains/{uuid}/actions/{action}': { post: { summary: 'Start, shut down, reboot …', description: 'role: operator' } },
          '/api/v1/domains/{uuid}/snapshots': { get: { summary: 'List snapshots', description: 'role: viewer' }, post: { summary: 'Take a snapshot', description: 'role: operator' } },
          '/api/v1/metrics': { get: { summary: 'Prometheus metrics', description: 'role: viewer' } },
          '/api/v1/stream': { get: { summary: 'Live snapshots (Server-Sent Events)', description: 'role: viewer' } },
        },
      })

    // Cluster
    case 'GET /api/v1/cluster':
      return json({
        enabled: true,
        selfId: w.SELF.id,
        members: w.HOSTS.map((h) => ({ id: h.id, name: h.name, address: `https://10.20.0.1${w.HOSTS.indexOf(h) + 1}:8420`, fingerprint: 'SHA256:' + h.id.repeat(2).slice(0, 43), self: h.self, reachable: true, version: w.hostVersions[h.id] })),
        mixedVersions: new Set(Object.values(w.hostVersions)).size > 1,
        minVersion: Object.values(w.hostVersions).sort()[0],
      })
    case 'GET /api/v1/cluster/lcm': {
      const plans = w.lcmPlans.map(advanceLCM)
      return json({ enabled: true, activePlan: plans.find((p) => p.state === 'running' || p.state === 'paused'), plans: [...plans].reverse() })
    }
    case 'POST /api/v1/cluster/lcm/precheck':
      return json({
        checks: w.HOSTS.map((h) => ({ hostId: h.id, hostName: h.name, ok: true, problems: [] })),
      })
    case 'POST /api/v1/cluster/lcm/start': {
      if (w.lcmPlans.map(advanceLCM).some((p) => p.state === 'running' || p.state === 'paused')) return json({ error: 'a rolling upgrade is already running' }, 409)
      const order = [...w.HOSTS.filter((h) => !h.self), w.SELF].map((h) => h.id)
      const plan = {
        id: `lcm-${String(w.lcmPlans.length + 1).padStart(4, '0')}`, started: new Date().toISOString(), state: 'running' as const, hostOrder: order,
        targetVersion: body?.targetVersion || '0.6.0-demo', osUpdates: !!body?.osUpdates, reboot: !!body?.reboot, moveBack: !!body?.moveBack,
        startedBy: session.user ?? 'demo', maintenance: [], steps: [],
      }
      w.lcmPlans.push(plan)
      return json(advanceLCM(plan), 202)
    }
    case `GET /api/v1/cluster/lcm/plans/${parts[3]}`:
    case `POST /api/v1/cluster/lcm/plans/${parts[3]}/${parts[4]}`: {
      const plan = w.lcmPlans.find((p) => p.id === parts[3])
      if (!plan) return json({ error: 'no such plan' }, 404)
      if (method === 'POST') {
        const cur = advanceLCM(plan)
        if (cur.state !== 'running' && cur.state !== 'paused') return json({ error: 'this plan is not running on this host' }, 409)
        if (parts[4] === 'abort') Object.assign(plan, { state: 'aborted', finished: new Date().toISOString(), steps: cur.steps })
        else plan.state = parts[4] === 'pause' ? 'paused' : 'running'
      }
      return json(advanceLCM(plan))
    }
    case 'GET /api/v1/cluster/cpu-baseline':
      return json({ model: 'EPYC-Genoa', hosts: 3 })
    case 'POST /api/v1/cluster/invite':
      return json({ code: 'VMJ1-DEMO-7Q4K-PX2M-9WTE', address: 'https://10.20.0.11:8420', expires: new Date(Date.now() + 15 * 60e3).toISOString() })
    case 'POST /api/v1/cluster/join':
      return json({ error: DEMO_ONLY }, 422)
    case 'GET /api/v1/containers/images':
      return json({
        available: true,
        images: [
          { key: 'debian-13', label: 'Debian 13 (trixie)' },
          { key: 'ubuntu-24.04', label: 'Ubuntu 24.04 LTS' },
          { key: 'alpine-3.22', label: 'Alpine Linux 3.22' },
        ],
      })
    case 'GET /api/v1/host/confidential':
      return json({ kinds: ['sev', 'sev-snp'] })
    case 'GET /api/v1/host/ksm':
      return json({ supported: true, active: true, pagesShared: 81234, pagesSharing: 412870, pagesUnshared: 96120, pagesVolatile: 2210, savedBytes: 412870 * 4096, sleepMillisecs: 20, pagesToScan: 100 })
    case 'GET /api/v1/activity/siem':
      return json({ enabled: false, target: '', facility: 1, tag: 'virtomir', hostname: '' })
    case 'GET /api/v1/storage/zfs/replications':
      return json({
        replications: [
          {
            spec: { id: 'rep-demo00000001', sourcePool: 'tank/vms', sourceVolume: 'db-01-disk0', targetHost: 'root@10.20.0.12', targetPool: 'dr', intervalMinutes: 15, enabled: true, keep: 3 },
            state: 'idle',
            lastSync: new Date(Date.now() - 6 * 60e3).toISOString(),
            lastSnapshot: 'virtomir-rep-' + new Date(Date.now() - 6 * 60e3).toISOString().slice(0, 19).replace(/[-:]/g, '').replace('T', '-'),
            bytesTransferred: 184 * 2 ** 20,
          },
        ],
      })
    case 'GET /api/v1/cluster/drs/placement':
      return json({ hostId: w.HOSTS[1].id, hostName: w.HOSTS[1].name, cpuPct: 22.1, memPct: 38.0 })
    case 'GET /api/v1/cluster/drs':
      return json({
        enabled: true,
        mode: 'manual',
        balanceScore: 78.5,
        cpuStdDev: 24.2,
        memStdDev: 18.7,
        thresholdStdDev: 15.0,
        lastEvaluationTime: new Date().toISOString(),
        hosts: w.HOSTS.map((h, i) => ({
          id: h.id,
          name: h.name,
          reachable: true,
          cpuPct: [68.4, 22.1, 41.3][i % 3],
          memPct: [74.2, 38.0, 52.6][i % 3],
          memUsedB: [48 * 2 ** 30, 16 * 2 ** 30, 32 * 2 ** 30][i % 3],
          memTotalB: 64 * 2 ** 30,
          vmCount: [7, 2, 4][i % 3],
        })),
        recommendations: [
          {
            id: 'drs-rec-01',
            priority: 4,
            domainUuid: '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03',
            domainName: 'db-01',
            sourceHostId: 'hv-01',
            sourceName: 'hv-01',
            targetHostId: 'hv-02',
            targetName: 'hv-02',
            reason: 'hv-01 CPU utilization (68.4%) exceeds cluster average by 24.2%. Migrating db-01 to hv-02 balances compute load.',
            expectedGain: 14.8,
            createdAt: new Date().toISOString(),
          },
        ],
        rules: demoPlacementRules.map((r) => ({
          id: r.id,
          name: r.name,
          kind: r.kind === 'vm_host' ? 'vm_host_affinity' : r.kind,
          enabled: r.enabled,
          mandatory: r.strength === 'must',
          domains: r.domains,
          targetHost: r.hosts?.[0],
        })),
      })
    case 'PUT /api/v1/cluster/drs/config':
      return json({ status: 'ok' })
    case 'POST /api/v1/cluster/drs/rules': {
      const id = newUUID().replace(/-/g, '').slice(0, 16)
      const r: PlacementRule = {
        id,
        name: body?.name || 'Rule',
        kind: body?.kind === 'vm_host_affinity' ? 'vm_host' : body?.kind || 'vm_anti_affinity',
        enabled: body?.enabled !== false,
        strength: body?.mandatory ? 'must' : 'should',
        domains: body?.domains || [],
        hosts: body?.targetHost ? [body.targetHost] : undefined,
      }
      demoPlacementRules.push(r)
      return json({ status: 'created', id: r.id })
    }
    case 'GET /api/v1/cluster/inventory/machines': {
      const all = w.HOSTS.flatMap((h) =>
        w.onHost(h.id).map((m, i) => ({
          ...w.liveDomain(m, i),
          hostId: h.id,
          hostName: h.name,
        })),
      )
      return json({ domains: all })
    }

    // Placement (ADR-0062)
    case 'GET /api/v1/placement':
      return json({ rules: demoPlacementRules, hostLabels: demoHostLabels, violations: getDemoViolations() })
    case 'POST /api/v1/placement/rules': {
      if (!body?.name?.trim()) return json({ error: 'rule name is required' }, 422)
      if (body.kind === 'vm_anti_affinity' && (!body.domains || body.domains.length < 2)) return json({ error: 'anti-affinity rule requires at least two VMs' }, 422)
      if (body.kind === 'vm_host' && (!body.hosts || body.hosts.length < 1)) return json({ error: 'vm-host rule requires at least one host' }, 422)
      if (!body.domains || body.domains.length < 1) return json({ error: 'at least one VM must be assigned to the rule' }, 422)
      const id = body.id || newUUID().replace(/-/g, '').slice(0, 16)
      const rule: PlacementRule = {
        id,
        name: body.name.trim(),
        kind: body.kind,
        enabled: body.enabled !== false,
        strength: body.strength || 'must',
        domains: body.domains || [],
        hosts: body.hosts,
        exclude: !!body.exclude,
      }
      const existing = demoPlacementRules.findIndex((r) => r.id === id)
      if (existing >= 0) demoPlacementRules[existing] = rule
      else demoPlacementRules.push(rule)
      return json(rule, 201)
    }
    case 'GET /api/v1/placement/violations':
      return json(getDemoViolations())
    case 'POST /api/v1/placement/check':
      return json(demoPlacementCheck(body?.domainUuid ?? '', body?.hostId ?? ''))

    // Live data
    case 'GET /api/v1/summary':
      return json(w.snapshotOf(host))
    case 'GET /api/v1/host':
      return json(host.info)
    case 'GET /api/v1/history':
      return history(host, q)
    case 'GET /api/v1/jobs':
      return json({ jobs: [...jobs.values()].reverse() })

    // Machines
    case 'GET /api/v1/domains':
      return json({ domains: w.onHost(host.id).map((m, i) => w.liveDomain(m, i)) })
    case 'POST /api/v1/domains':
      return createMachine(host, body as CreateSpec)
    case 'POST /api/v1/domains/import':
      return json({ error: DEMO_ONLY }, 422)

    // Projects (ADR-0060)
    case 'GET /api/v1/projects':
      return json({ projects: w.projects })
    case 'POST /api/v1/projects': {
      const p = body as Project
      if (!p?.id || !p?.name) return json({ error: 'a project needs an ID and a name' }, 422)
      if (w.projects.some((x) => x.id === p.id)) return json({ error: `a project named ${p.id} exists already` }, 409)
      const created: Project = { ...p, created: new Date().toISOString() }
      w.projects.push(created)
      return json({ project: created }, 201)
    }

    // Storage and networks
    case 'GET /api/v1/storage/pools':
      return json({ pools: w.pools })
    case 'GET /api/v1/networks':
      return json({ networks: w.networks })

    // Catalog (ADR-0065)
    case 'GET /api/v1/catalog/blueprints': {
      const pj = q.get('project')
      return json({
        blueprints: w.catalogBlueprints.filter((b) => !pj || !b.allowedProjects?.length || b.allowedProjects.includes(pj)),
      })
    }
    case 'POST /api/v1/catalog/blueprints': {
      const bp = body as Blueprint
      if (!bp?.id?.trim() || !bp?.name?.trim()) return json({ error: 'a blueprint needs an ID and a name' }, 422)
      if (w.catalogBlueprints.some((x) => x.id === bp.id)) return json({ error: `a blueprint named ${bp.id} exists already` }, 409)
      const invalid = validateBlueprint(bp)
      if (invalid) return json({ error: invalid }, 422)
      const created = { ...bp, defaultPresetId: bp.defaultPresetId || bp.presets[0].id, created: new Date().toISOString(), updated: new Date().toISOString() }
      w.catalogBlueprints.push(created)
      return json({ blueprint: created }, 201)
    }
    case 'GET /api/v1/catalog/requests': {
      const st = q.get('status')
      const pj = q.get('project')
      const reqUser = q.get('requester')
      return json({
        requests: w.catalogRequests.filter((r) => (!st || r.status === st) && (!pj || r.projectId === pj) && (!reqUser || r.requester === reqUser)).map(maskedRequest),
      })
    }
    case 'POST /api/v1/catalog/requests': {
      const req = body as CatalogRequest
      const bp = w.catalogBlueprints.find((x) => x.id === req?.blueprintId)
      if (!bp) return json({ error: `no blueprint ${req?.blueprintId}` }, 404)
      const proj = w.projects.find((x) => x.id === req?.projectId)
      if (!proj) return json({ error: `no project ${req?.projectId}` }, 404)
      for (const f of bp.cloudInitFields ?? []) {
        if (f.required && !String(req?.fieldValues?.[f.key] ?? '').trim()) {
          return json({ error: `field "${f.label || f.key}" is required` }, 422)
        }
      }
      const preset = bp.presets.find((p) => p.id === req?.presetId) ?? bp.presets[0]
      if (bp.allowedProjects?.length && !bp.allowedProjects.includes(proj.id)) return json({ error: `project ${proj.id} may not order ${bp.id}` }, 422)
      if (req?.network && bp.allowedNetworks?.length && !bp.allowedNetworks.includes(req.network)) return json({ error: `network ${req.network} not allowed by ${bp.id}` }, 422)
      const days: number = req?.leaseDays || bp.defaultLeaseDays || 30
      if (bp.maxLeaseDays && days > bp.maxLeaseDays) return json({ error: `lease of ${days} days exceeds the ${bp.id} maximum of ${bp.maxLeaseDays}` }, 422)
      const created: CatalogRequest = {
        ...req,
        // The suffix keeps IDs unique: two orders in the same millisecond must not collide.
        id: `req-${Date.now().toString(36)}-${++reqSeq}`,
        requester: session.user ?? 'demo',
        presetId: preset.id,
        vcpus: preset.vcpus,
        memoryMiB: preset.memoryMiB,
        diskGiB: preset.diskGiB,
        leaseDays: days,
        status: bp.requireApproval ? 'pending_approval' : 'active',
        createdAt: new Date().toISOString(),
      }
      w.catalogRequests.push(created)
      if (created.status === 'active') provision(created, bp)
      return json(maskedRequest(created), 201)
    }
    case 'GET /api/v1/catalog/pricing':
      return json(w.pricing)
    case 'PUT /api/v1/catalog/pricing': {
      const pc = body as PricingConfig
      if (![pc?.vcpuHour, pc?.ramGiBHour, pc?.diskGiBMonth].every((v) => Number.isFinite(v) && (v as number) >= 0)) return json({ error: 'rates must be non-negative numbers' }, 422)
      const currency = pc.currency?.trim() || w.pricing.currency
      if (!currency || !/^[\p{Sc}\p{L}]{1,6}$/u.test(currency)) return json({ error: 'currency must be 1 to 6 letters or currency symbols' }, 422)
      const next: PricingConfig = { vcpuHour: pc.vcpuHour, ramGiBHour: pc.ramGiBHour, diskGiBMonth: pc.diskGiBMonth, currency }
      w.setPricing(next)
      return json({ pricing: next })
    }
    case 'GET /api/v1/catalog/showback':
      return showback(q)

    // Backups and schedules
    case 'GET /api/v1/backups': {
      const d = q.get('domain')
      return json({ backups: w.backups.filter((b) => !d || b.domain === d || b.domainUuid === d), dir: '/var/lib/virtomir/backups' })
    }
    case 'POST /api/v1/backups/restore':
      return json(job('restore', `Restore ${body?.name ?? 'backup'}`, 5000, () => addMachine(host.id, { name: body?.name || 'restored' }).uuid))
    case 'GET /api/v1/backups/settings':
      return json({ backupDir: '/var/lib/virtomir/backups' })
    case 'GET /api/v1/offsite':
      return json({
        config: { repository: 's3:https://s3.eu-central-1.wasabisys.com/acme-vm-backups', password: '••••••••', env: { AWS_ACCESS_KEY_ID: '••••••••', AWS_SECRET_ACCESS_KEY: '••••••••' }, afterScheduled: true, keepDaily: 14, keepWeekly: 8, keepMonthly: 12, limitUploadKiB: 51200 },
        status: { restic: 'restic 0.18.1', configured: true },
      })
    case 'GET /api/v1/offsite/snapshots':
      return json({
        snapshots: w.backups.slice(0, 4).map((b, i) => ({ id: `${i}a1f9c2e7d4b3`.padEnd(64, '0'), shortId: `${i}a1f9c2e`, time: b.created, host: 'hv-01', machine: b.domain, domainUuid: b.domainUuid, backup: b.id, path: `/var/lib/virtomir/backups/${b.id}`, sizeBytes: b.sizeBytes * 0.6 })),
      })
    case 'POST /api/v1/offsite/upload':
      return json(job('offsite', 'Copy backup off-site', 5000))
    case 'POST /api/v1/offsite/restore':
      return json(job('offsite', 'Bring backup back from off-site', 5000))
    case 'POST /api/v1/offsite/check':
      return json({ message: 'The repository answers; 41 snapshots, last check 2 days ago.' })
    case 'GET /api/v1/schedules': {
      const d = q.get('domain')
      return json({ schedules: w.schedules.filter((s) => !d || s.domain === d || w.byUUID(d)?.name === s.domain) })
    }
    case 'PUT /api/v1/schedules': {
      const s = body as ScheduleInfo
      if (!s.id) s.id = `sch-${Date.now()}`
      const i = w.schedules.findIndex((x) => x.id === s.id)
      if (i >= 0) w.schedules[i] = { ...w.schedules[i], ...s }
      else w.schedules.push({ ...s, nextRun: new Date(Date.now() + 3600e3).toISOString() })
      return json(s)
    }

    // Alerts and activity
    case 'GET /api/v1/alerts':
      // Each alert belongs to the host of its machine (pool alerts to this host).
      return json({ alerts: w.alerts.filter((a) => (a.subjectId ? w.byUUID(a.subjectId)?.host : w.SELF.id) === host.id) })
    case 'GET /api/v1/alerts/config':
      return json(w.alertConfig)
    case 'POST /api/v1/alerts/test':
      return none()
    case 'GET /api/v1/activity':
      return json({ entries: host.self ? w.activity : w.activity.slice(4) })

    // Compliance, FIPS and audit verification (ADR-0064)
    case 'GET /api/v1/compliance/checks':
      return json(w.compliance(host.id))
    case 'POST /api/v1/compliance/checks/run':
      return json(w.compliance(host.id, true))
    case 'GET /api/v1/compliance/fips':
      return json(w.demoFIPS)
    case 'GET /api/v1/cluster/compliance':
      return json(w.HOSTS.map((h) => w.compliance(h.id)))
    case 'POST /api/v1/activity/verify':
      return json(w.demoAuditVerification[host.id] ?? w.demoAuditVerification[w.SELF.id])
    case 'GET /api/v1/activity/export': {
      const rows = host.self ? w.activity : w.activity.slice(4)
      if (q.get('format') === 'csv')
        return {
          status: 200,
          type: 'text/csv; charset=utf-8',
          body: ['time,user,method,path,status,error', ...rows.map((e) => [e.time, e.user, e.method, e.path, String(e.status), e.error ?? ''].map((f) => (/[",]/.test(f) ? '"' + f.replaceAll('"', '""') + '"' : f)).join(','))].join('\n'),
        }
      return { status: 200, type: 'application/jsonl; charset=utf-8', body: rows.map((e) => JSON.stringify(e)).join('\n') }
    }

    // Host
    case 'GET /api/v1/host/devices':
      return json(host.self ? w.hardware : { iommu: true, pci: [], usb: [], mdevs: [] })
    case 'GET /api/v1/host/chassis':
      return json({ ...w.chassis, serial: w.chassis.serial + host.name.slice(-1) })
    case 'GET /api/v1/host/topology':
      return json(w.topology())
    case 'GET /api/v1/host/gpus':
      return json({ gpus: host.self ? w.gpus : (w.memberGPUs[host.id] ?? []) })
    case 'GET /api/v1/cluster/gpus':
      return json({ gpus: w.clusterGPUs() })
    case 'GET /api/v1/gpu/pools':
      return json({ pools: w.gpuPools.map(w.gpuPoolView) })
    case 'POST /api/v1/gpu/pools': {
      if (w.gpuPools.some((p) => p.id === body?.id)) return json({ error: `exists: a GPU pool ${body.id} exists already` }, 409)
      const p = { ...body, created: new Date().toISOString() }
      w.gpuPools.push(p)
      return json(p, 201)
    }
    case `GET /api/v1/gpu/pools/${parts[2]}`:
    case `PUT /api/v1/gpu/pools/${parts[2]}`:
    case `DELETE /api/v1/gpu/pools/${parts[2]}`: {
      const i = w.gpuPools.findIndex((p) => p.id === parts[2])
      if (i < 0) return json({ error: 'no GPU pool ' + parts[2] }, 404)
      if (method === 'GET') return json(w.gpuPoolView(w.gpuPools[i]))
      if (method === 'DELETE') {
        w.gpuPools.splice(i, 1)
        return none()
      }
      w.gpuPools[i] = { ...w.gpuPools[i], ...body, id: parts[2] }
      return json(w.gpuPools[i])
    }
    case `GET /api/v1/gpu/pools/${parts[2]}/availability`: {
      const p = w.gpuPools.find((x) => x.id === parts[2])
      return p ? json({ hosts: w.gpuPoolView(p).hosts }) : json({ error: 'no GPU pool ' + parts[2] }, 404)
    }
    case 'POST /api/v1/gpu/placement': {
      const p = w.gpuPools.find((x) => x.id === body?.poolId)
      if (!p) return json({ error: 'no GPU pool' }, 404)
      const cands = (w.gpuPoolView(p).hosts ?? []).filter((h) => p.kind !== 'mig' && h.free >= (body?.count ?? 1)).sort((a, b) => b.free - a.free)
        .map((h) => ({ hostId: h.hostId, hostName: h.hostName, available: h.free }))
      return cands.length ? json({ best: cands[0], candidates: cands }) : none()
    }
    case 'GET /api/v1/host/interfaces':
      return json({ interfaces: w.interfaces })
    case 'GET /api/v1/host/network':
      return json({ backend: 'netplan', created: [{ kind: 'bridge', name: 'br0', ports: ['bond0'], stp: false, takeAddressFrom: 'bond0', backend: 'netplan', created: ago(120 * 86400e3) }] })
    case 'GET /api/v1/host/capture':
      return json({ error: 'Packet capture needs a real host (tcpdump). It is not part of the online demo.' }, 501)

    // HA, resource pools, reports
    case 'GET /api/v1/ha/status':
      return json(w.haStatus())
    case 'GET /api/v1/respools':
      return json({ pools: w.respools })
    case 'GET /api/v1/reports':
      return json({ reports: w.reports })
    case 'POST /api/v1/reports': {
      const kind = body?.kind ?? 'capacity'
      const TITLES: Record<string, string> = {
        capacity: 'Capacity — cluster',
        inventory: 'Machine inventory',
        backup_sla: 'Backup compliance',
        compliance: 'Security compliance',
      }
      const r = { id: `rep-${Date.now()}`, kind, title: TITLES[kind] ?? 'Report', format: body?.format ?? 'html', generatedAt: new Date().toISOString(), sizeBytes: 47100, summary: kind === 'compliance' ? '3 hosts · 75% average compliance score · 11 checks per host' : '3 hosts · 14 machines · N+1: yes', by: 'demo' }
      w.reports.unshift(r)
      return json(r)
    }
    case 'GET /api/v1/report-schedules':
      return json({ schedules: w.reportSchedules })
    case 'GET /api/v1/insights': {
      const f = (days: number, confidence: number) => ({ metric: '', slope: 0.4, daysUntilFull: days, confidence, insufficientData: false, seasonal: true })
      const ms = w.machines.filter((m) => m.host === host.id)
      return json({
        host: { cpu: f(-1, 0.62), mem: f(143, 0.81) },
        pools: [
          { name: 'default', capacityBytes: 1.8e12, allocationBytes: 1.31e12, forecast: f(24, 0.9) },
          { name: 'nvme-fast', capacityBytes: 3.6e12, allocationBytes: 0.9e12, forecast: f(-1, 0.4) },
        ],
        machines: ms.map((m, i) => {
          const big = i % 3 === 0 && m.vcpus > 2
          return {
            uuid: m.uuid, name: m.name, state: m.state, currentVcpus: m.vcpus, currentMemMiB: m.memoryMiB,
            suggestedVcpus: big ? Math.max(1, m.vcpus / 2) : m.vcpus, suggestedMemMiB: m.memoryMiB,
            oversized: big, undersized: false,
            reasons: big ? [`CPU is oversized (p95=6.2% < 15%, p99=21.0% < 35%): suggest reducing from ${m.vcpus} to ${Math.max(1, m.vcpus / 2)} vCPUs`] : [],
          }
        }),
      })
    }

    // VMware (ADR-0061)
    case 'GET /api/v1/vmware/connection':
      return json(demoConnection)
    case 'PUT /api/v1/vmware/connection': {
      const c = body as VMwareConnection
      if (c.password && c.password !== '••••••••') demoConnection.password = c.password
      demoConnection = { ...demoConnection, ...c, password: demoConnection.password || '••••••••' }
      return json(demoConnection)
    }
    case 'DELETE /api/v1/vmware/connection':
      demoConnection = {}
      return none()
    case 'POST /api/v1/vmware/connection/test':
      return json({ ok: true, message: 'Connected successfully to vCenter 8.0.2 (demo).' })
    case 'GET /api/v1/vmware/inventory':
      return json(demoInventory)
    case 'POST /api/v1/vmware/preflight': {
      const vmIds = (body?.vmIds as string[]) || []
      const poolName = (body?.pool as string)?.trim()
      if (!poolName) return json({ error: 'choose the storage pool' }, 422)
      const pool = w.pools.find((x) => x.name === poolName)
      if (!pool) return json({ error: `storage pool not found: ${poolName}` }, 422)
      if (!pool.active) return json({ error: `storage pool '${poolName}' is not active` }, 409)

      const issues: Record<string, string[]> = {}
      const items = vmIds.map((id) => {
        const iss: string[] = []
        const vm = demoInventory.vms.find((v) => v.id === id)
        if (!vm) {
          iss.push(`VM "${id}" not found in VMware inventory`)
        } else {
          if (vm.powerState === 'on') {
            iss.push('Machine is powered on; shut down before migrating.')
          }
          if (id === 'vm-104') {
            iss.push('Warning: Windows guest detected without custom VirtIO ISO; virt-v2v will attempt automatic driver injection.')
          }
          const totalDiskBytes = vm.disks.reduce((acc, d) => acc + d.capacityBytes, 0)
          if (pool.availableBytes > 0 && totalDiskBytes > pool.availableBytes) {
            iss.push(`Not enough free space in pool "${pool.name}" (${Math.round(totalDiskBytes / 1024 ** 3)} GiB needed, ${Math.round(pool.availableBytes / 1024 ** 3)} GiB available)`)
          }
        }
        issues[id] = iss
        return { vmId: id, issues: iss }
      })
      return json({
        pool: pool.name,
        poolPath: pool.path || '/var/lib/libvirt/images',
        poolFreeBytes: pool.availableBytes,
        issues,
        items,
      })
    }
    case 'POST /api/v1/vmware/import': {
      const vmId = body?.vmId || 'vm-101'
      const name = body?.name || vmId
      const pool = (body?.pool as string)?.trim()
      if (pool) {
        const found = w.pools.find((x) => x.name === pool)
        if (!found) return json({ error: `storage pool not found: ${pool}` }, 422)
      }
      const j = job('vmware-import', `Import ${name}`, 3000, () => addMachine(host.id, { name }).uuid)
      return json(j, 202)
    }
    case 'POST /api/v1/vmware/waves': {
      const vmIds = (body?.vmIds as string[]) || ['vm-101']
      const pool = (body?.pool as string)?.trim()
      if (pool) {
        const found = w.pools.find((x) => x.name === pool)
        if (!found) return json({ error: `storage pool not found: ${pool}` }, 422)
      }
      const waveId = `wave-${Date.now().toString(36)}`
      const wave: VMwareWave = {
        id: waveId,
        parallel: body?.parallel || 2,
        created: new Date().toISOString(),
        items: vmIds.map((id) => {
          const found = demoInventory.vms.find((v) => v.id === id)
          return { vmId: id, name: found?.name || id, state: 'running' as const, message: 'Converting disk images…' }
        }),
      }
      demoWaves.unshift(wave)
      const j = job('vmware-wave', `Migration wave: ${wave.items.length} VMs`, 3500, () => {
        for (const it of wave.items) {
          if ((it.vmId === 'vm-104' && !body?.virtioIso) || it.vmId.includes('fail')) {
            it.state = 'failed'
            it.message = 'virt-v2v: disk conversion failed (missing VirtIO drivers for Windows guest)'
          } else {
            it.state = 'done'
            it.message = ''
            addMachine(host.id, { name: it.name })
          }
        }
        return wave.id
      })
      return json(j, 202)
    }
    case 'GET /api/v1/vmware/waves':
      return json({ waves: demoWaves })
  }

  // Parametrised paths.
  if (parts[0] === 'content') {
    // /api/v1/content/config
    if (parts[1] === 'config') {
      if (method === 'GET') return json(w.contentConfig)
      if (method === 'PUT') {
        Object.assign(w.contentConfig, body ?? {})
        return json(w.contentConfig)
      }
      return notFound()
    }

    // /api/v1/content/items
    if (parts[1] === 'items') {
      // GET /api/v1/content/items
      if (parts.length === 2 && method === 'GET') {
        const summaries = w.contentItems.map((it) => {
          const latest = it.versions && it.versions.length > 0 ? it.versions[it.versions.length - 1] : undefined
          const syncStatus = it.sourceHost === host.id ? 'ready' : (it.id === 'ci-k8s-worker-tpl' ? 'missing' : 'ready')
          return {
            ...it,
            versionCount: it.versions?.length ?? 0,
            latestVersion: latest,
            syncStatus,
          }
        })
        return json({ items: summaries })
      }

      // POST /api/v1/content/items
      if (parts.length === 2 && method === 'POST') {
        const id = body?.id || `ci-${Date.now()}-${++jobSeq}`
        const item: ContentItem = {
          id,
          name: body?.name || 'Untitled Content',
          type: body?.type || 'image',
          description: body?.description || '',
          sourceHost: host.id,
          keepVersions: body?.keepVersions || 3,
          syncPolicy: body?.syncPolicy || { mode: 'all' },
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
          versions: [
            {
              id: `ver-${Date.now()}-${++jobSeq}`,
              version: body?.version || '1.0',
              description: body?.description || '',
              sha256: body?.sha256 || 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
              size: body?.size || 1024 * 1024 * 500,
              format: body?.format || (body?.type === 'iso' ? 'iso' : 'qcow2'),
              sourceHost: host.id,
              sourcePool: body?.pool || 'default',
              sourceVol: body?.volume,
              created: new Date().toISOString(),
            },
          ],
        }
        w.contentItems.unshift(item)
        return json({ item }, 201)
      }

      const itemId = parts[2]
      const itemIndex = w.contentItems.findIndex((it) => it.id === itemId)

      // GET /api/v1/content/items/{id}
      if (parts.length === 3 && method === 'GET') {
        if (itemIndex < 0) return notFound('item not found')
        return json(w.contentItems[itemIndex])
      }

      // PUT /api/v1/content/items/{id}
      if (parts.length === 3 && method === 'PUT') {
        if (itemIndex < 0) return notFound('item not found')
        w.contentItems[itemIndex] = { ...w.contentItems[itemIndex], ...(body ?? {}), updated: new Date().toISOString() }
        return json({ item: w.contentItems[itemIndex] })
      }

      // DELETE /api/v1/content/items/{id}
      if (parts.length === 3 && method === 'DELETE') {
        if (itemIndex >= 0) {
          w.contentItems.splice(itemIndex, 1)
        }
        return none()
      }

      // POST /api/v1/content/items/{id}/versions
      if (parts.length === 4 && parts[3] === 'versions' && method === 'POST') {
        if (itemIndex < 0) return notFound('item not found')
        const item = w.contentItems[itemIndex]
        const version: ContentVersion = {
          id: `ver-${Date.now()}-${++jobSeq}`,
          version: body?.version || `${(item.versions?.length ?? 0) + 1}.0`,
          description: body?.description || '',
          sha256: body?.sha256 || 'f4b88235e16f3d99432f81156828a2a7d451270274092b34a9b6c08d1326c9aa',
          size: body?.size || 1024 * 1024 * 512,
          format: body?.format || (item.type === 'iso' ? 'iso' : 'qcow2'),
          sourceHost: host.id,
          sourcePool: body?.pool || 'default',
          sourceVol: body?.volume,
          created: new Date().toISOString(),
        }
        if (!item.versions) item.versions = []
        item.versions.push(version)
        item.updated = new Date().toISOString()
        return json({ item, version }, 201)
      }

      // DELETE /api/v1/content/items/{id}/versions/{vid}
      if (parts.length === 5 && parts[3] === 'versions' && method === 'DELETE') {
        if (itemIndex < 0) return notFound('item not found')
        const item = w.contentItems[itemIndex]
        const vid = parts[4]
        if (item.versions) {
          item.versions = item.versions.filter((v) => v.id !== vid && v.version !== vid)
        }
        item.updated = new Date().toISOString()
        return none()
      }

      // POST /api/v1/content/items/{id}/sync
      if (parts.length === 4 && parts[3] === 'sync' && method === 'POST') {
        if (itemIndex < 0) return notFound('item not found')
        const item = w.contentItems[itemIndex]
        const j = job('content-sync', `Sync ${item.name}`, 3000)
        return json(j, 202)
      }

      // POST /api/v1/content/items/{id}/deploy
      if (parts.length === 4 && parts[3] === 'deploy' && method === 'POST') {
        if (itemIndex < 0) return notFound('item not found')
        const item = w.contentItems[itemIndex]
        const vmName = body?.name || `${item.name.toLowerCase().replace(/[^a-z0-9_-]/g, '-')}-vm`
        const j = job('content-deploy', `Deploy ${vmName} from ${item.name}`, 3500, () => {
          const created = addMachine(host.id, { name: vmName, state: body?.start ? 'running' : 'shutoff' })
          return created.uuid
        })
        return json(j, 202)
      }
    }
  }

  if (parts[0] === 'domains' && parts.length >= 2) return domainRoute(host, method, parts.slice(1), q, body)
  if (parts[0] === 'jobs' && parts.length === 2 && method === 'GET') {
    const j = jobs.get(parts[1])
    return j ? json(j) : notFound('no such job')
  }
  if (parts[0] === 'storage' && parts[1] === 'pools' && parts.length >= 3) {
    const p = w.pools.find((x) => x.name === parts[2])
    if (!p) return notFound('no such pool')
    if (method === 'GET' && parts[3] === 'volumes') return json({ volumes: w.volumes(p.name) })
    if (method === 'GET' && parts[3] === 'xml') return xml(w.poolXML(p))
    if (method === 'POST' && parts[3] === 'download') return json(job('download', `Download ${body?.name ?? 'image'} into ${p.name}`, 6000))
    if (method === 'POST' && (parts[3] === 'upload' || parts[3] === 'import-ova')) return json({ error: DEMO_ONLY }, 422)
    return method === 'GET' ? notFound() : json({ restartNeeded: false })
  }
  if (parts[0] === 'projects' && parts.length >= 2) {
    const i = w.projects.findIndex((p) => p.id === parts[1])
    if (method === 'GET') return i >= 0 ? json(w.projects[i]) : notFound(`no project ${parts[1]}`)
    if (method === 'PUT') {
      if (i < 0) return notFound(`no project ${parts[1]}`)
      const p = body as Project
      w.projects[i] = { ...p, id: w.projects[i].id, created: w.projects[i].created }
      return json({ project: w.projects[i] })
    }
    if (method === 'DELETE') {
      if (i >= 0) {
        w.projects.splice(i, 1)
        for (const m of w.machines) if (m.project === parts[1]) m.project = undefined
      }
      return none()
    }
  }
  if (parts[0] === 'catalog' && parts[1] === 'blueprints' && parts.length === 3) {
    const i = w.catalogBlueprints.findIndex((p) => p.id === parts[2])
    if (method === 'GET') return i >= 0 ? json(w.catalogBlueprints[i]) : notFound(`no blueprint ${parts[2]}`)
    if (method === 'PUT') {
      if (i < 0) return notFound(`no blueprint ${parts[2]}`)
      const next = body as Blueprint
      const invalid = validateBlueprint(next)
      if (invalid) return json({ error: invalid }, 422)
      w.catalogBlueprints[i] = { ...next, id: w.catalogBlueprints[i].id, defaultPresetId: next.defaultPresetId || next.presets[0].id, created: w.catalogBlueprints[i].created, updated: new Date().toISOString() }
      return json({ blueprint: w.catalogBlueprints[i] })
    }
    if (method === 'DELETE') {
      if (i >= 0) w.catalogBlueprints.splice(i, 1)
      return none()
    }
  }
  if (parts[0] === 'catalog' && parts[1] === 'requests' && parts.length >= 3) {
    const r = w.catalogRequests.find((x) => x.id === parts[2])
    if (!r) return notFound(`no request ${parts[2]}`)
    if (parts.length === 3) {
      if (method === 'GET') {
        const lease = w.catalogLeases.find((l) => l.requestId === r.id)
        const out = maskedRequest(r)
        return json(lease ? { request: out, lease } : { request: out })
      }
      if (method === 'DELETE') {
        w.catalogRequests.splice(w.catalogRequests.indexOf(r), 1)
        return none()
      }
    }
    if (method === 'POST' && parts[3] === 'approve') {
      if (r.status !== 'pending_approval') return json({ error: `request ${r.id} is ${r.status}, not pending_approval` }, 422)
      r.status = 'approved'
      r.reviewedAt = new Date().toISOString()
      r.reviewedBy = session.user ?? 'demo'
      const bp = w.catalogBlueprints.find((x) => x.id === r.blueprintId)
      const j = job('catalog-provision', `Provision ${r.vmName} from ${r.blueprintId}`, 4000, () => {
        provision(r, bp)
        return r.machineUuid ?? ''
      })
      return json(j, 202)
    }
    if (method === 'POST' && parts[3] === 'reject') {
      if (r.status !== 'pending_approval') return json({ error: `request ${r.id} is ${r.status}, not pending_approval` }, 422)
      r.status = 'rejected'
      r.reviewedAt = new Date().toISOString()
      r.reviewedBy = session.user ?? 'demo'
      r.reviewComment = body?.comment ?? ''
      return json(maskedRequest(r))
    }
    if (method === 'POST' && parts[3] === 'extend') {
      const lease = w.catalogLeases.find((l) => l.requestId === r.id)
      if (!lease) return json({ error: `request ${r.id} has no lease to extend` }, 422)
      const days = Number(body?.days)
      if (!Number.isFinite(days) || days <= 0) return json({ error: 'days must be a positive number' }, 422)
      // The total lease, counted from the order, can never exceed the blueprint maximum —
      // otherwise repeated extensions would bypass the cap ordering enforces.
      const bp = w.catalogBlueprints.find((x) => x.id === r.blueprintId)
      if (bp?.maxLeaseDays) {
        const newExpiry = Math.max(Date.parse(lease.expiry), Date.now()) + days * 86400e3
        const maxExpiry = (Date.parse(r.createdAt) || Date.now()) + bp.maxLeaseDays * 86400e3
        if (newExpiry > maxExpiry + 1000 || (r.leaseDays > 0 && r.leaseDays + days > bp.maxLeaseDays))
          return json({ error: `total lease duration exceeds the ${bp.id} maximum of ${bp.maxLeaseDays} days` }, 422)
      }
      lease.expiry = new Date(Math.max(Date.parse(lease.expiry), Date.now()) + days * 86400e3).toISOString()
      lease.lastStateChange = new Date().toISOString()
      r.leaseDays += days
      return json(lease)
    }
  }
  if (parts[0] === 'networks' && parts.length >= 2) {
    const n = w.networks.find((x) => x.name === parts[1])
    if (!n) return notFound('no such network')
    if (method === 'GET' && parts[2] === 'leases') return json({ leases: w.leases(n.name) })
    if (method === 'GET' && parts[2] === 'xml') return xml(w.networkXML(n))
    return method === 'GET' ? notFound() : json({ restartNeeded: false })
  }
  if (parts[0] === 'alerts' && parts[2] === 'ack' && method === 'POST') {
    const a = w.alerts.find((x) => x.id === parts[1])
    if (a) Object.assign(a, { ackedBy: session.user ?? 'demo', ackedAt: new Date().toISOString() })
    return none()
  }
  if (parts[0] === 'schedules' && parts.length >= 2) {
    const i = w.schedules.findIndex((s) => s.id === parts[1])
    if (method === 'DELETE' && i >= 0) w.schedules.splice(i, 1)
    if (method === 'POST' && parts[2] === 'run' && i >= 0) w.schedules[i].lastRun = new Date().toISOString()
    return none()
  }
  if (parts[0] === 'reports' && parts.length === 2 && method === 'GET') {
    return { status: 200, type: 'text/html; charset=utf-8', body: '<!doctype html><title>Report</title><p style="font:16px system-ui;padding:2rem">Reports are generated by a real VirtoMir host. This is the online demo.</p>' }
  }
  if (parts[0] === 'ha' && parts[1] === 'nodes' && parts[3] === 'simulate') {
    const node = w.HOSTS.find((h) => h.id === parts[2]) ?? w.SELF
    const st = w.haStatus()
    return json({
      node: { id: node.id, name: node.name },
      quorate: true,
      fencing: node.id === w.HOSTS[2].id ? 'IPMI power off through 10.20.0.113' : 'Redfish power off through the BMC',
      canRecover: true,
      steps: st.machines
        .filter((m) => m.owner === node.id)
        .map((m, i) => ({ order: i + 1, uuid: m.uuid, name: m.name, priority: m.priority, delaySec: m.restartDelaySec, target: m.targetNode ? w.HOSTS.find((h) => h.id === m.targetNode)?.name : w.HOSTS.find((h) => h.id !== node.id)?.name })),
    })
  }
  if (parts[0] === 'placement') {
    if (parts[1] === 'rules' && parts.length === 3) {
      const id = parts[2]
      const idx = demoPlacementRules.findIndex((r) => r.id === id)
      if (idx < 0) return notFound(`rule "${id}" not found`)
      if (method === 'PUT') {
        if (!body?.name?.trim()) return json({ error: 'rule name is required' }, 422)
        demoPlacementRules[idx] = { ...demoPlacementRules[idx], ...body, id }
        return json(demoPlacementRules[idx])
      }
      if (method === 'DELETE') {
        demoPlacementRules.splice(idx, 1)
        return none()
      }
    }
    if (parts[1] === 'hosts' && parts.length === 4 && parts[3] === 'labels' && method === 'PUT') {
      const id = parts[2]
      const host = w.HOSTS.find((h) => h.id === id)
      if (!host) return notFound(`host "${id}" not found`)
      demoHostLabels[id] = body?.labels || {}
      host.labels = demoHostLabels[id]
      return json({ labels: demoHostLabels[id] })
    }
  }
  if (parts[0] === 'cluster' && parts[1] === 'drs' && parts[2] === 'rules' && parts.length === 4 && method === 'DELETE') {
    const id = parts[3]
    const idx = demoPlacementRules.findIndex((r) => r.id === id)
    if (idx >= 0) demoPlacementRules.splice(idx, 1)
    return none()
  }

  if (parts[0] === 'vmware' && parts[1] === 'waves' && parts.length === 3 && method === 'GET') {
    const wave = demoWaves.find((w) => w.id === parts[2])
    return wave ? json(wave) : notFound('no such wave')
  }
  if (parts[0] === 'vmware' && parts[1] === 'waves' && parts.length === 4 && parts[3] === 'retry' && method === 'POST') {
    const wave = demoWaves.find((w) => w.id === parts[2])
    if (!wave) return notFound('no such wave')
    for (const it of wave.items) {
      if (it.state === 'failed') {
        it.state = 'running'
        it.message = 'Retrying conversion…'
      }
    }
    const j = job('vmware-wave-retry', `Retry wave ${wave.id}`, 3000, () => {
      for (const it of wave.items) {
        if (it.state === 'running' || it.state === 'failed') {
          it.state = 'done'
          it.message = ''
          addMachine(host.id, { name: it.name })
        }
      }
      return wave.id
    })
    return json(j, 202)
  }

  if (method === 'GET') return notFound()
  // Any other change: accepted, without effect (the demo has no real host behind it).
  return json({})
}

let session: { mode: 'pam'; user?: string; role?: 'admin' } = { mode: 'pam', user: 'demo', role: 'admin' }
