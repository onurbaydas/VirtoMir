// Demo mode (VITE_DEMO=1): the UI talks to an in-browser pretend virtomird instead of a host.
// fetch, EventSource, WebSocket and XMLHttpRequest calls to /api/ are answered by routes.ts;
// anything else goes to the network as usual. Imported once, before the app mounts.

import { handle, type Reply } from './routes'
import * as w from './world'
import { serialShell } from './shell'

const isAPI = (u: URL) => u.origin === location.origin && u.pathname.startsWith('/api/')

/** Network-ish delay so spinners and transitions look like the real thing. */
const latency = () => new Promise((r) => setTimeout(r, 60 + Math.random() * 120))

function parseBody(body: BodyInit | null | undefined): unknown {
  if (typeof body !== 'string') return undefined
  try {
    return JSON.parse(body)
  } catch {
    return body
  }
}

function toResponse(r: Reply): Response {
  return new Response(r.status === 204 ? null : (r.body ?? ''), { status: r.status, headers: r.type ? { 'Content-Type': r.type } : {} })
}

// ---- fetch ----------------------------------------------------------------------------------

const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const req = input instanceof Request ? input : null
  const url = new URL(req ? req.url : String(input), location.href)
  if (!isAPI(url)) return realFetch(input, init)
  const method = (init?.method ?? req?.method ?? 'GET').toUpperCase()
  await latency()
  if (init?.signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  return toResponse(handle(method, url, parseBody(init?.body)))
}

// ---- EventSource (the live stream) ----------------------------------------------------------

const RealEventSource = window.EventSource

class DemoEventSource extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2
  readonly url: string
  readonly withCredentials = false
  readyState = 0
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  #timer: ReturnType<typeof setInterval> | undefined

  constructor(url: string | URL) {
    super()
    this.url = new URL(String(url), location.href).href
    const host = /\/api\/v1\/hosts\/([0-9a-f]{16})\//.exec(this.url)?.[1] ?? null
    const h = w.hostByID(host) ?? w.SELF
    setTimeout(() => {
      if (this.readyState === 2) return
      this.readyState = 1
      const open = new Event('open')
      this.onopen?.(open)
      this.dispatchEvent(open)
      // A short backlog first, so charts are not empty, then one snapshot a second.
      for (let i = 20; i > 0; i--) this.#emit(h, Date.now() - i * 1000)
      this.#timer = setInterval(() => this.#emit(h, Date.now()), 1000)
    }, 150)
  }

  #emit(h: w.DemoHost, at: number) {
    const e = new MessageEvent('snapshot', { data: JSON.stringify(w.snapshotOf(h, at)) })
    this.dispatchEvent(e)
  }

  close() {
    this.readyState = 2
    clearInterval(this.#timer)
  }
}

const DemoEventSourceCtor = function (url: string | URL, init?: EventSourceInit) {
  const u = new URL(String(url), location.href)
  return isAPI(u) ? new DemoEventSource(u) : new RealEventSource(url, init)
}
Object.assign(DemoEventSourceCtor, { CONNECTING: 0, OPEN: 1, CLOSED: 2 })
window.EventSource = DemoEventSourceCtor as unknown as typeof EventSource

// ---- WebSocket (consoles) -------------------------------------------------------------------

const RealWebSocket = window.WebSocket

class DemoSocket extends EventTarget {
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSING = 2
  readonly CLOSED = 3
  readonly url: string
  readyState = 0
  binaryType: BinaryType = 'blob'
  protocol = ''
  extensions = ''
  bufferedAmount = 0
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onclose: ((e: CloseEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  #write: ((s: string) => void) | null = null
  #input: ((s: string) => void) | null = null

  constructor(url: string) {
    super()
    this.url = url
    const u = new URL(url)
    const m = /\/domains\/([0-9a-f-]{36})\/console\/(\w+)/.exec(u.pathname)
    const machine = m ? w.byUUID(m[1]) : undefined
    setTimeout(() => {
      if (!machine || m![2] !== 'serial') {
        // Screen consoles speak VNC/SPICE to a real QEMU; there is none in the demo.
        return this.#close(1011, 'The screen console needs a real host; try the Serial console in the demo.')
      }
      const enc = new TextEncoder()
      this.#write = (s) => this.#fire('message', new MessageEvent('message', { data: enc.encode(s).buffer }))
      this.#input = serialShell(machine, (s) => this.#write?.(s))
      this.readyState = 1
      this.#fire('open', new Event('open')) // the console sends "\r" here, which logs in
    }, 200)
  }

  #fire(type: 'open' | 'message' | 'close' | 'error', e: Event) {
    const h = (this as any)['on' + type]
    if (h) h.call(this, e)
    this.dispatchEvent(e)
  }

  #close(code: number, reason: string) {
    this.readyState = 3
    this.#fire('close', new CloseEvent('close', { code, reason, wasClean: false }))
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
    if (this.readyState !== 1) return
    const s = typeof data === 'string' ? data : new TextDecoder().decode(data as ArrayBuffer)
    this.#input?.(s)
  }

  close() {
    if (this.readyState >= 2) return
    this.#close(1000, '')
  }
}

const DemoWebSocket = function (url: string | URL, protocols?: string | string[]) {
  const u = new URL(String(url), location.href)
  const sameHost = u.host === location.host && u.pathname.startsWith('/api/')
  return sameHost ? new DemoSocket(u.href) : new RealWebSocket(url, protocols)
}
// Code compares readyState with WebSocket.OPEN and friends.
Object.assign(DemoWebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 })
window.WebSocket = DemoWebSocket as unknown as typeof WebSocket

// ---- XMLHttpRequest (uploads) ---------------------------------------------------------------

const RealXHR = window.XMLHttpRequest

class DemoXHR extends EventTarget {
  status = 0
  statusText = ''
  responseText = ''
  readyState = 0
  upload = new EventTarget() as XMLHttpRequestUpload
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  #method = 'GET'
  #url = ''
  #aborted = false

  open(method: string, url: string) {
    this.#method = method
    this.#url = url
  }
  setRequestHeader() {}
  abort() {
    this.#aborted = true
    this.onabort?.()
  }
  send(body?: unknown) {
    const size = body instanceof Blob ? body.size : 1
    let done = 0
    const step = () => {
      if (this.#aborted) return
      done = Math.min(size, done + Math.max(size / 12, 1))
      const p = new ProgressEvent('progress', { lengthComputable: true, loaded: done, total: size })
      ;(this.upload as any).onprogress?.(p)
      if (done < size) return void setTimeout(step, 120)
      const r = handle(this.#method, new URL(this.#url, location.href), undefined)
      Object.assign(this, { status: r.status, statusText: r.status < 300 ? 'OK' : 'Error', responseText: r.body ?? '', readyState: 4 })
      this.onload?.()
    }
    setTimeout(step, 120)
  }
}

window.XMLHttpRequest = function () {
  return new DemoXHR()
} as unknown as typeof XMLHttpRequest
// Non-API XHRs (none in the app today) would need the real one.
void RealXHR
