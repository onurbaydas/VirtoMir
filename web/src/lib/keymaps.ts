// Guest keyboard layouts for "Type" in the console (P6). QEMU's extended key events send
// physical key positions and the guest's own layout turns them into characters, so to type
// "ş" into a Turkish guest we must press the key where ş sits on a Turkish keyboard (the
// US ";" position). Each layout lists, per physical row, the character each key produces
// plain, with Shift and with AltGr.

export type LayoutId = 'us' | 'uk' | 'tr-q' | 'de'

export const LAYOUTS: { id: LayoutId; label: string }[] = [
  { id: 'us', label: 'English (US)' },
  { id: 'uk', label: 'English (UK)' },
  { id: 'tr-q', label: 'Turkish Q' },
  { id: 'de', label: 'German' },
]

// Physical keys (KeyboardEvent.code) in row order.
const ROW_DIGITS = ['Backquote', 'Digit1', 'Digit2', 'Digit3', 'Digit4', 'Digit5', 'Digit6', 'Digit7', 'Digit8', 'Digit9', 'Digit0', 'Minus', 'Equal']
const ROW_Q = ['KeyQ', 'KeyW', 'KeyE', 'KeyR', 'KeyT', 'KeyY', 'KeyU', 'KeyI', 'KeyO', 'KeyP', 'BracketLeft', 'BracketRight', 'Backslash']
const ROW_A = ['KeyA', 'KeyS', 'KeyD', 'KeyF', 'KeyG', 'KeyH', 'KeyJ', 'KeyK', 'KeyL', 'Semicolon', 'Quote']
const ROW_Z = ['IntlBackslash', 'KeyZ', 'KeyX', 'KeyC', 'KeyV', 'KeyB', 'KeyN', 'KeyM', 'Comma', 'Period', 'Slash']

/** One row: plain, shift and altgr characters, aligned with the row's keys ("\0" = none). */
type Row = [plain: string, shift: string, altgr?: string]

interface Layout {
  digits: Row
  q: Row
  a: Row
  z: Row
}

// "\0" marks a key that produces nothing in that layer (or does not exist, like the ISO key
// on US keyboards).
const DEF: Record<LayoutId, Layout> = {
  us: {
    digits: ['`1234567890-=', '~!@#$%^&*()_+'],
    q: ['qwertyuiop[]\\', 'QWERTYUIOP{}|'],
    a: ["asdfghjkl;'", 'ASDFGHJKL:"'],
    z: ['\0zxcvbnm,./', '\0ZXCVBNM<>?'],
  },
  uk: {
    digits: ['`1234567890-=', '¬!"£$%^&*()_+', '¦\0\0\0€\0\0\0\0\0\0\0\0'],
    q: ['qwertyuiop[]#', 'QWERTYUIOP{}~'],
    a: ["asdfghjkl;'", 'ASDFGHJKL:@'],
    z: ['\\zxcvbnm,./', '|ZXCVBNM<>?'],
  },
  'tr-q': {
    digits: ['"1234567890*-', "é!'^+%&/()=?_", '\0>£#$½\0{[]}\\|'],
    q: ['qwertyuıopğü,', 'QWERTYUIOPĞÜ;', '@\0€\0\0\0\0\0\0\0¨~`'],
    a: ['asdfghjklşi', 'ASDFGHJKLŞİ', 'æß\0\0\0\0\0\0\0´\0'],
    z: ['<zxcvbnmöç.', '>ZXCVBNMÖÇ:', '|\0\0\0\0\0\0\0\0\0\0'],
  },
  de: {
    digits: ['^1234567890ß´', '°!"§$%&/()=?`', '\0\0²³\0\0\0{[]}\\\0'],
    q: ['qwertzuiopü+#', "QWERTZUIOPÜ*'", '@\0€\0\0\0\0\0\0\0\0~\0'],
    a: ['asdfghjklöä', 'ASDFGHJKLÖÄ'],
    z: ['<yxcvbnm,.-', '>YXCVBNM;:_', '|\0\0\0\0\0\0µ\0\0\0'],
  },
}

export interface KeyStroke {
  code: string
  shift: boolean
  altgr: boolean
}

const cache = new Map<LayoutId, Map<string, KeyStroke>>()

/** Which physical key (and modifiers) types ch on a guest with this layout. */
export function strokeFor(layout: LayoutId, ch: string): KeyStroke | undefined {
  let m = cache.get(layout)
  if (!m) {
    m = new Map()
    const def = DEF[layout]
    const rows: [string[], Row][] = [
      [ROW_DIGITS, def.digits],
      [ROW_Q, def.q],
      [ROW_A, def.a],
      [ROW_Z, def.z],
    ]
    // Plain keys first, then Shift, then AltGr: the simplest way to type a character wins.
    for (const [layer, sh, ag] of [[0, false, false], [1, true, false], [2, false, true]] as const) {
      for (const [codes, row] of rows) {
        const chars = row[layer]
        if (!chars) continue
        Array.from(chars).forEach((c, i) => {
          if (c !== '\0' && i < codes.length && !m!.has(c)) m!.set(c, { code: codes[i], shift: sh, altgr: ag })
        })
      }
    }
    m.set(' ', { code: 'Space', shift: false, altgr: false })
    m.set('\n', { code: 'Enter', shift: false, altgr: false })
    m.set('\t', { code: 'Tab', shift: false, altgr: false })
    cache.set(layout, m)
  }
  return m.get(ch)
}

/** A sensible default for the guest layout: the browser's language. */
export function guessLayout(lang = typeof navigator !== 'undefined' ? navigator.language : 'en-US'): LayoutId {
  const l = lang.toLowerCase()
  if (l.startsWith('tr')) return 'tr-q'
  if (l.startsWith('de')) return 'de'
  if (l === 'en-gb') return 'uk'
  return 'us'
}

/** X11 keysym of a character (what noVNC sends alongside the key code). */
export function keysym(ch: string): number {
  if (ch === '\n') return 0xff0d
  if (ch === '\t') return 0xff09
  const cp = ch.codePointAt(0)!
  return cp < 0x100 ? cp : 0x01000000 + cp
}
