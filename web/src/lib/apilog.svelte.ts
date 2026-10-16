// The API inspector: the last calls this browser made to virtomird, so people can see (and
// copy as curl) exactly what each button does. Kept in memory only.

export interface ApiCall {
  time: number
  method: string
  path: string
  status: number
  ms: number
}

const KEEP = 200

class ApiLog {
  calls = $state.raw<ApiCall[]>([])

  add(c: ApiCall) {
    // Live streams and pollers would drown everything else.
    if (c.method === 'GET' && /\/(stream|summary|jobs|alerts)(\?|$)/.test(c.path)) return
    this.calls = [c, ...this.calls].slice(0, KEEP)
  }

  clear() {
    this.calls = []
  }
}

export const apiLog = new ApiLog()

/** The call as a curl command against this server, authenticating with an API token. */
export function asCurl(c: ApiCall): string {
  const method = c.method === 'GET' ? '' : ` -X ${c.method}`
  return `curl -k${method} -H 'Authorization: Bearer $VIRTOMIR_TOKEN' '${location.origin}${c.path}'`
}
