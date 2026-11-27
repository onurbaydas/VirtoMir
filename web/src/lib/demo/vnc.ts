// Demo only: a pretend VNC server for the Screen console. It speaks enough RFB 3.8 for noVNC
// (no authentication, raw encoding) and shows one still picture: what the machine's screen
// could look like, with a note that the live screen needs a real host.
import type { DemoMachine } from './world'

const W = 1024
const H = 640

// picture draws the still screen of a machine.
function picture(m: DemoMachine): ImageData {
  const c = document.createElement('canvas')
  c.width = W
  c.height = H
  const g = c.getContext('2d')!
  const mono = '16px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace'
  if (m.os === 'windows') {
    const bg = g.createLinearGradient(0, 0, W, H)
    bg.addColorStop(0, '#0b3d91')
    bg.addColorStop(1, '#1e78d6')
    g.fillStyle = bg
    g.fillRect(0, 0, W, H)
    g.fillStyle = 'rgba(0,0,0,.35)'
    g.fillRect(0, H - 44, W, 44)
    g.fillStyle = '#fff'
    g.font = '600 64px system-ui, sans-serif'
    g.fillText(new Date().toTimeString().slice(0, 5), 60, 140)
    g.font = '24px system-ui, sans-serif'
    g.fillText(new Date().toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' }), 62, 180)
  } else {
    g.fillStyle = '#000'
    g.fillRect(0, 0, W, H)
    g.fillStyle = '#c8c8c8'
    g.font = mono
    const os = { debian: 'Debian GNU/Linux 13', ubuntu: 'Ubuntu 24.04.1 LTS', rocky: 'Rocky Linux 9.5 (Blue Onyx)', alpine: 'Welcome to Alpine Linux 3.22' }[m.os]
    const lines = [`${os} ${m.name} tty1`, '', `${m.name} login: `]
    lines.forEach((l, i) => g.fillText(l, 12, 28 + i * 22))
    g.fillRect(12 + g.measureText(lines[2]).width, 28 + 2 * 22 - 14, 9, 17)
  }
  // The note.
  const note = 'Demo: a still picture. The live screen needs a real VirtoMir host; the Text console works here.'
  g.font = '15px system-ui, sans-serif'
  const w = g.measureText(note).width + 32
  g.fillStyle = 'rgba(255, 196, 0, .92)'
  g.fillRect((W - w) / 2, H - 110, w, 38)
  g.fillStyle = '#1a1a1a'
  g.fillText(note, (W - w) / 2 + 16, H - 85)
  return g.getImageData(0, 0, W, H)
}

/**
 * vncServer answers a noVNC client: send(bytes) delivers to it, the returned function takes
 * what it sends.
 */
export function vncServer(m: DemoMachine, send: (b: Uint8Array) => void): (b: Uint8Array) => void {
  let buf = new Uint8Array(0)
  let stage: 'version' | 'security' | 'init' | 'normal' = 'version'
  // The client's pixel format: 32 bits, shifts of red/green/blue, byte order.
  let fmt = { big: false, rs: 16, gs: 8, bs: 0 }
  let img: ImageData | null = null
  let sentOnce = false

  // After the caller has the input function: the client answers at once.
  setTimeout(() => send(new TextEncoder().encode('RFB 003.008\n')))

  function frame() {
    img ??= picture(m)
    const out = new Uint8Array(4 + 12 + W * H * 4)
    const v = new DataView(out.buffer)
    v.setUint8(0, 0) // FramebufferUpdate
    v.setUint16(2, 1) // one rectangle
    v.setUint16(4, 0)
    v.setUint16(6, 0)
    v.setUint16(8, W)
    v.setUint16(10, H)
    v.setInt32(12, 0) // raw
    const px = img.data
    for (let i = 0, o = 16; i < W * H; i++, o += 4) {
      const p = (px[i * 4] << fmt.rs) | (px[i * 4 + 1] << fmt.gs) | (px[i * 4 + 2] << fmt.bs)
      v.setUint32(o, p >>> 0, !fmt.big)
    }
    send(out)
  }

  // need returns n bytes when they have arrived (and takes them), else null.
  function need(n: number): Uint8Array | null {
    if (buf.length < n) return null
    const out = buf.slice(0, n)
    buf = buf.slice(n)
    return out
  }

  return (data: Uint8Array) => {
    const next = new Uint8Array(buf.length + data.length)
    next.set(buf)
    next.set(data, buf.length)
    buf = next
    for (;;) {
      if (stage === 'version') {
        if (!need(12)) return
        send(new Uint8Array([1, 1])) // one security type: None
        stage = 'security'
      } else if (stage === 'security') {
        if (!need(1)) return
        send(new Uint8Array([0, 0, 0, 0])) // SecurityResult: OK
        stage = 'init'
      } else if (stage === 'init') {
        if (!need(1)) return // ClientInit (shared flag)
        const name = new TextEncoder().encode(m.name)
        const si = new Uint8Array(24 + name.length)
        const v = new DataView(si.buffer)
        v.setUint16(0, W)
        v.setUint16(2, H)
        si.set([32, 24, 0, 1], 4) // bpp, depth, big-endian, true colour
        v.setUint16(8, 255)
        v.setUint16(10, 255)
        v.setUint16(12, 255)
        si.set([16, 8, 0], 14)
        v.setUint32(20, name.length)
        si.set(name, 24)
        send(si)
        stage = 'normal'
      } else {
        if (buf.length < 1) return
        const type = buf[0]
        let len = 0
        if (type === 0) len = 20 // SetPixelFormat
        else if (type === 2) {
          if (buf.length < 4) return
          len = 4 + 4 * ((buf[2] << 8) | buf[3]) // SetEncodings
        } else if (type === 3) len = 10 // FramebufferUpdateRequest
        else if (type === 4) len = 8 // KeyEvent
        else if (type === 5) len = 6 // PointerEvent
        else if (type === 6) {
          if (buf.length < 8) return
          len = 8 + new DataView(buf.buffer, buf.byteOffset).getUint32(4) // ClientCutText
        } else {
          buf = new Uint8Array(0) // something this pretend server does not know: drop it
          return
        }
        const msg = need(len)
        if (!msg) return
        if (type === 0) fmt = { big: msg[6] !== 0, rs: msg[14 + 0], gs: msg[15], bs: msg[16] }
        if (type === 3 && (!msg[1] || !sentOnce)) {
          sentOnce = true
          frame()
        }
      }
    }
  }
}
