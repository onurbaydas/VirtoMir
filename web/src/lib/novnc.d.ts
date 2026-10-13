// Minimal types for the parts of noVNC (MPL-2.0) that VirtoMir uses.
declare module '@novnc/novnc' {
  export interface RFBOptions {
    shared?: boolean
    credentials?: { username?: string; password?: string; target?: string }
    wsProtocols?: string[]
  }
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string, options?: RFBOptions)
    viewOnly: boolean
    focusOnClick: boolean
    scaleViewport: boolean
    resizeSession: boolean
    clipViewport: boolean
    showDotCursor: boolean
    qualityLevel: number
    compressionLevel: number
    background: string
    readonly capabilities: { power?: boolean }
    disconnect(): void
    sendCredentials(creds: { username?: string; password?: string }): void
    sendCtrlAltDel(): void
    sendKey(keysym: number, code: string | null, down?: boolean): void
    focus(options?: FocusOptions): void
    blur(): void
    clipboardPasteFrom(text: string): void
    toBlob(callback: (blob: Blob | null) => void, type?: string, quality?: number): void
  }
}
