// spice-html5 (LGPL-3.0-or-later) ships plain ES modules without types.
declare module '@spice-project/spice-html5' {
  export interface SpiceOptions {
    uri: string
    password?: string
    screen_id?: string
    message_id?: string
    onerror?: (e: Error) => void
    onsuccess?: () => void
    onagent?: (sc: SpiceMainConn) => void
  }
  export class SpiceMainConn {
    constructor(o: SpiceOptions)
    stop(): void
    screen_id: string
  }
  export function resize_helper(sc: SpiceMainConn): void
  export function handle_resize(e?: Event): void
  export function sendCtrlAltDel(): void
}
