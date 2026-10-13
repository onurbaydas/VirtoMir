// Tiny history-API router. virtomird serves index.html for unknown paths, so deep links
// and reloads work. Routes: "/" overview, "/machines/<uuid>" machine page.

export type Route = { name: 'overview' } | { name: 'machine'; uuid: string; tab: string } | { name: 'not-found' }

function parse(path: string, hash: string): Route {
  if (path === '/' || path === '') return { name: 'overview' }
  const m = /^\/machines\/([0-9a-fA-F-]{36})\/?$/.exec(path)
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
