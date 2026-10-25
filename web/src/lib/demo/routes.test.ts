import { describe, expect, it, vi } from 'vitest'
import { handle } from './routes'

const call = (method: string, path: string, body?: unknown) => {
  const r = handle(method, new URL('http://demo' + path), body)
  return { status: r.status, json: r.body && r.type?.startsWith('application/json') ? JSON.parse(r.body) : r.body }
}

const WEB1 = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01'
const DB1 = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e03'
const CI = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06'

describe('demo API', () => {
  it('answers the pages the UI opens first', () => {
    for (const p of ['/api/v1/auth/session', '/api/v1/cluster', '/api/v1/domains', '/api/v1/storage/pools', '/api/v1/networks', '/api/v1/alerts', '/api/v1/ha/status', '/api/v1/projects']) {
      expect(call('GET', p).status, p).toBe(200)
    }
    expect(call('GET', `/api/v1/domains/${DB1}`).json.name).toBe('db-01')
    expect(call('GET', `/api/v1/domains/${DB1}/xml`).json).toContain('<name>db-01</name>')
    expect(call('GET', `/api/v1/domains/${WEB1}`).status).toBe(404)
    expect(call('GET', `/api/v1/hosts/b2c3d4e5f6071829/domains/${WEB1}`).json.name).toBe('web-01')
  })

  it('serves projects and moves machines into them', () => {
    expect(call('GET', '/api/v1/projects').json.projects).toHaveLength(2)
    expect(call('GET', '/api/v1/projects/prod').json.name).toBe('Production')
    expect(call('GET', '/api/v1/projects/nope').status).toBe(404)

    expect(call('PUT', `/api/v1/domains/${CI}/project`, { project: 'ml' }).status).toBe(204)
    expect(call('GET', `/api/v1/domains/${CI}`).json.project).toBe('ml')

    const created = call('POST', '/api/v1/projects', { id: 'test', name: 'Test', members: [], limits: { machines: 1, vcpus: 1, memoryMiB: 0, diskGiB: 0 }, storagePools: [], networks: [] })
    expect(created.status).toBe(201)
    expect(call('PUT', '/api/v1/projects/test', { ...created.json.project, name: 'Test 2' }).json.project.name).toBe('Test 2')
    expect(call('DELETE', '/api/v1/projects/test').status).toBe(204)
    expect(call('GET', '/api/v1/projects/test').status).toBe(404)
  })

  it('scopes /hosts/<id>/ to that member', () => {
    const own = call('GET', '/api/v1/domains').json.domains.map((d: { name: string }) => d.name)
    const other = call('GET', '/api/v1/hosts/b2c3d4e5f6071829/domains').json.domains.map((d: { name: string }) => d.name)
    expect(own).toContain('db-01')
    expect(other).toContain('k8s-cp-01')
    expect(other).toContain('web-01')
    expect(other).not.toContain('db-01')
    expect(call('GET', '/api/v1/hosts/0000000000000000/domains').status).toBe(404)
  })

  it('serves placement rules, labels, violations and check', () => {
    const p = call('GET', '/api/v1/placement').json
    expect(p.rules.length).toBeGreaterThanOrEqual(3)
    expect(p.hostLabels['a1b2c3d4e5f60718']).toEqual({ rack: 'r1', zone: 'zone-a' })

    const viols = call('GET', '/api/v1/placement/violations').json
    expect(viols.some((v: any) => v.hard && v.kind === 'vm_anti_affinity')).toBe(true)

    const checkHard = call('POST', '/api/v1/placement/check', { domainUuid: WEB1, hostId: 'b2c3d4e5f6071829' }).json
    expect(checkHard.ok).toBe(false)
    expect(checkHard.hard.length).toBeGreaterThan(0)

    const checkClean = call('POST', '/api/v1/placement/check', { domainUuid: WEB1, hostId: 'c3d4e5f60718293a' }).json
    expect(checkClean.ok).toBe(true)

    // Rule CRUD
    const newRule = call('POST', '/api/v1/placement/rules', {
      name: 'Test Anti',
      kind: 'vm_anti_affinity',
      strength: 'must',
      domains: [WEB1, CI],
    })
    expect(newRule.status).toBe(201)
    const ruleId = newRule.json.id

    const updated = call('PUT', `/api/v1/placement/rules/${ruleId}`, {
      ...newRule.json,
      name: 'Test Anti Updated',
    })
    expect(updated.status).toBe(200)
    expect(updated.json.name).toBe('Test Anti Updated')

    expect(call('DELETE', `/api/v1/placement/rules/${ruleId}`).status).toBe(204)

    // Host labels
    const setLbl = call('PUT', '/api/v1/placement/hosts/a1b2c3d4e5f60718/labels', {
      labels: { rack: 'r1', row: 'row-3' },
    })
    expect(setLbl.status).toBe(200)
    expect(setLbl.json.labels.row).toBe('row-3')

    // DRS rules consistency
    const drs = call('GET', '/api/v1/cluster/drs').json
    expect(drs.rules.length).toBeGreaterThanOrEqual(3)
    expect(drs.rules.some((r: any) => r.name === 'Separate web frontends')).toBe(true)

    // Inventory machines endpoint contract
    const inv = call('GET', '/api/v1/cluster/inventory/machines').json
    expect(Array.isArray(inv.domains)).toBe(true)
    expect(inv.domains.length).toBeGreaterThanOrEqual(3)
  })

  it('exports showback safely and with the configured currency', () => {
    const csv = call('GET', '/api/v1/catalog/showback?format=csv')
    expect(csv.status).toBe(200)
    // Every data row is fully quoted so spreadsheet formula injection is impossible.
    for (const line of String(csv.json).split('\n').slice(1)) {
      expect(line.startsWith('"')).toBe(true)
      expect(line.endsWith('"')).toBe(true)
    }
    const html = call('GET', '/api/v1/catalog/showback?format=html')
    expect(html.status).toBe(200)
    expect(String(html.json)).not.toContain('NaN')
    // The configured pricing currency symbol ($), not a fixed USD locale format.
    expect(String(html.json)).toMatch(/\$\d+\.\d{2}/)
  })

  it('keeps a hostile machine name one quoted CSV cell', () => {
    const bp = call('GET', '/api/v1/catalog/blueprints').json.blueprints[0]
    const name = '=SUM(1,1)","alert(1)"'
    const ok = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: name,
      presetId: bp.presets[0].id,
      leaseDays: 7,
      fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nz' },
    })
    expect(ok.status).toBe(201)
    const csv = String(call('GET', '/api/v1/catalog/showback?format=csv').json)
    // One quoted cell: doubled inner quotes stop CSV breakout, the leading apostrophe
    // stops the '=' from running as a spreadsheet formula.
    expect(csv).toContain(`"'=SUM(1,1)"",""alert(1)""`)
    expect(call('DELETE', `/api/v1/catalog/requests/${ok.json.id}`).status).toBe(204)
  })

  it('escapes hostile project names in the HTML export', () => {
    const before = call('GET', '/api/v1/projects/prod').json
    call('PUT', '/api/v1/projects/prod', { ...before, name: '<script>alert(1)</script>' })
    const html = String(call('GET', '/api/v1/catalog/showback?format=html').json)
    call('PUT', '/api/v1/projects/prod', { ...before })
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('escapes hostile currency in HTML export and rejects hostile currency charset in pricing PUT', () => {
    // PUT /catalog/pricing rejects hostile currency symbols
    const bad = call('PUT', '/api/v1/catalog/pricing', {
      vcpuHour: 0.04,
      ramGiBHour: 0.01,
      diskGiBMonth: 0.08,
      currency: '<script>alert(1)</script>',
    })
    expect(bad.status).toBe(422)

    // PUT /catalog/pricing accepts valid currency symbols (e.g. €, $, ₺, USD)
    const ok = call('PUT', '/api/v1/catalog/pricing', {
      vcpuHour: 0.04,
      ramGiBHour: 0.01,
      diskGiBMonth: 0.08,
      currency: '€',
    })
    expect(ok.status).toBe(200)

    // Showback HTML uses the new currency
    const html = String(call('GET', '/api/v1/catalog/showback?format=html').json)
    expect(html).toContain('€')

    // Reset back to $
    call('PUT', '/api/v1/catalog/pricing', {
      vcpuHour: 0.04,
      ramGiBHour: 0.01,
      diskGiBMonth: 0.08,
      currency: '$',
    })
  })

  it('enforces required cloud-init fields when ordering', () => {
    const bp = call('GET', '/api/v1/catalog/blueprints').json.blueprints.find((b: any) => b.id === 'web-server-ubuntu')
    // Missing required sshKey
    const missing = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: 'test-no-ssh',
      presetId: bp.presets[0].id,
      leaseDays: 7,
      fieldValues: {},
    })
    expect(missing.status).toBe(422)
    expect(missing.json.error).toContain('is required')

    // Providing required sshKey succeeds
    const ok = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: 'test-with-ssh',
      presetId: bp.presets[0].id,
      leaseDays: 7,
      fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nz' },
    })
    expect(ok.status).toBe(201)
    expect(call('DELETE', `/api/v1/catalog/requests/${ok.json.id}`).status).toBe(204)
  })

  it('filters requests by requester query parameter', () => {
    const denizReqs = call('GET', '/api/v1/catalog/requests?requester=deniz').json.requests
    expect(denizReqs.length).toBeGreaterThan(0)
    for (const r of denizReqs) {
      expect(r.requester).toBe('deniz')
    }

    const none = call('GET', '/api/v1/catalog/requests?requester=nonexistent-user').json.requests
    expect(none).toEqual([])
  })

  it('masks password answers in every request response', () => {
    const bp = call('GET', '/api/v1/catalog/blueprints').json.blueprints.find((b: any) => b.id === 'postgres-16-appliance')
    const made = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: 'pg-secret-test',
      presetId: bp.presets[0].id,
      fieldValues: { dbName: 'reporting', adminPassword: 'hunter2' },
    })
    expect(made.status).toBe(201)
    expect(made.json.fieldValues.adminPassword).toBe('••••••••')
    expect(made.json.fieldValues.dbName).toBe('reporting')
    const list = call('GET', '/api/v1/catalog/requests?project=prod').json.requests
    expect(list.find((r: any) => r.id === made.json.id).fieldValues.adminPassword).toBe('••••••••')
    const single = call('GET', `/api/v1/catalog/requests/${made.json.id}`).json.request
    expect(single.fieldValues.adminPassword).toBe('••••••••')
    expect(single.fieldValues.dbName).toBe('reporting')
    expect(call('DELETE', `/api/v1/catalog/requests/${made.json.id}`).status).toBe(204)
  })

  it('validates blueprint bodies on create and update', () => {
    const list = call('GET', '/api/v1/catalog/blueprints').json.blueprints
    expect(list.length).toBeGreaterThan(0)
    const bp = list[0]
    expect(call('PUT', `/api/v1/catalog/blueprints/${bp.id}`, { ...bp, presets: [] }).status).toBe(422)
    expect(
      call('PUT', `/api/v1/catalog/blueprints/${bp.id}`, {
        ...bp,
        presets: [{ id: 'x', name: '', vcpus: 2, memoryMiB: 2048, diskGiB: 20 }],
      }).status,
    ).toBe(422)
    expect(call('PUT', `/api/v1/catalog/blueprints/${bp.id}`, { ...bp, defaultPresetId: 'missing' }).status).toBe(422)
    // Create rejects the same broken shapes, so a preset-less blueprint can never crash an order.
    expect(
      call('POST', '/api/v1/catalog/blueprints', {
        id: 'no-presets',
        name: 'No Presets',
        sourceType: 'template',
        sourceRef: 'tmpl-x',
        presets: [],
        requireApproval: false,
      }).status,
    ).toBe(422)
    // defaultLeaseDays cannot exceed maxLeaseDays
    expect(
      call('POST', '/api/v1/catalog/blueprints', {
        id: 'bad-lease',
        name: 'Bad Lease',
        sourceType: 'template',
        sourceRef: 'tmpl-x',
        presets: [{ id: 's', name: 'S', vcpus: 1, memoryMiB: 1024, diskGiB: 10 }],
        defaultLeaseDays: 60,
        maxLeaseDays: 30,
        requireApproval: false,
      }).status,
    ).toBe(422)
    const made = call('POST', '/api/v1/catalog/blueprints', {
      id: 'orderable',
      name: 'Orderable',
      sourceType: 'template',
      sourceRef: 'tmpl-x',
      presets: [{ id: 's', name: 'S', vcpus: 1, memoryMiB: 1024, diskGiB: 10 }],
      requireApproval: false,
    })
    expect(made.status).toBe(201)
    expect(made.json.blueprint.defaultPresetId).toBe('s') // falls back to the first preset
    expect(call('DELETE', '/api/v1/catalog/blueprints/orderable').status).toBe(204)
  })

  it('enforces maxLeaseDays when ordering and extending', () => {
    const bp = call('GET', '/api/v1/catalog/blueprints').json.blueprints[0]
    const over = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: 'lease-test',
      presetId: bp.presets[bp.presets.length - 1].id,
      leaseDays: bp.maxLeaseDays + 10,
      fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nz' },
    })
    expect(over.status).toBe(422)
    expect(over.json.error).toContain('exceeds')

    const ok = call('POST', '/api/v1/catalog/requests', {
      blueprintId: bp.id,
      projectId: 'prod',
      vmName: 'lease-test-2',
      presetId: bp.presets[bp.presets.length - 1].id,
      leaseDays: 7,
      fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nz' },
    })
    expect(ok.status).toBe(201)
    const reqId = ok.json.id
    const ext = call('POST', `/api/v1/catalog/requests/${reqId}/extend`, { days: 365 })
    expect(ext.status).toBe(422)
    expect(call('POST', `/api/v1/catalog/requests/${reqId}/extend`, { days: 7 }).status).toBe(200)

    // Repeated extensions cannot outgrow the maximum either: the total counts from the order.
    const sandbox = call('GET', '/api/v1/catalog/blueprints').json.blueprints.find((b: any) => b.id === 'dev-sandbox')
    const order = call('POST', '/api/v1/catalog/requests', {
      blueprintId: sandbox.id,
      projectId: 'prod',
      vmName: 'lease-total-test',
      presetId: sandbox.presets[0].id,
      leaseDays: 7,
      fieldValues: { sshKey: 'ssh-ed25519 AAAAC3Nz' },
    })
    expect(order.status).toBe(201)
    const totalId = order.json.id
    expect(call('POST', `/api/v1/catalog/requests/${totalId}/extend`, { days: 7 }).status).toBe(200) // 7 + 7 = 14 = max
    const overTotal = call('POST', `/api/v1/catalog/requests/${totalId}/extend`, { days: 7 })
    expect(overTotal.status).toBe(422)
    expect(overTotal.json.error).toContain('total lease')
    expect(call('DELETE', `/api/v1/catalog/requests/${totalId}`).status).toBe(204)
    expect(call('DELETE', `/api/v1/catalog/requests/${reqId}`).status).toBe(204)
  })

  it('applies power actions after a moment', () => {
    vi.useFakeTimers()
    expect(call('POST', `/api/v1/domains/${CI}/actions/start`).status).toBe(204)
    vi.advanceTimersByTime(2000)
    expect(call('GET', `/api/v1/domains/${CI}`).json.state).toBe('running')
    vi.useRealTimers()
  })

  it('serves history in the requested resolution', () => {
    const r = call('GET', `/api/v1/history?range=24h&series=host.cpu,vm.${WEB1}.cpu`).json
    expect(r.step).toBe(60)
    expect(r.series['host.cpu']).toHaveLength(1440)
    expect(Math.max(...r.series['host.cpu'])).toBeLessThanOrEqual(100)
    expect(call('GET', '/api/v1/history?range=2y&series=host.cpu').status).toBe(400)
  })

  it('serves VMware migration demo endpoints', () => {
    // 1. Connection
    const conn = call('GET', '/api/v1/vmware/connection').json
    expect(conn.url).toBe('https://vcenter.demo.internal')
    expect(conn.password).toBe('••••••••')

    const testRes = call('POST', '/api/v1/vmware/connection/test', {}).json
    expect(testRes.ok).toBe(true)

    // 2. Inventory
    const inv = call('GET', '/api/v1/vmware/inventory').json
    expect(inv.datacenters).toHaveLength(2)
    expect(inv.hosts).toHaveLength(2)
    expect(inv.vms.length).toBeGreaterThanOrEqual(4)
    expect(inv.vms.some((v: { powerState: string }) => v.powerState === 'off')).toBe(true)
    expect(inv.vms.some((v: { powerState: string }) => v.powerState === 'on')).toBe(true)

    // 3. Preflight
    const pre = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101', 'vm-104'], pool: 'default' }).json
    expect(pre.pool).toBe('default')
    expect(pre.issues['vm-101']).toHaveLength(0)
    expect(pre.issues['vm-104'][0]).toContain('Warning:')

    // 4. Wave import
    const waveJob = call('POST', '/api/v1/vmware/waves', { vmIds: ['vm-101', 'vm-102'], pool: 'default', parallel: 2 })
    expect(waveJob.status).toBe(202)
    const wavesList = call('GET', '/api/v1/vmware/waves').json.waves
    expect(wavesList.length).toBeGreaterThanOrEqual(1)
    const firstWave = wavesList[0]
    expect(call('GET', `/api/v1/vmware/waves/${firstWave.id}`).status).toBe(200)

    // 5. Single import
    const singleJob = call('POST', '/api/v1/vmware/import', { vmId: 'vm-101', pool: 'default', name: 'demo-imported-vm' })
    expect(singleJob.status).toBe(202)
  })

  it('validates storage pool during preflight and handles wave failures with retry', () => {
    // Preflight pool validations
    const emptyPoolRes = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: '' })
    expect(emptyPoolRes.status).toBe(422)

    const unknownPoolRes = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: 'non-existent-pool' })
    expect(unknownPoolRes.status).toBe(422)
    expect(unknownPoolRes.json.error).toContain('non-existent-pool')

    const validPre = call('POST', '/api/v1/vmware/preflight', { vmIds: ['vm-101'], pool: 'default' })
    expect(validPre.status).toBe(200)
    expect(validPre.json.pool).toBe('default')
    expect(validPre.json.poolFreeBytes).toBeGreaterThan(0)
    expect(validPre.json.poolPath).toBe('/var/lib/libvirt/images')

    // Wave partial failure and retry workflow
    vi.useFakeTimers()
    const waveRes = call('POST', '/api/v1/vmware/waves', { vmIds: ['vm-101', 'vm-104'], pool: 'default' })
    expect(waveRes.status).toBe(202)
    const waveList = call('GET', '/api/v1/vmware/waves').json.waves
    const wave = waveList[0]
    expect(wave).toBeDefined()

    // Advance timers so wave migration completes
    vi.advanceTimersByTime(4000)

    const finishedWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm101 = finishedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-101')
    const vm104 = finishedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm101?.state).toBe('done')
    expect(vm104?.state).toBe('failed')
    expect(vm104?.message).toContain('VirtIO')

    // Retry the failed item in the wave
    const retryRes = call('POST', `/api/v1/vmware/waves/${wave.id}/retry`, {})
    expect(retryRes.status).toBe(202)

    // Items being retried transition to running
    const retryingWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm104Retrying = retryingWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm104Retrying?.state).toBe('running')

    // Advance timer so retry completes
    vi.advanceTimersByTime(3500)

    const completedWave = call('GET', `/api/v1/vmware/waves/${wave.id}`).json
    const vm104Done = completedWave.items.find((it: { vmId: string }) => it.vmId === 'vm-104')
    expect(vm104Done?.state).toBe('done')
    expect(completedWave.items.every((it: { state: string }) => it.state === 'done')).toBe(true)

    // Check machine was added
    const domains = call('GET', '/api/v1/domains').json.domains
    expect(domains.some((d: { name: string }) => d.name === 'win-dc-01')).toBe(true)

    vi.useRealTimers()
  })

  it('serves compliance posture, FIPS info and audit verification per host', () => {
    // Checks (same shape the real GET /api/v1/compliance/checks returns).
    const checks = call('GET', '/api/v1/compliance/checks').json
    expect(checks.hostId).toBe('a1b2c3d4e5f60718')
    expect(checks.totalCount).toBe(checks.results.length)
    expect(checks.score).toBe(Math.round((checks.passedCount * 100) / checks.totalCount))
    expect(checks.results.some((c: any) => c.category === 'SSH' && c.benchmark.length > 0)).toBe(true)

    // A weaker host elsewhere in the cluster.
    const other = call('GET', '/api/v1/hosts/c3d4e5f60718293a/compliance/checks').json
    expect(other.failedCount).toBeGreaterThan(1)
    expect(other.score).toBeLessThan(checks.score)

    // Running a scan refreshes evaluatedAt.
    const before = call('GET', '/api/v1/compliance/checks').json.evaluatedAt
    vi.useFakeTimers()
    const scan = call('POST', '/api/v1/compliance/checks/run').json
    expect(new Date(scan.evaluatedAt).getTime()).toBeGreaterThan(new Date(before).getTime())
    vi.useRealTimers()

    // Cluster aggregation: one entry per member.
    const cluster = call('GET', '/api/v1/cluster/compliance').json
    expect(cluster).toHaveLength(3)
    expect(cluster[0].hostName).toBe('hv-01')
    expect(cluster.every((c: any) => c.results.length > 0)).toBe(true)

    // FIPS profile
    const fips = call('GET', '/api/v1/compliance/fips').json
    expect(fips.kernelEnabled).toBe(true)
    expect(fips.enforced).toBe(true)
    expect(fips.tlsMinVersion).toBe('TLS 1.2')
    expect(fips.cipherSuites.length).toBeGreaterThan(0)
    expect(fips.curves).toEqual(['P-256', 'P-384', 'P-521'])

    // Audit log verification: intact here, tampered on hv-03.
    const audit = call('POST', '/api/v1/activity/verify').json
    expect(audit.valid).toBe(true)
    expect(audit.verifiedCount).toBeGreaterThan(0)
    expect(audit.lastSeq).toBeGreaterThan(0)
    expect(audit.headHash).toMatch(/^[0-9a-f]{64}$/)
    const tampered = call('POST', '/api/v1/hosts/c3d4e5f60718293a/activity/verify').json
    expect(tampered.valid).toBe(false)
    expect(tampered.tamperedSeq).toBeGreaterThan(1)
    expect(tampered.reason.length).toBeGreaterThan(0)

    // A compliance report is generated like any other kind, with its own title.
    const rep = call('POST', '/api/v1/reports', { kind: 'compliance', format: 'csv' }).json
    expect(rep.kind).toBe('compliance')
    expect(rep.title).toBe('Security compliance')
    expect(rep.format).toBe('csv')
  })

  it('exports the audit log as JSONL and CSV text', () => {
    const jsonl = handle('GET', new URL('http://demo/api/v1/activity/export?format=jsonl'), null)
    expect(jsonl.status).toBe(200)
    expect(jsonl.type).toContain('application/jsonl')
    const lines = (jsonl.body as string).split('\n')
    expect(lines.length).toBeGreaterThan(1)
    for (const l of lines) expect(() => JSON.parse(l)).not.toThrow()

    const csv = handle('GET', new URL('http://demo/api/v1/activity/export?format=csv'), null)
    expect(csv.type).toContain('text/csv')
    const rows = (csv.body as string).split('\n')
    expect(rows[0]).toBe('time,user,method,path,status,error')
    expect(rows.length).toBe(lines.length + 1)
  })

  it('manages content library items, versions, config, sync, and deploy', () => {
    // 1. List items
    const listRes = call('GET', '/api/v1/content/items')
    expect(listRes.status).toBe(200)
    expect(listRes.json.items.length).toBeGreaterThanOrEqual(3)
    const u2404 = listRes.json.items.find((it: { id: string }) => it.id === 'ci-ubuntu-2404')
    expect(u2404).toBeDefined()
    expect(u2404.type).toBe('image')
    expect(u2404.syncStatus).toBe('ready')
    expect(u2404.latestVersion.version).toBe('24.04.1')

    // 2. Publish new item
    const publishRes = call('POST', '/api/v1/content/items', {
      name: 'Alpine 3.20 Standard',
      type: 'iso',
      description: 'Lightweight security-oriented Linux',
      version: '3.20.0',
      keepVersions: 3,
      syncPolicy: { mode: 'all' },
    })
    expect(publishRes.status).toBe(201)
    const newItem = publishRes.json.item
    expect(newItem.name).toBe('Alpine 3.20 Standard')
    expect(newItem.versions).toHaveLength(1)

    // 3. Get item by id
    const getRes = call('GET', `/api/v1/content/items/${newItem.id}`)
    expect(getRes.status).toBe(200)
    expect(getRes.json.name).toBe('Alpine 3.20 Standard')

    // 4. Update item
    const updateRes = call('PUT', `/api/v1/content/items/${newItem.id}`, {
      description: 'Updated description for Alpine',
    })
    expect(updateRes.status).toBe(200)
    expect(updateRes.json.item.description).toBe('Updated description for Alpine')

    // 5. Publish new version
    const verRes = call('POST', `/api/v1/content/items/${newItem.id}/versions`, {
      version: '3.20.1',
      description: 'Point release with bug fixes',
    })
    expect(verRes.status).toBe(201)
    expect(verRes.json.version.version).toBe('3.20.1')
    expect(verRes.json.item.versions).toHaveLength(2)

    // 6. Delete a version
    const delVerRes = call('DELETE', `/api/v1/content/items/${newItem.id}/versions/${verRes.json.version.id}`)
    expect(delVerRes.status).toBe(204)
    const itemAfterVerDel = call('GET', `/api/v1/content/items/${newItem.id}`).json
    expect(itemAfterVerDel.versions).toHaveLength(1)

    // 7. Sync content
    const syncRes = call('POST', `/api/v1/content/items/${newItem.id}/sync`, {})
    expect(syncRes.status).toBe(202)
    expect(syncRes.json.kind).toBe('content-sync')

    // 8. Deploy from content
    vi.useFakeTimers()
    const deployRes = call('POST', `/api/v1/content/items/${newItem.id}/deploy`, {
      name: 'alpine-vm-01',
      pool: 'default',
      vcpus: 1,
      memoryMiB: 1024,
      start: true,
    })
    expect(deployRes.status).toBe(202)
    expect(deployRes.json.kind).toBe('content-deploy')

    // Advance timer so deployment completes
    vi.advanceTimersByTime(4000)
    const domains = call('GET', '/api/v1/domains').json.domains
    expect(domains.some((d: { name: string }) => d.name === 'alpine-vm-01')).toBe(true)
    vi.useRealTimers()

    // 9. Delete item
    const delRes = call('DELETE', `/api/v1/content/items/${newItem.id}`)
    expect(delRes.status).toBe(204)
    expect(call('GET', `/api/v1/content/items/${newItem.id}`).status).toBe(404)

    // 10. Get and Update Config
    const cfgRes = call('GET', '/api/v1/content/config')
    expect(cfgRes.status).toBe(200)
    expect(cfgRes.json.targetPool).toBe('default')

    const saveCfgRes = call('PUT', '/api/v1/content/config', {
      targetPool: 'fast-ssd',
      autoSync: false,
    })
    expect(saveCfgRes.status).toBe(200)
    expect(saveCfgRes.json.targetPool).toBe('fast-ssd')
    expect(saveCfgRes.json.autoSync).toBe(false)
  })

  it('runs a rolling upgrade', () => {
    const c = call('GET', '/api/v1/cluster').json
    expect(c.mixedVersions).toBe(true)
    expect(c.minVersion).toBe('0.5.9-demo')
    expect(call('GET', '/api/v1/cluster/lcm').json.plans[0].state).toBe('succeeded')
    expect(call('POST', '/api/v1/cluster/lcm/precheck', {}).json.checks).toHaveLength(3)

    vi.useFakeTimers()
    try {
      const plan = call('POST', '/api/v1/cluster/lcm/start', { moveBack: true })
      expect(plan.status).toBe(202)
      expect(call('POST', '/api/v1/cluster/lcm/start', {}).status).toBe(409)
      vi.advanceTimersByTime(10_000)
      const mid = call('GET', `/api/v1/cluster/lcm/plans/${plan.json.id}`).json
      expect(mid.state).toBe('running')
      expect(mid.steps.length).toBeGreaterThan(2)
      expect(call('POST', `/api/v1/cluster/lcm/plans/${plan.json.id}/pause`).json.state).toBe('paused')
      expect(call('POST', `/api/v1/cluster/lcm/plans/${plan.json.id}/resume`).json.state).toBe('running')
      vi.advanceTimersByTime(60_000)
      expect(call('GET', `/api/v1/cluster/lcm/plans/${plan.json.id}`).json.state).toBe('succeeded')
      expect(call('GET', '/api/v1/cluster').json.mixedVersions).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('serves GPU pools and places GPUs', () => {
    const pools = call('GET', '/api/v1/gpu/pools').json.pools
    expect(pools.map((p: { id: string }) => p.id)).toEqual(['l40s', 'l40s-12q', 'a100-1g'])
    const l40s = pools[0]
    expect(l40s.total).toBe(2)
    expect(l40s.free).toBe(1)
    expect(call('GET', '/api/v1/cluster/gpus').json.gpus.length).toBe(4)
    expect(call('POST', '/api/v1/gpu/placement', { poolId: 'l40s', count: 1 }).json.best.hostName).toBe('hv-02')
    expect(call('POST', '/api/v1/gpu/placement', { poolId: 'l40s', count: 3 }).status).toBe(204)
    expect(call('POST', '/api/v1/gpu/pools', { id: 'l40s', name: 'x', model: 'x', kind: 'whole' }).status).toBe(409)
    expect(call('POST', '/api/v1/gpu/pools', { id: 'h100', name: 'H100', model: 'H100', kind: 'whole' }).status).toBe(201)
    expect(call('PUT', '/api/v1/gpu/pools/h100', { name: 'H100 SXM' }).json.name).toBe('H100 SXM')
    expect(call('GET', '/api/v1/gpu/pools/l40s/availability').json.hosts[0].hostName).toBe('hv-02')
    expect(call('DELETE', '/api/v1/gpu/pools/h100').status).toBe(204)
    expect(call('GET', '/api/v1/gpu/pools/h100').status).toBe(404)
  })

  it('runs a Ceph cluster from hv-01 with storage policies', () => {
    const st = call('GET', '/api/v1/ceph/status').json
    expect(st.managed).toBe(true)
    expect(st.telemetry.health.status).toBe('HEALTH_OK')
    expect(st.telemetry.daemons.osds).toBe(6)
    expect(call('GET', '/api/v1/hosts/b2c3d4e5f6071829/ceph/status').json.managedBy.name).toBe('hv-01')
    expect(call('GET', '/api/v1/hosts/b2c3d4e5f6071829/ceph/pools').status).toBe(409)
    expect(call('GET', '/api/v1/ceph/pools').json.pools).toHaveLength(2)

    const full = call('POST', '/api/v1/ceph/osds', { hostname: 'hv-03', disk: '/dev/sdd' })
    expect(full.status).toBe(422)
    expect(full.json.error).toContain('wipefs')
    expect(call('POST', '/api/v1/ceph/osds', { hostname: 'hv-02', disk: '/dev/sdc' }).status).toBe(202)

    expect(call('POST', '/api/v1/ceph/maintenance/enter', { hostname: 'hv-02' }).status).toBe(204)
    const warn = call('GET', '/api/v1/ceph/status').json.telemetry
    expect(warn.health.status).toBe('HEALTH_WARN')
    expect(warn.daemons.noout).toBe(true)
    expect(call('POST', '/api/v1/ceph/maintenance/exit', { hostname: 'hv-02' }).status).toBe(204)
    expect(call('GET', '/api/v1/ceph/status').json.telemetry.health.status).toBe('HEALTH_OK')

    expect(call('POST', '/api/v1/ceph/pools', { name: 'rbd-fast', type: 'replicated', replicaSize: 3, libvirtPool: 'ceph-fast' }).status).toBe(201)
    expect(call('GET', '/api/v1/storage/pools').json.pools.some((p: { name: string }) => p.name === 'ceph-fast')).toBe(true)
    expect(call('DELETE', '/api/v1/ceph/pools/rbd-fast').status).toBe(409)

    expect(call('GET', '/api/v1/storage/policies').json.policies).toHaveLength(3)
    expect(call('POST', '/api/v1/storage/policies', { id: 'fast', name: 'Fast', pool: 'ceph-fast', default: true }).status).toBe(201)
    const list = call('GET', '/api/v1/storage/policies').json.policies
    expect(list.filter((p: { default: boolean }) => p.default).map((p: { id: string }) => p.id)).toEqual(['fast'])
    expect(call('DELETE', '/api/v1/storage/policies/fast').status).toBe(204)
  })

  it('shows storage efficiency, tiers and NVMe/TCP', () => {
    expect(call('GET', '/api/v1/storage/tiers').json.tiers['fast-zfs']).toBe('nvme')
    expect(call('GET', '/api/v1/storage/pools/fast-zfs/efficiency').json.zfs.compressRatio).toBe(1.48)
    expect(call('GET', '/api/v1/storage/pools/ceph-cold/efficiency').json.ceph.compressionMode).toBe('aggressive')
    expect(call('GET', '/api/v1/storage/pools/default/efficiency').json.kind).toBe('none')
    expect(call('PUT', '/api/v1/storage/pools/fast-zfs/zfs', { dedup: 'on' }).status).toBe(422)
    expect(call('PUT', '/api/v1/storage/pools/fast-zfs/zfs', { compression: 'zstd' }).json.compression).toBe('zstd')
    expect(call('PUT', '/api/v1/storage/tiers/iso', { tier: 'hdd' }).status).toBe(204)
    expect(call('GET', '/api/v1/storage/tiers').json.tiers.iso).toBe('hdd')

    expect(call('POST', `/api/v1/domains/${DB1}/disks/vda/sparsify`).status).toBe(409)
    expect(call('POST', `/api/v1/domains/${DB1}/disks/vda/tier`, { tier: 'hdd' }).status).toBe(202)

    const found = call('POST', '/api/v1/storage/nvme/discover', { portal: '192.168.77.50' }).json.targets
    expect(found).toHaveLength(2)
    expect(call('POST', '/api/v1/storage/nvme/connect', found[0]).status).toBe(204)
    const subs = call('GET', '/api/v1/storage/nvme').json.subsystems
    expect(subs.find((s: { nqn: string }) => s.nqn === found[0].nqn).managed).toBe(true)
    expect(call('POST', '/api/v1/storage/nvme/disconnect', { nqn: found[0].nqn }).status).toBe(204)
  })

  it('shows IP pools, dynamic DNS, BGP and SR-IOV', () => {
    expect(call('GET', '/api/v1/physnet/ip-pools').json.pools).toHaveLength(2)
    const r = call('POST', '/api/v1/physnet/ip-pools/prod-v20/allocations', { name: 'printer' })
    expect(r.status).toBe(201)
    expect(r.json.ip).toBe('10.20.0.102')
    expect(call('POST', '/api/v1/physnet/ip-pools/prod-v20/check', { ip: '10.20.0.102' }).json.free).toBe(false)
    expect(call('DELETE', '/api/v1/physnet/ip-pools/prod-v20').status).toBe(409)
    expect(call('DELETE', '/api/v1/physnet/ip-pools/lab-v30').status).toBe(204)
    expect(call('GET', '/api/v1/physnet/dns').json.config.zone).toBe('prod.example.')
    expect(call('POST', '/api/v1/physnet/dns/test').json.ok).toBe(true)
    expect(call('GET', '/api/v1/physnet/bgp').json.sessions[0].state).toBe('Established')
    expect(call('PUT', '/api/v1/physnet/bgp', { enabled: true, localAs: 0 }).status).toBe(422)
    expect(call('PUT', '/api/v1/physnet/sriov/ens1f1', { vfs: 8, persistent: true }).status).toBe(204)
    expect(call('GET', '/api/v1/physnet/sriov').json.devices[1].vfs).toHaveLength(8)
    expect(call('PUT', '/api/v1/physnet/sriov/ens1f1', { vfs: 99 }).status).toBe(422)
  })


  it('runs Kubernetes clusters', () => {
    expect(call('GET', '/api/v1/k8s/clusters').json.clusters[0].nodes).toHaveLength(6)
    expect(call('POST', '/api/v1/k8s/clusters', { name: 'Bad!' }).status).toBe(422)
    const c = call('POST', '/api/v1/k8s/clusters', { name: 'dev', controlPlanes: 1, workers: 2, version: '1.35' }).json
    expect(c.nodes.map((n: { name: string }) => n.name)).toEqual(['dev-cp-1', 'dev-worker-1', 'dev-worker-2'])
    expect(call('POST', `/api/v1/k8s/clusters/${c.id}/scale`, { workers: 1 }).json.nodes).toHaveLength(2)
    expect(call('POST', `/api/v1/k8s/clusters/${c.id}/upgrade`).json.version).toBe('1.36')
    expect(call('GET', `/api/v1/k8s/clusters/${c.id}/kubeconfig`).status).toBe(200)
    expect(call('DELETE', `/api/v1/k8s/clusters/${c.id}`).status).toBe(202)
    expect(call('GET', `/api/v1/k8s/clusters/${c.id}`).status).toBe(404)
  })


  it('registers and installs bare-metal hosts', () => {
    const v = call('GET', '/api/v1/baremetal').json
    expect(v.hosts.map((h: { state: string }) => h.state)).toEqual(['joined', 'registered'])
    expect(call('POST', '/api/v1/baremetal/hosts', { name: 'Bad Name', mac: '52:54:00:00:00:01' }).status).toBe(422)
    const h = call('POST', '/api/v1/baremetal/hosts', { name: 'hv-06', mac: '52:54:00:00:00:06', bmc: { driver: '' }, password: 'long-enough' }).json
    expect(call('POST', `/api/v1/baremetal/hosts/${h.id}/install`).json.state).toBe('installing')
    expect(call('POST', `/api/v1/baremetal/hosts/${h.id}/power`, { action: 'status' }).status).toBe(409)
    expect(call('POST', `/api/v1/baremetal/hosts/${h.id}/cancel`).json.state).toBe('registered')
    expect(call('POST', `/api/v1/baremetal/hosts/${v.hosts[0].id}/install`).status).toBe(409)
    expect(call('DELETE', `/api/v1/baremetal/hosts/${h.id}`).status).toBe(204)
  })
})
