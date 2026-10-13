// Tiny history-API router. virtomird serves index.html for unknown paths, so deep links
// and reloads work. Routes: "/" overview, "/machines/<uuid>[#tab]" machine page,
// "/machines/new" wizard, "/storage[#pool]", "/networks", "/cluster".
// In a cluster, "/hosts/<id>" in front of any page shows it for that member (ADR-0020); without
// the prefix the page is about the host the browser is connected to.

export type Route =
  | { name: 'overview' }
  | { name: 'machine'; uuid: string; tab: string }
  | { name: 'new' }
  | { name: 'storage'; pool: string }
  | { name: 'networks' }
  | { name: 'activity' }
  | { name: 'alerts'; tab: string }
  | { name: 'backups' }
  | { name: 'account' }
  | { name: 'console'; uuid: string }
  | { name: 'cluster' }
  | { name: 'not-found' }

function parse(path: string, hash: string): Route {
  path = path.replace(/\/+$/, '') || '/'
  if (path === '/') return { name: 'overview' }
  if (path === '/machines/new') return { name: 'new' }
  if (path === '/storage') return { name: 'storage', pool: decodeURIComponent(hash.replace(/^#/, '')) }
  if (path === '/networks') return { name: 'networks' }
  if (path === '/activity') return { name: 'activity' }
  if (path === '/alerts') return { name: 'alerts', tab: hash.replace(/^#/, '') }
  if (path === '/backups') return { name: 'backups' }
  if (path === '/account') return { name: 'account' }
  if (path === '/cluster') return { name: 'cluster' }
  const c = /^\/console\/([0-9a-fA-F-]{36})$/.exec(path)
  if (c) return { name: 'console', uuid: c[1].toLowerCase() }
  const m = /^\/machines\/([0-9a-fA-F-]{36})$/.exec(path)
  if (m) return { name: 'machine', uuid: m[1].toLowerCase(), tab: hash.replace(/^#/, '') }
  return { name: 'not-found' }
}

const HOST_RE = /^\/hosts\/([0-9a-f]{16})(?=\/|$)/

/** Splits "/hosts/<id>/rest" into the member ID and "/rest". */
function splitHost(path: string): { host: string | null; path: string } {
  const m = HOST_RE.exec(path)
  return m ? { host: m[1], path: path.slice(m[0].length) || '/' } : { host: null, path }
}

const withHost = (id: string | null, path: string) => (id ? `/hosts/${id}${path === '/' ? '' : path}` : path)

class Router {
  /** Cluster member the pages are about; null = the host serving this page. */
  host = $state<string | null>(splitHost(location.pathname).host)
  route = $state<Route>(parse(splitHost(location.pathname).path, location.hash))

  constructor() {
    window.addEventListener('popstate', () => this.#sync())
  }

  #sync() {
    const { host, path } = splitHost(location.pathname)
    this.host = host
    this.route = parse(path, location.hash)
  }

  /** Path with the current member's prefix (links inside the app stay on that member). */
  scoped(path: string): string {
    return HOST_RE.test(path) || path === '/cluster' ? path : withHost(this.host, path)
  }

  /** Shows the same kind of page for another member (machine pages fall back to its overview). */
  switchHost(id: string | null) {
    const keep = ['overview', 'storage', 'networks', 'backups', 'alerts', 'activity'].includes(this.route.name)
    const path = keep ? splitHost(location.pathname).path : '/'
    history.pushState(null, '', withHost(id, path))
    this.#sync()
  }

  navigate(href: string, opts: { replace?: boolean } = {}) {
    const url = new URL(href, location.href)
    url.pathname = this.scoped(url.pathname)
    if (url.pathname + url.hash === location.pathname + location.hash) return
    const samePage = url.pathname === location.pathname
    history[opts.replace ? 'replaceState' : 'pushState'](null, '', url.pathname + url.hash)
    this.#sync()
    if (!samePage) window.scrollTo({ top: 0 })
  }

  /** onclick for in-app <a href> links: plain left clicks navigate without a reload. */
  link = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const a = e.currentTarget as HTMLAnchorElement
    if (a.target || a.origin !== location.origin) return
    e.preventDefault()
    this.navigate(a.href)
  }
}

export const router = new Router()

export const machineHref = (uuid: string, tab?: string) => router.scoped(`/machines/${uuid}${tab ? '#' + tab : ''}`)
