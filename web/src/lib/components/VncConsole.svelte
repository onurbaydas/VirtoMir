<script lang="ts">
  import { wsURL } from '../api'
  // Graphical console: noVNC over virtomird's WebSocket bridge to the machine's local VNC
  // server. Designed around the usual console pain points:
  //  - keyboard layouts: QEMU's extended key events send physical key positions, so the
  //    guest's own layout applies (no keymap mismatch between browser and guest)
  //  - clipboard: "Type" sends text as keystrokes, which works without any guest agent
  //  - resize: fit to window, 1:1, or ask the guest to match the window
  //  - reconnect: automatic, with backoff, also across guest reboots
  //  - special keys the browser would swallow: Ctrl+Alt+Del, Ctrl+Alt+Fn, Alt+Tab, Win, PrtSc
  import Keyboard from '@lucide/svelte/icons/keyboard'
  import Clipboard from '@lucide/svelte/icons/clipboard'
  import Maximize from '@lucide/svelte/icons/maximize'
  import Camera from '@lucide/svelte/icons/camera'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import Eye from '@lucide/svelte/icons/eye'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Video from '@lucide/svelte/icons/video'
  import type RFBType from '@novnc/novnc'
  import { onDestroy } from 'svelte'
  import { toasts } from '../toast.svelte'

  interface Props {
    uuid: string
    name: string
    password?: string
    /** Full-window layout (pop-out page). */
    standalone?: boolean
  }
  let { uuid, name, password, standalone = false }: Props = $props()

  type ConnState = 'connecting' | 'connected' | 'disconnected' | 'error'
  let conn = $state<ConnState>('connecting')
  let message = $state('')
  let screen = $state<HTMLDivElement>()
  let wrap = $state<HTMLDivElement>()
  let rfb: RFBType | null = null
  let retry = 0
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let destroyed = false

  type View = 'fit' | 'native' | 'resize'
  let view = $state<View>(load('virtomir.console.view', 'fit') as View)
  let viewOnly = $state(false)
  let keysOpen = $state(false)
  let clipOpen = $state(false)
  let clipText = $state('')
  let guestClip = $state('')
  let recording = $state(false)
  let mediaRecorder: MediaRecorder | null = null
  let recordedChunks: Blob[] = []

  function toggleRecord() {
    if (recording) {
      mediaRecorder?.stop()
      recording = false
    } else {
      const canvas = screen?.querySelector('canvas')
      if (!canvas) {
        toasts.push({ kind: 'warn', title: 'Cannot record', detail: 'No video stream active' })
        return
      }
      try {
        const stream = (canvas as any).captureStream ? (canvas as any).captureStream(30) : null
        if (!stream) {
          toasts.push({ kind: 'error', title: 'Recording not supported', detail: 'Canvas captureStream unavailable' })
          return
        }
        recordedChunks = []
        mediaRecorder = new MediaRecorder(stream, { mimeType: 'video/webm' })
        mediaRecorder.ondataavailable = (e) => {
          if (e.data.size > 0) recordedChunks.push(e.data)
        }
        mediaRecorder.onstop = () => {
          const blob = new Blob(recordedChunks, { type: 'video/webm' })
          const url = URL.createObjectURL(blob)
          const a = document.createElement('a')
          a.href = url
          a.download = `${name}-console-${Date.now()}.webm`
          a.click()
          URL.revokeObjectURL(url)
          toasts.push({ kind: 'info', title: 'Video saved', detail: 'Console session recording exported as WebM' })
        }
        mediaRecorder.start()
        recording = true
        toasts.push({ kind: 'info', title: 'Recording started', detail: 'Capturing console canvas at 30 FPS' })
      } catch (err) {
        toasts.push({ kind: 'error', title: 'Could not record', detail: String(err) })
      }
    }
  }

  function load(key: string, def: string) {
    try {
      return localStorage.getItem(key) ?? def
    } catch {
      return def
    }
  }
  function save(key: string, v: string) {
    try {
      localStorage.setItem(key, v)
    } catch {
      /* storage blocked */
    }
  }

  function applyView() {
    if (!rfb) return
    rfb.scaleViewport = view === 'fit'
    rfb.resizeSession = view === 'resize'
    rfb.clipViewport = view === 'native'
    save('virtomir.console.view', view)
  }

  async function connect() {
    if (!screen || destroyed) return
    clearTimeout(retryTimer)
    conn = 'connecting'
    message = ''
    const { default: RFB } = await import('@novnc/novnc')
    if (destroyed) return
    const url = wsURL(`/api/v1/domains/${encodeURIComponent(uuid)}/console/vnc`)
    const r = new RFB(screen, url, { shared: true, wsProtocols: ['binary'], credentials: password ? { password } : undefined })
    rfb = r
    r.background = 'var(--bg)'
    r.focusOnClick = true
    r.qualityLevel = 6
    r.compressionLevel = 2
    r.viewOnly = viewOnly
    applyView()

    r.addEventListener('connect', () => {
      conn = 'connected'
      retry = 0
      r.focus()
    })
    r.addEventListener('disconnect', (e: Event) => {
      rfb = null
      if (destroyed) return
      const clean = (e as CustomEvent<{ clean: boolean }>).detail?.clean
      conn = 'disconnected'
      // Reconnect with backoff: 1, 2, 4, … up to 15 s. A rebooting guest comes back on its own.
      const wait = Math.min(15, 2 ** retry) * 1000
      retry++
      message = clean ? 'The console closed.' : 'Connection lost.'
      retryTimer = setTimeout(connect, wait)
    })
    r.addEventListener('credentialsrequired', () => {
      if (password) r.sendCredentials({ password })
      else {
        conn = 'error'
        message = 'The VNC server asks for a password VirtoMir does not know.'
      }
    })
    r.addEventListener('securityfailure', (e: Event) => {
      conn = 'error'
      message = `Security negotiation failed: ${(e as CustomEvent<{ reason?: string }>).detail?.reason ?? 'unknown reason'}`
    })
    r.addEventListener('clipboard', (e: Event) => {
      guestClip = (e as CustomEvent<{ text: string }>).detail.text
    })
  }

  $effect(() => {
    if (screen) void connect()
    return () => {
      destroyed = true
      clearTimeout(retryTimer)
      rfb?.disconnect()
      rfb = null
    }
  })
  onDestroy(() => (destroyed = true))

  $effect(() => {
    void view
    applyView()
  })
  $effect(() => {
    if (rfb) rfb.viewOnly = viewOnly
  })

  // ---- Keys -------------------------------------------------------------------------------
  const K = {
    ctrl: [0xffe3, 'ControlLeft'],
    alt: [0xffe9, 'AltLeft'],
    shift: [0xffe1, 'ShiftLeft'],
    win: [0xffeb, 'MetaLeft'],
    tab: [0xff09, 'Tab'],
    del: [0xffff, 'Delete'],
    bksp: [0xff08, 'Backspace'],
    esc: [0xff1b, 'Escape'],
    print: [0xff61, 'PrintScreen'],
  } as const
  type Key = readonly [number, string]
  const F = (n: number): Key => [0xffbe + n - 1, `F${n}`]

  function combo(...keys: Key[]) {
    if (!rfb) return
    for (const [sym, code] of keys) rfb.sendKey(sym, code, true)
    for (const [sym, code] of [...keys].reverse()) rfb.sendKey(sym, code, false)
    rfb.focus()
    keysOpen = false
  }

  const SHORTCUTS: { label: string; keys: Key[] }[] = [
    { label: 'Ctrl + Alt + Backspace', keys: [K.ctrl, K.alt, K.bksp] },
    { label: 'Alt + Tab', keys: [K.alt, K.tab] },
    { label: 'Windows / Super', keys: [K.win] },
    { label: 'Print Screen', keys: [K.print] },
    { label: 'Escape', keys: [K.esc] },
    { label: 'Ctrl + Esc', keys: [K.ctrl, K.esc] },
  ]

  // Typing: each character becomes a key press. Works without guest tools (installers,
  // BIOS, login prompts); uses the US key positions QEMU expects for plain keysyms.
  let typing = $state(false)
  async function typeText(text: string) {
    if (!rfb || !text) return
    typing = true
    const r = rfb
    for (const ch of text.replace(/\r\n/g, '\n')) {
      if (destroyed || rfb !== r) break
      const cp = ch.codePointAt(0)!
      const sym = ch === '\n' ? 0xff0d : ch === '\t' ? 0xff09 : cp < 0x100 ? cp : 0x01000000 + cp
      r.sendKey(sym, null, true)
      r.sendKey(sym, null, false)
      await new Promise((res) => setTimeout(res, 12)) // guests drop keys that come too fast
    }
    typing = false
    clipOpen = false
    r.focus()
  }

  function screenshot() {
    rfb?.toBlob((blob) => {
      if (!blob) return
      const a = Object.assign(document.createElement('a'), {
        href: URL.createObjectURL(blob),
        download: `${name}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.png`,
      })
      a.click()
      URL.revokeObjectURL(a.href)
    })
  }

  function fullscreen() {
    if (document.fullscreenElement) void document.exitFullscreen()
    else void wrap?.requestFullscreen().then(() => rfb?.focus())
  }

  function popOut() {
    window.open(`/console/${uuid}`, `virtomir-console-${uuid}`, 'popup,width=1100,height=760')
  }

  // Close the little menus when clicking elsewhere.
  $effect(() => {
    if (!keysOpen && !clipOpen) return
    const onDown = (e: PointerEvent) => {
      if (!(e.target as HTMLElement).closest('.pop, .pop-btn')) {
        keysOpen = false
        clipOpen = false
      }
    }
    document.addEventListener('pointerdown', onDown, true)
    return () => document.removeEventListener('pointerdown', onDown, true)
  })
</script>

<div class="console" class:standalone bind:this={wrap}>
  <div class="bar">
    <span class="status {conn}">
      <span class="dot"></span>
      {conn === 'connected' ? 'Connected' : conn === 'connecting' ? 'Connecting…' : conn === 'disconnected' ? 'Reconnecting…' : 'Not connected'}
    </span>

    <button class="tb" onclick={() => rfb?.sendCtrlAltDel()} disabled={conn !== 'connected' || viewOnly} title="Send Ctrl+Alt+Del">Ctrl+Alt+Del</button>

    <div class="rel">
      <button class="tb pop-btn" onclick={() => ((keysOpen = !keysOpen), (clipOpen = false))} disabled={conn !== 'connected' || viewOnly} aria-expanded={keysOpen}>
        <Keyboard size={14} /> Keys <ChevronDown size={12} />
      </button>
      {#if keysOpen}
        <div class="pop keys" role="menu">
          <div class="fkeys">
            {#each Array.from({ length: 12 }, (_, i) => i + 1) as n (n)}
              <button role="menuitem" onclick={() => combo(K.ctrl, K.alt, F(n))} title="Ctrl + Alt + F{n} (switch virtual terminal)">F{n}</button>
            {/each}
          </div>
          <span class="hint">Ctrl + Alt + F-key</span>
          {#each SHORTCUTS as s (s.label)}
            <button role="menuitem" class="item" onclick={() => combo(...s.keys)}>{s.label}</button>
          {/each}
        </div>
      {/if}
    </div>

    <div class="rel">
      <button class="tb pop-btn" onclick={() => ((clipOpen = !clipOpen), (keysOpen = false))} disabled={conn !== 'connected' || viewOnly} aria-expanded={clipOpen}>
        <Clipboard size={14} /> Type
      </button>
      {#if clipOpen}
        <div class="pop clip">
          <label class="ui-field">
            <span class="label">Text to type into the machine</span>
            <!-- svelte-ignore a11y_autofocus -->
            <textarea class="ui-input mono" rows="4" bind:value={clipText} placeholder="Paste a password, a command, a long URL…" autofocus></textarea>
          </label>
          <div class="clip-actions">
            <span class="hint">Typed key by key — works in installers and login screens, no guest tools needed.</span>
            <button class="ui-btn sm primary" disabled={!clipText || typing} onclick={() => typeText(clipText)}>{typing ? 'Typing…' : 'Type it'}</button>
          </div>
          {#if guestClip}
            <div class="guest-clip">
              <span class="label">The machine copied:</span>
              <code class="mono">{guestClip.length > 200 ? guestClip.slice(0, 200) + '…' : guestClip}</code>
              <button class="ui-btn sm" onclick={() => navigator.clipboard.writeText(guestClip).then(() => toasts.push({ kind: 'info', title: 'Copied from the machine' }))}>Copy to my clipboard</button>
            </div>
          {/if}
        </div>
      {/if}
    </div>

    <span class="spacer"></span>

    <div class="seg" role="radiogroup" aria-label="Display size">
      <button role="radio" aria-checked={view === 'fit'} class:on={view === 'fit'} onclick={() => (view = 'fit')} title="Scale the screen to fit">Fit</button>
      <button role="radio" aria-checked={view === 'native'} class:on={view === 'native'} onclick={() => (view = 'native')} title="Real pixels, scroll if larger">1:1</button>
      <button role="radio" aria-checked={view === 'resize'} class:on={view === 'resize'} onclick={() => (view = 'resize')} title="Ask the machine to use the window size (needs a resizable display, e.g. virtio-gpu)">Match window</button>
    </div>
    <button class="tb icon" class:on={viewOnly} onclick={() => (viewOnly = !viewOnly)} title={viewOnly ? 'View only (input is off)' : 'Turn input off (view only)'} aria-pressed={viewOnly}><Eye size={15} /></button>
    <button class="tb icon" onclick={screenshot} disabled={conn !== 'connected'} title="Save a screenshot"><Camera size={15} /></button>
    <button class="tb icon" class:recording onclick={toggleRecord} disabled={conn !== 'connected'} title={recording ? 'Stop and download WebM recording' : 'Record console to WebM'}>
      <Video size={15} />
    </button>
    {#if !standalone}<button class="tb icon" onclick={popOut} title="Open in its own window"><ExternalLink size={15} /></button>{/if}
    <button class="tb icon" onclick={fullscreen} title="Full screen (Esc leaves)"><Maximize size={15} /></button>
  </div>

  <div class="screen-wrap">
    <div class="screen" class:native={view === 'native'} bind:this={screen}></div>
    {#if conn !== 'connected'}
      <div class="overlay">
        {#if conn === 'error'}
          <strong>Console unavailable</strong>
          <span>{message}</span>
          <button class="ui-btn sm" onclick={() => ((retry = 0), connect())}><RefreshCw size={14} /> Try again</button>
        {:else}
          <span class="spin"></span>
          <span>{conn === 'connecting' ? `Connecting to ${name}…` : `${message} Reconnecting…`}</span>
          {#if conn === 'disconnected'}<button class="ui-btn sm ghost" onclick={() => ((retry = 0), connect())}>Reconnect now</button>{/if}
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .console {
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: var(--frame) solid var(--border-strong);
    border-radius: var(--radius-sm);
    overflow: hidden;
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .console.standalone,
  .console:fullscreen {
    height: 100vh;
    border: 0;
    border-radius: var(--radius-sm);
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .status {
    --c: var(--text-3);
    display: inline-flex;
    align-items: center;
    gap: 7px;
    margin-right: 6px;
    color: var(--c);
    font-size: 12.5px;
    font-weight: 500;
  }
  .status.connected {
    --c: var(--ok);
  }
  .status.disconnected,
  .status.connecting {
    --c: var(--info);
  }
  .status.error {
    --c: var(--danger);
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: var(--radius-sm);
    background: var(--c);
  }
  .tb {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
    cursor: pointer;
  }
  .tb.icon {
    width: 30px;
    padding: 0;
    justify-content: center;
  }
  .tb:hover:not(:disabled) {
    color: var(--text);
    border-color: var(--border-strong);
  }
  .tb.on {
    color: var(--accent);
    border-color: var(--accent);
  }
  .tb.icon.recording {
    background: var(--danger);
    color: #fff;
    border-color: var(--danger);
    animation: blink 1s infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0.5;
    }
  }
  .tb:disabled {
    opacity: 0.45;
    cursor: default;
  }
  .rel {
    position: relative;
  }
  .pop {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    z-index: 20;
    padding: 8px;
    background: var(--bg-elev);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-pop);
  }
  .keys {
    width: 250px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .fkeys {
    display: grid;
    grid-template-columns: repeat(6, 1fr);
    gap: 4px;
  }
  .fkeys button {
    height: 28px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font-size: 12px;
    cursor: pointer;
  }
  .fkeys button:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .keys .hint {
    margin: 2px 0 6px;
  }
  .item {
    height: 32px;
    padding: 0 8px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    font-size: 13px;
    text-align: left;
    cursor: pointer;
  }
  .item:hover {
    background: var(--surface-hover);
  }
  .clip {
    width: min(420px, 80vw);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .clip-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .hint {
    flex: 1;
    color: var(--text-3);
    font-size: 11.5px;
  }
  .guest-clip {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-top: 8px;
    border-top: 1px solid var(--border);
    align-items: flex-start;
  }
  .guest-clip code {
    max-width: 100%;
    overflow-wrap: anywhere;
    font-size: 12px;
  }
  .label {
    color: var(--text-2);
    font-size: 12px;
  }
  .spacer {
    flex: 1;
  }
  .seg {
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    box-shadow: var(--bevel-in);
  }
  .seg button {
    height: 26px;
    padding: 0 9px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-2);
    font-size: 12px;
    cursor: pointer;
  }
  .seg button.on {
    background: var(--metal);
    outline: 1px solid var(--border-strong);
    color: var(--text);
    box-shadow: var(--bevel);
  }
  .screen-wrap {
    position: relative;
    flex: 1;
    min-height: 480px;
    background: #000;
  }
  .standalone .screen-wrap,
  .console:fullscreen .screen-wrap {
    min-height: 0;
  }
  .screen {
    position: absolute;
    inset: 0;
    display: flex;
  }
  .screen.native {
    overflow: auto;
  }
  .overlay {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 16px;
    background: color-mix(in srgb, var(--bg) 88%, transparent);
    color: var(--text-2);
    font-size: 13.5px;
    text-align: center;
  }
  .overlay strong {
    color: var(--text);
    font-size: 15px;
  }
  .spin {
    width: 22px;
    height: 22px;
    border: 2.5px solid var(--accent);
    border-right-color: transparent;
    border-radius: var(--radius-sm);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
