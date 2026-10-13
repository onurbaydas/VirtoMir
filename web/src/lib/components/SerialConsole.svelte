<script lang="ts">
  import { wsURL } from '../api'
  // Text console (serial port / virtio console) with xterm.js over virtomird's WebSocket bridge.
  // Reconnects by itself; takes over a console someone else left open (virsh console).
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Eraser from '@lucide/svelte/icons/eraser'
  import { theme } from '../theme.svelte'
  import type { Terminal as TerminalType } from '@xterm/xterm'
  import type { FitAddon as FitType } from '@xterm/addon-fit'

  let { uuid, devices = [], standalone = false }: { uuid: string; devices?: string[]; standalone?: boolean } = $props()

  let el = $state<HTMLDivElement>()
  let device = $state('')
  let conn = $state<'connecting' | 'connected' | 'disconnected'>('connecting')
  let term: TerminalType | null = null
  let fit: FitType | null = null
  let ws: WebSocket | null = null
  let retry = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let destroyed = false

  function colors() {
    const css = (n: string) => getComputedStyle(document.documentElement).getPropertyValue(n).trim()
    return {
      background: css('--bg-elev'),
      foreground: css('--text'),
      cursor: css('--accent'),
      selectionBackground: css('--accent-soft'),
    }
  }

  function open() {
    if (destroyed || !term) return
    clearTimeout(timer)
    conn = 'connecting'
    const q = device ? `?device=${encodeURIComponent(device)}` : ''
    const sock = new WebSocket(wsURL(`/api/v1/domains/${encodeURIComponent(uuid)}/console/serial${q}`))
    sock.binaryType = 'arraybuffer'
    ws = sock
    sock.onopen = () => {
      conn = 'connected'
      retry = 0
      term?.focus()
      // Nudge a fresh prompt out of the guest.
      sock.send(new TextEncoder().encode('\r'))
    }
    sock.onmessage = (e) => term?.write(new Uint8Array(e.data as ArrayBuffer))
    sock.onclose = (e) => {
      if (ws !== sock || destroyed) return
      ws = null
      conn = 'disconnected'
      if (e.reason) term?.write(`\r\n\x1b[2m[${e.reason}]\x1b[0m\r\n`)
      const wait = Math.min(15, 2 ** retry) * 1000
      retry++
      timer = setTimeout(open, wait)
    }
  }

  $effect(() => {
    if (!el) return
    let ro: ResizeObserver | undefined
    let disposed = false
    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')])
      await import('@xterm/xterm/css/xterm.css')
      if (disposed || !el) return
      term = new Terminal({
        fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--font-mono'),
        fontSize: 13,
        cursorBlink: true,
        scrollback: 5000,
        theme: colors(),
        convertEol: false,
      })
      fit = new FitAddon()
      term.loadAddon(fit)
      term.open(el)
      fit.fit()
      term.onData((d) => ws?.readyState === WebSocket.OPEN && ws.send(new TextEncoder().encode(d)))
      ro = new ResizeObserver(() => fit?.fit())
      ro.observe(el)
      open()
    })()
    return () => {
      disposed = true
      destroyed = true
      clearTimeout(timer)
      ro?.disconnect()
      ws?.close()
      term?.dispose()
      term = null
    }
  })

  // Follow theme changes.
  $effect(() => {
    void theme.resolved
    if (term) term.options.theme = colors()
  })

  function switchDevice(d: string) {
    device = d
    retry = 0
    const old = ws
    ws = null
    old?.close()
    term?.clear()
    open()
  }
</script>

<div class="serial" class:standalone>
  <div class="bar">
    <span class="status {conn}"><span class="dot"></span>{conn === 'connected' ? 'Connected' : conn === 'connecting' ? 'Connecting…' : 'Reconnecting…'}</span>
    {#if devices.length > 1}
      <select class="ui-input dev" value={device || devices[0]} onchange={(e) => switchDevice(e.currentTarget.value)} aria-label="Console device">
        {#each devices as d (d)}<option value={d}>{d}</option>{/each}
      </select>
    {/if}
    <span class="spacer"></span>
    <span class="hint">Text console of the guest (serial / virtio). Shows boot messages and a login if the guest offers one there.</span>
    <button class="tb" onclick={() => term?.clear()} title="Clear the screen"><Eraser size={14} /></button>
    <button class="tb" onclick={() => switchDevice(device)} title="Reconnect"><RefreshCw size={14} /></button>
  </div>
  <div class="term" bind:this={el}></div>
</div>

<style>
  .serial {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .serial.standalone {
    height: 100vh;
    border: 0;
    border-radius: 0;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .status {
    --c: var(--info);
    display: inline-flex;
    align-items: center;
    gap: 7px;
    color: var(--c);
    font-size: 12.5px;
    font-weight: 500;
  }
  .status.connected {
    --c: var(--ok);
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--c);
  }
  .dev {
    width: auto;
    height: 30px;
  }
  .spacer {
    flex: 1;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .tb {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
    color: var(--text-2);
    cursor: pointer;
  }
  .tb:hover {
    color: var(--text);
  }
  .term {
    flex: 1;
    min-height: 460px;
    padding: 8px;
    background: var(--bg-elev);
  }
  .standalone .term {
    min-height: 0;
  }
  @media (max-width: 760px) {
    .hint {
      display: none;
    }
  }
</style>
