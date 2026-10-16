<script lang="ts">
  import { t } from '../i18n.svelte'
  // SPICE console (ADR-0046) with spice-html5 over virtomird's WebSocket bridge. With the guest
  // agent (spice-vdagent) the guest's resolution follows the window and the clipboard is shared.
  // spice-html5 works with fixed element ids and a global connection, so only one SPICE console
  // is shown at a time.
  import Maximize from '@lucide/svelte/icons/maximize'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Keyboard from '@lucide/svelte/icons/keyboard'
  import { onDestroy, onMount } from 'svelte'
  import { wsURL } from '../api'
  import type { SpiceMainConn } from '@spice-project/spice-html5'

  let { uuid, name, password, standalone = false }: { uuid: string; name: string; password?: string; standalone?: boolean } = $props()

  type ConnState = 'connecting' | 'connected' | 'error'
  let conn = $state<ConnState>('connecting')
  let message = $state('')
  let agent = $state(false)
  let area = $state<HTMLDivElement>()
  let sc: SpiceMainConn | null = null
  let lib: typeof import('@spice-project/spice-html5') | null = null
  let retry = 0
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let destroyed = false

  // spice-html5 reaches for these globals (window.spice_connection for resizing, sc for keys).
  const g = window as unknown as { spice_connection?: SpiceMainConn | null; sc?: SpiceMainConn | null }

  async function connect() {
    clearTimeout(retryTimer)
    stop()
    conn = 'connecting'
    message = ''
    agent = false
    try {
      lib ??= await import('@spice-project/spice-html5')
      const screen = document.getElementById('spice-screen')
      if (screen) screen.replaceChildren()
      sc = new lib.SpiceMainConn({
        uri: wsURL(`/api/v1/domains/${encodeURIComponent(uuid)}/console/spice`),
        password: password ?? '',
        screen_id: 'spice-screen',
        onsuccess: () => {
          conn = 'connected'
          retry = 0
        },
        onerror: (e: Error) => {
          if (destroyed) return
          conn = 'error'
          message = e?.message || String(e)
          stop()
          // Guest reboots and short drops: try again, slower each time.
          retry++
          retryTimer = setTimeout(connect, Math.min(30_000, 1000 * 2 ** Math.min(retry, 5)))
        },
        onagent: (c) => {
          agent = true
          g.spice_connection = c
          lib?.resize_helper(c)
        },
      })
      g.sc = sc
    } catch (e) {
      conn = 'error'
      message = e instanceof Error ? e.message : String(e)
    }
  }
  function stop() {
    if (sc) {
      try {
        sc.stop()
      } catch {
        /* already closed */
      }
    }
    sc = null
    g.sc = g.spice_connection = null
  }
  const onResize = (e: Event) => lib?.handle_resize(e)

  onMount(() => {
    void connect()
    window.addEventListener('resize', onResize)
  })
  onDestroy(() => {
    destroyed = true
    clearTimeout(retryTimer)
    window.removeEventListener('resize', onResize)
    stop()
  })

  function cad() {
    g.sc = sc
    lib?.sendCtrlAltDel()
  }
  async function fullscreen() {
    await area?.requestFullscreen?.()
    if (sc && agent) setTimeout(() => sc && lib?.resize_helper(sc), 300)
  }
</script>

<div class="spice" class:standalone>
  <div class="bar">
    <span class="state" class:ok={conn === 'connected'} class:bad={conn === 'error'}>
      {conn === 'connected' ? `SPICE · ${name}` : conn === 'connecting' ? 'Connecting…' : t('Not connected{v} — retrying', { v: message ? ': ' + message : '' })}
    </span>
    {#if conn === 'connected'}
      <span class="hint">{agent ? 'Guest agent: resolution follows the window, clipboard shared' : t('No guest agent (install spice-vdagent for resizing and the clipboard)')}</span>
    {/if}
    <span class="spacer"></span>
    <button class="ui-btn sm" disabled={conn !== 'connected'} onclick={cad} title={t('Send Ctrl+Alt+Del')}><Keyboard size={13} /> {t('Ctrl+Alt+Del')}</button>
    <button class="ui-btn sm" onclick={fullscreen} title={t('Full screen')}><Maximize size={13} /></button>
    <button class="ui-btn sm ghost" onclick={connect} title={t('Reconnect')}><RefreshCw size={13} /></button>
  </div>
  <div id="spice-area" class="area" bind:this={area}>
    <div id="spice-screen" class="screen"></div>
  </div>
</div>

<style>
  .spice { display: flex; flex-direction: column; gap: 8px; }
  .bar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; font-size: 12.5px; }
  .state { font-weight: 600; }
  .state.ok { color: var(--ok); }
  .state.bad { color: var(--danger); }
  .hint { color: var(--text-3); }
  .spacer { flex: 1; }
  .area { background: #000; min-height: 420px; overflow: auto; display: flex; justify-content: center; }
  .standalone .area { min-height: calc(100vh - 48px); }
  .screen { width: 100%; }
  .screen :global(canvas) { display: block; margin: 0 auto; outline: none; }
</style>
