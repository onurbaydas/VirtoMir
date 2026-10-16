<script lang="ts">
  // Packet capture on a host interface: tcpdump on the host, its lines streamed here, or the
  // raw capture as a .pcap file for Wireshark. Admins only (it shows other people's traffic).
  import Dialog from './Dialog.svelte'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Download from '@lucide/svelte/icons/download'
  import { errorText } from '../api'
  import { ent, type HostInterface } from '../enterprise'
  import { live } from '../live.svelte'

  let { open, onclose }: { open: boolean; onclose: () => void } = $props()

  let ifs = $state<HostInterface[]>([])
  let iface = $state('')
  let filter = $state('')
  let count = $state(500)
  let seconds = $state(30)
  let lines = $state<string[]>([])
  let status = $state('')
  let running = $state(false)
  let ctrl: AbortController | null = null

  $effect(() => {
    if (!open) return
    void ent.interfaces().then((l) => {
      ifs = l.filter((i) => i.kind !== 'loopback')
      if (!iface) iface = ifs.find((i) => i.kind === 'bridge')?.name ?? ifs[0]?.name ?? ''
    }).catch((e) => (status = errorText(e)))
  })

  let vmOf = $derived.by(() => {
    const m = new Map<string, string>()
    for (const d of live.snapshot?.domains ?? []) for (const n of d.nets ?? []) m.set(n.name, d.name)
    return m
  })

  async function start() {
    lines = []
    status = 'Capturing…'
    running = true
    ctrl = new AbortController()
    try {
      const res = await fetch(ent.captureURL(iface, filter, count, seconds), { signal: ctrl.signal, headers: { Accept: 'text/event-stream' } })
      if (!res.ok || !res.body) {
        let msg = res.statusText
        try { msg = (await res.json()).error ?? msg } catch { /* not JSON */ }
        throw new Error(msg)
      }
      const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
      let buf = ''
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += value
        let i
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const ev = buf.slice(0, i)
          buf = buf.slice(i + 2)
          const kind = /^event: (.*)$/m.exec(ev)?.[1]
          const data = /^data: (.*)$/m.exec(ev)?.[1] ?? ''
          if (kind === 'packet') lines = [...lines.slice(-1999), data]
          else if (kind === 'end') status = data === 'finished' ? `Finished: ${lines.length} packets` : data
        }
      }
    } catch (e) {
      status = (e as Error).name === 'AbortError' ? `Stopped: ${lines.length} packets` : errorText(e)
    } finally {
      running = false
      ctrl = null
    }
  }
  function stop() {
    ctrl?.abort()
  }
  function close() {
    stop()
    onclose()
  }
</script>

<Dialog {open} title="Packet capture" subtitle="tcpdump on this host" width={860} onclose={close}>
  <div class="form">
    <label class="ui-field"><span class="label">Interface</span>
      <select class="ui-input mono" bind:value={iface} disabled={running}>
        {#each ifs as i (i.name)}<option value={i.name}>{i.name} · {i.kind}{vmOf.get(i.name) ? ` · ${vmOf.get(i.name)}` : ''}</option>{/each}
      </select></label>
    <label class="ui-field grow"><span class="label">Filter (tcpdump syntax)</span>
      <input class="ui-input mono" placeholder="e.g. tcp port 443 and host 10.0.0.5" bind:value={filter} disabled={running} /></label>
    <label class="ui-field"><span class="label">Packets</span><input class="ui-input" type="number" min="1" max="20000" bind:value={count} disabled={running} /></label>
    <label class="ui-field"><span class="label">Seconds</span><input class="ui-input" type="number" min="1" max="120" bind:value={seconds} disabled={running} /></label>
  </div>
  <div class="bar">
    {#if running}
      <button class="ui-btn danger-soft" onclick={stop}><Square size={13} /> Stop</button>
    {:else}
      <button class="ui-btn primary" disabled={!iface} onclick={start}><Play size={13} /> Capture here</button>
      <a class="ui-btn" class:disabled={!iface} href={iface ? ent.captureURL(iface, filter, count, seconds, true) : undefined} download><Download size={13} /> Save as .pcap</a>
    {/if}
    <span class="status">{status}</span>
  </div>
  <pre class="out mono">{lines.join('\n') || 'No packets yet.'}</pre>
  <p class="hint">The .pcap download starts when the capture ends ({count} packets or {seconds} s). Needs tcpdump on the host.</p>
</Dialog>

<style>
  .form { display: flex; gap: 10px; flex-wrap: wrap; align-items: end; }
  .grow { flex: 1; min-width: 240px; }
  .bar { display: flex; gap: 8px; align-items: center; margin: 10px 0; }
  .status { color: var(--text-2); font-size: 13px; }
  .out { height: 340px; overflow: auto; background: var(--bg-elev); border: 1px solid var(--border); padding: 8px; font-size: 11.5px; margin: 0; white-space: pre; }
  .hint { color: var(--text-2); font-size: 12px; margin: 8px 0 0; }
  .disabled { pointer-events: none; opacity: 0.5; }
</style>
