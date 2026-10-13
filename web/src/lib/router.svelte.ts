// Tiny history-API router. virtomird serves index.html for unknown paths, so deep links
// and reloads work. Routes: "/" overview, "/machines/<uuid>[#tab]" machine page,
// "/machines/new" wizard, "/storage[#pool]", "/networks".

export type Route =
  | { name: 'overview' }
  | { name: 'machine'; uuid: string; tab: string }
  | { name: 'new' }
  | { name: 'storage'; pool: string }
  | { name: 'networks' }
  | { name: 'activity' }
  | { name: 'account' }
  | { name: 'console'; uuid: string }
  | { name: 'not-found' }

function parse(path: string, hash: string): Route {
  path = path.replace(/\/+$/, '') || '/'
  if (path === '/') return { name: 'overview' }
  if (path === '/machines/new') return { name: 'new' }
  if (path === '/storage') return { name: 'storage', pool: decodeURIComponent(hash.replace(/^#/, '')) }
  if (path === '/networks') return { name: 'networks' }
  if (path === '/activity') return { name: 'activity' }
  if (path === '/account') return { name: 'account' }
  const c = /^\/console\/([0-9a-fA-F-]{36})$/.exec(path)
  if (c) return { name: 'console', uuid: c[1].toLowerCase() }
  const m = /^\/machines\/([0-9a-fA-F-]{36})$/.exec(path)
  if (m) return { name: 'machine', uuid: m[1].toLowerCase(), tab: hash.replace(/^#/, '') }
  return { name: 'not-found' }
}

class Router {
  route = $state<Route>(parse(location.pathname, location.hash))

  constructor() {
    window.addEventListener('popstate', () => this.#sync())
  }

  #sync() {
    this.route = parse(location.pathname, location.hash)
  }

  navigate(href: string, opts: { replace?: boolean } = {}) {
    const url = new URL(href, location.href)
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

export const machineHref = (uuid: string, tab?: string) => `/machines/${uuid}${tab ? '#' + tab : ''}`
