// The demo's pretend virtomird: answers /api/v1 requests from the in-memory world. Reads come
// from world.ts; the common changes (power actions, snapshots, new machines, clones, rename,
// delete, schedules, alerts) are applied to it, everything else answers "done" without effect.

import type { CreateSpec, DeploySpec, DomainState, Job, ScheduleInfo, SnapshotInfo } from '../api'
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

// ---- Background jobs ------------------------------------------------------------------------

const jobs = new Map<string, Job>()
let jobSeq = 0

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
    case op === 'PUT media' || op === 'POST disks' || op === 'POST interfaces' || op === 'POST hostdevs' || op === 'POST devices' || op === 'POST devices/detach':
      return json({ appliesOnNextBoot: false, target: 'vdb', liveError: '' })
    case method === 'POST' && rest[1] === 'disks' && rest[3] === 'resize':
      m.diskGiB = body?.sizeGiB ?? m.diskGiB
      return none()
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
        members: w.HOSTS.map((h) => ({ id: h.id, name: h.name, address: `https://10.20.0.1${w.HOSTS.indexOf(h) + 1}:8420`, fingerprint: 'SHA256:' + h.id.repeat(2).slice(0, 43), self: h.self, reachable: true, version: '0.6.0-demo' })),
      })
    case 'GET /api/v1/cluster/cpu-baseline':
      return json({ model: 'EPYC-Genoa', hosts: 3 })
    case 'POST /api/v1/cluster/invite':
      return json({ code: 'VMJ1-DEMO-7Q4K-PX2M-9WTE', address: 'https://10.20.0.11:8420', expires: new Date(Date.now() + 15 * 60e3).toISOString() })
    case 'POST /api/v1/cluster/join':
      return json({ error: DEMO_ONLY }, 422)

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

    // Storage and networks
    case 'GET /api/v1/storage/pools':
      return json({ pools: w.pools })
    case 'GET /api/v1/networks':
      return json({ networks: w.networks })

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

    // Host
    case 'GET /api/v1/host/devices':
      return json(host.self ? w.hardware : { iommu: true, pci: [], usb: [], mdevs: [] })
    case 'GET /api/v1/host/chassis':
      return json({ ...w.chassis, serial: w.chassis.serial + host.name.slice(-1) })
    case 'GET /api/v1/host/topology':
      return json(w.topology())
    case 'GET /api/v1/host/gpus':
      return json({ gpus: host.self ? w.gpus : [] })
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
      const r = { id: `rep-${Date.now()}`, kind: body?.kind ?? 'capacity', title: 'Capacity — cluster', format: body?.format ?? 'html', generatedAt: new Date().toISOString(), sizeBytes: 47100, summary: '3 hosts · 14 machines · N+1: yes', by: 'demo' }
      w.reports.unshift(r)
      return json(r)
    }
    case 'GET /api/v1/report-schedules':
      return json({ schedules: w.reportSchedules })
  }

  // Parametrised paths.
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
  if (parts[0] === 'ha' && parts[1] === 'nodes' && parts[3] === 'fence')
    return json({ nodeId: parts[2], driver: 'redfish', action: body?.action ?? 'status', power: 'on', message: 'Demo: the BMC was not contacted.', time: new Date().toISOString() })

  if (method === 'GET') return notFound()
  // Any other change: accepted, without effect (the demo has no real host behind it).
  return json({})
}

let session: { mode: 'pam'; user?: string; role?: 'admin' } = { mode: 'pam', user: 'demo', role: 'admin' }
