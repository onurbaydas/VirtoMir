<script lang="ts">
  import { onDestroy } from 'svelte'
  import Dialog from './Dialog.svelte'
  import Download from '@lucide/svelte/icons/download'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Radio from '@lucide/svelte/icons/radio'
  import { toasts } from '../toast.svelte'

  interface Props {
    open: boolean
    onclose: () => void
  }
  let { open, onclose }: Props = $props()

  let iface = $state('vmbr0')
  let filter = $state('tcp port 80 or tcp port 443')
  let capturing = $state(false)
  let packetCount = $state(0)
  let bytesCount = $state(0)
  let timer: ReturnType<typeof setInterval> | undefined

  interface PacketSample {
    time: string
    src: string
    dst: string
    proto: string
    len: number
    info: string
  }
  let samplePackets = $state<PacketSample[]>([])

  function startCapture() {
    capturing = true
    packetCount = 0
    bytesCount = 0
    samplePackets = []
    toasts.push({ kind: 'info', title: `PCAP live capture started on ${iface}` })

    timer = setInterval(() => {
      const added = Math.floor(Math.random() * 8) + 2
      packetCount += added
      bytesCount += added * 128
      const now = new Date().toISOString().slice(11, 23)
      const sample: PacketSample = {
        time: now,
        src: `192.168.100.${Math.floor(Math.random() * 50) + 10}:49${Math.floor(Math.random() * 800)}`,
        dst: `10.0.0.1:443`,
        proto: 'TCP',
        len: 128,
        info: 'ACK, PSH Seq=12048 Ack=8920 Win=64240',
      }
      samplePackets = [sample, ...samplePackets.slice(0, 19)]
    }, 400)
  }

  function stopCapture() {
    capturing = false
    clearInterval(timer)
  }

  function downloadPcap() {
    stopCapture()
    const content = `PCAP-DUMP-BINARY-MOCK-${Date.now()}`
    const blob = new Blob([content], { type: 'application/vnd.tcpdump.pcap' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `capture-${iface}-${Date.now()}.pcap`
    a.click()
    URL.revokeObjectURL(url)
    toasts.push({ kind: 'info', title: 'PCAP file downloaded', detail: `${packetCount} packets saved.` })
  }

  onDestroy(() => clearInterval(timer))
</script>

<Dialog {open} title="" {onclose} width={640}>
  <div class="sniffer-dialog">
    <div class="sniffer-header mono">
      <div class="title-wrap">
        <Radio size={16} />
        <span>LIVE PCAP PACKET SNIFFER & WIRE INSPECTOR</span>
      </div>
      {#if capturing}
        <span class="live-tag"><span class="dot"></span>CAPTURING</span>
      {/if}
    </div>

    <div class="sniffer-body mono">
      <div class="control-row">
        <label class="param-field">
          <span class="lbl">CAPTURE INTERFACE:</span>
          <select class="ui-input" bind:value={iface} disabled={capturing}>
            <option value="vmbr0">vmbr0 (Linux Bridge Aggregate)</option>
            <option value="eno1">eno1 (10G SFP+ Uplink 1)</option>
            <option value="eno2">eno2 (10G SFP+ Uplink 2)</option>
            <option value="virbr0">virbr0 (NAT Network)</option>
            <option value="vnet0">vnet0 (Virtual Guest NIC)</option>
          </select>
        </label>

        <label class="param-field">
          <span class="lbl">BPF FILTER EXPRESSION:</span>
          <input
            type="text"
            class="ui-input"
            bind:value={filter}
            placeholder="e.g. tcp port 80 or icmp"
            disabled={capturing}
          />
        </label>
      </div>

      <!-- Live Counters Deck -->
      <div class="counters-deck">
        <div class="counter-box">
          <span class="c-title">PACKETS CAPTURED</span>
          <span class="c-val">{packetCount.toLocaleString()}</span>
        </div>
        <div class="counter-box">
          <span class="c-title">BUFFER ACCUMULATED</span>
          <span class="c-val">{(bytesCount / 1024).toFixed(1)} KiB</span>
        </div>
        <div class="counter-box">
          <span class="c-title">SOCKET STATUS</span>
          <span class="c-val ok">{capturing ? 'SOCK_RAW PROMISC' : 'IDLE'}</span>
        </div>
      </div>

      <!-- Live Sample Feed -->
      <div class="stream-box">
        <div class="stream-head">LIVE FRAME TELEMETRY PREVIEW:</div>
        <div class="stream-scroll">
          {#each samplePackets as p (p.time + p.src)}
            <div class="packet-line">
              <span class="time">{p.time}</span>
              <span class="flow">{p.src} → {p.dst}</span>
              <span class="proto">{p.proto}</span>
              <span class="info">{p.info}</span>
            </div>
          {:else}
            <div class="empty-feed">Press START to initiate packet sniffing on {iface}...</div>
          {/each}
        </div>
      </div>
    </div>

    <div class="sniffer-footer mono">
      <button class="ui-btn" onclick={onclose}>CLOSE</button>
      {#if !capturing}
        <button class="ui-btn primary" onclick={startCapture}>
          <Play size={13} />
          <span>START CAPTURE</span>
        </button>
      {:else}
        <button class="ui-btn warn" onclick={stopCapture}>
          <Square size={13} />
          <span>STOP</span>
        </button>
      {/if}
      <button class="ui-btn accent" disabled={packetCount === 0} onclick={downloadPcap}>
        <Download size={13} />
        <span>DOWNLOAD .PCAP</span>
      </button>
    </div>
  </div>
</Dialog>

<style>
  .sniffer-dialog {
    display: flex;
    flex-direction: column;
    margin: -16px -20px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
  }
  .sniffer-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 18px;
    background: var(--surface);
    border-bottom: 2px solid var(--ink);
  }
  .title-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    font-weight: 800;
    color: var(--accent);
  }
  .live-tag {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 10px;
    font-weight: 700;
    color: var(--danger);
  }
  .dot {
    width: 6px;
    height: 6px;
    background: var(--danger);
    box-shadow: 0 0 6px var(--danger);
    animation: blink 0.8s infinite;
  }
  @keyframes blink {
    50% { opacity: 0.3; }
  }
  .sniffer-body {
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    font-size: 11px;
  }
  .control-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .param-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .lbl {
    font-size: 10px;
    color: var(--text-3);
    font-weight: 700;
  }
  .counters-deck {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }
  .counter-box {
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .c-title {
    font-size: 9.5px;
    color: var(--text-3);
    font-weight: 700;
  }
  .c-val {
    font-size: 13px;
    font-weight: 700;
    color: var(--text);
  }
  .c-val.ok {
    color: var(--ok);
  }
  .stream-box {
    border: 1px solid var(--border-strong);
    background: #0d0e11;
    display: flex;
    flex-direction: column;
  }
  .stream-head {
    padding: 6px 10px;
    background: #18191e;
    border-bottom: 1px solid #2d2f35;
    font-size: 10px;
    color: var(--text-3);
    font-weight: 700;
  }
  .stream-scroll {
    height: 150px;
    overflow-y: auto;
    padding: 6px 10px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .packet-line {
    display: flex;
    gap: 8px;
    font-size: 10px;
    line-height: 1.4;
  }
  .time {
    color: var(--text-3);
  }
  .flow {
    color: #4a9eff;
  }
  .proto {
    color: var(--warn);
    font-weight: 700;
  }
  .info {
    color: var(--text-2);
  }
  .empty-feed {
    text-align: center;
    padding: 40px;
    color: var(--text-3);
  }
  .sniffer-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    background: var(--surface);
    border-top: 1px solid var(--border-strong);
  }
  .ui-btn.warn {
    background: var(--warn);
    color: #000;
  }
</style>
