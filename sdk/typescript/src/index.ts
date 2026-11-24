// Client for the VirtoMir API (/api/v1). Sign in with an API token made on the Account page.

/** Power and lifecycle actions a machine accepts. */
export type PowerAction =
  | 'start'
  | 'shutdown'
  | 'reboot'
  | 'force-off'
  | 'pause'
  | 'resume'
  | 'reset'
  | 'save';

/** A machine as the list shows it. */
export interface Machine {
  uuid: string;
  name: string;
  state: string;
  vcpus: number;
  maxMemoryKiB: number;
  autostart: boolean;
  template?: boolean;
  [field: string]: unknown;
}

export interface Snapshot {
  name: string;
  description?: string;
  parent?: string;
  state: string;
  /** Unix seconds. */
  created: number;
  current: boolean;
  memory: boolean;
  location: string;
}

export interface ClientOptions {
  /** API token (Account → API tokens). Sent as `Authorization: Bearer <token>`. */
  token?: string;
  /** Route every call to this cluster member: /api/v1/hosts/<host>/… */
  host?: string;
  /** A fetch to use instead of the global one (e.g. one that trusts a self-signed certificate). */
  fetch?: typeof fetch;
}

/** An error answer of the API. */
export class VirtoMirError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(`VirtoMir API ${status}: ${message}`);
    this.name = 'VirtoMirError';
  }
  get notFound(): boolean {
    return this.status === 404;
  }
}

/** A machine stops counting as on in these states. */
export function isRunning(m: Pick<Machine, 'state'>): boolean {
  return !['', 'shutoff', 'crashed', 'pmsuspended'].includes(m.state);
}

const seg = encodeURIComponent;

export class VirtoMirClient {
  readonly baseUrl: string;
  private readonly opts: ClientOptions;

  /** baseUrl looks like https://hv1:8420 */
  constructor(baseUrl: string, opts: ClientOptions = {}) {
    let u: URL | undefined;
    try {
      u = new URL(baseUrl);
    } catch {
      /* reported below */
    }
    if (!u || !/^https?:$/.test(u.protocol) || !u.host) {
      throw new Error(`url must look like https://host:8420, not ${JSON.stringify(baseUrl)}`);
    }
    this.baseUrl = u.toString().replace(/\/+$/, '');
    this.opts = opts;
  }

  private path(p: string): string {
    if (this.opts.host && p.startsWith('/api/v1/')) {
      return `/api/v1/hosts/${seg(this.opts.host)}${p.slice('/api/v1'.length)}`;
    }
    return p;
  }

  /** Sends one request; body (if given) is JSON. Returns the JSON answer, or undefined for an empty one. */
  async request<T = unknown>(method: string, p: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.opts.token) headers.Authorization = `Bearer ${this.opts.token}`;
    const f = this.opts.fetch ?? fetch;
    const res = await f(this.baseUrl + this.path(p), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) {
      let msg = text.trim();
      try {
        const e = JSON.parse(text) as { error?: string };
        if (e.error) msg = e.error;
      } catch {
        /* not JSON */
      }
      throw new VirtoMirError(res.status, msg || res.statusText);
    }
    return (text ? JSON.parse(text) : undefined) as T;
  }

  // ---- machines ------------------------------------------------------------------------

  async listMachines(): Promise<Machine[]> {
    const r = await this.request<{ domains?: Machine[] | null }>('GET', '/api/v1/domains');
    return r?.domains ?? [];
  }

  /** The machine page's full model of one machine. */
  getMachine(uuid: string): Promise<Record<string, unknown>> {
    return this.request('GET', `/api/v1/domains/${seg(uuid)}`);
  }

  /** Finds a machine by name or UUID; undefined when there is none. */
  async findMachine(nameOrUuid: string): Promise<Machine | undefined> {
    const want = nameOrUuid.toLowerCase();
    return (await this.listMachines()).find((m) => m.name === nameOrUuid || m.uuid.toLowerCase() === want);
  }

  async power(uuid: string, action: PowerAction): Promise<void> {
    await this.request('POST', `/api/v1/domains/${seg(uuid)}/actions/${seg(action)}`);
  }

  async setAutostart(uuid: string, enabled: boolean): Promise<void> {
    await this.request('PUT', `/api/v1/domains/${seg(uuid)}/autostart`, { enabled });
  }

  /** Deletes a machine; with removeStorage its disks go too. */
  async deleteMachine(uuid: string, removeStorage = false): Promise<void> {
    await this.request('DELETE', `/api/v1/domains/${seg(uuid)}${removeStorage ? '?storage=remove' : ''}`);
  }

  // ---- snapshots -----------------------------------------------------------------------

  async listSnapshots(uuid: string): Promise<Snapshot[]> {
    const r = await this.request<{ snapshots?: Snapshot[] | null }>('GET', `/api/v1/domains/${seg(uuid)}/snapshots`);
    return r?.snapshots ?? [];
  }

  /** Takes a snapshot and returns its name (an empty name lets VirtoMir pick one). */
  async createSnapshot(uuid: string, name = '', description = ''): Promise<string> {
    const r = await this.request<{ name: string }>('POST', `/api/v1/domains/${seg(uuid)}/snapshots`, { name, description });
    return r.name;
  }

  /** Goes back to a snapshot. With safety (the default) the current state is snapshotted first. */
  revertSnapshot(uuid: string, name: string, safety = true): Promise<unknown> {
    return this.request('POST', `/api/v1/domains/${seg(uuid)}/snapshots/${seg(name)}/revert${safety ? '' : '?safety=false'}`);
  }

  async deleteSnapshot(uuid: string, name: string): Promise<void> {
    await this.request('DELETE', `/api/v1/domains/${seg(uuid)}/snapshots/${seg(name)}`);
  }
}
