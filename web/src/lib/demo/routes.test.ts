import { describe, expect, it, vi } from 'vitest'
import { handle } from './routes'

const call = (method: string, path: string, body?: unknown) => {
  const r = handle(method, new URL('http://demo' + path), body)
  return { status: r.status, json: r.body && r.type?.startsWith('application/json') ? JSON.parse(r.body) : r.body }
}

const WEB1 = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e01'
const CI = '4f1c2a7e-9b3d-4c21-8e5f-0a1b2c3d4e06'

describe('demo API', () => {
  it('answers the pages the UI opens first', () => {
    for (const p of ['/api/v1/auth/session', '/api/v1/cluster', '/api/v1/domains', '/api/v1/storage/pools', '/api/v1/networks', '/api/v1/alerts', '/api/v1/ha/status']) {
      expect(call('GET', p).status, p).toBe(200)
    }
    expect(call('GET', `/api/v1/domains/${WEB1}`).json.name).toBe('web-01')
    expect(call('GET', `/api/v1/domains/${WEB1}/xml`).json).toContain('<name>web-01</name>')
  })

  it('scopes /hosts/<id>/ to that member', () => {
    const own = call('GET', '/api/v1/domains').json.domains.map((d: { name: string }) => d.name)
    const other = call('GET', '/api/v1/hosts/b2c3d4e5f6071829/domains').json.domains.map((d: { name: string }) => d.name)
    expect(own).toContain('web-01')
    expect(other).toContain('k8s-cp-01')
    expect(other).not.toContain('web-01')
    expect(call('GET', '/api/v1/hosts/0000000000000000/domains').status).toBe(404)
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
})
