<script lang="ts">
  import { session } from '../session.svelte'
  import Network from '@lucide/svelte/icons/network'
  import Plus from '@lucide/svelte/icons/plus'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Trash from '@lucide/svelte/icons/trash'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Pin from '@lucide/svelte/icons/pin'
  import { api, errorText, type Lease, type NetworkSpec, type VNetwork } from '../api'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'

  let nets = $state<VNetwork[] | null>(null)
  let error = $state('')
  let open = $state<string | null>(null)
  let leases = $state<Record<string, Lease[] | string>>({})

  async function load() {
    try {
      nets = await api.networks()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  async function toggle(n: VNetwork) {
    open = open === n.name ? null : n.name
    if (open && n.active) {
      try {
        leases[n.name] = await api.leases(n.name)
      } catch (e) {
        leases[n.name] = errorText(e)
      }
    }
  }

  async function act(n: VNetwork, action: 'start' | 'stop') {
    try {
      await api.networkAction(n.name, action)
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: `Could not ${action} ${n.name}`, detail: errorText(e) })
    }
  }
  async function autostart(n: VNetwork, on: boolean) {
    try {
      await api.networkAutostart(n.name, on)
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Autostart not changed', detail: errorText(e) })
    }
  }

  const MODE: Record<string, { label: string; hint: string }> = {
    nat: { label: 'NAT', hint: 'Machines reach the outside world through the host; they are not reachable from the LAN.' },
    route: { label: 'Routed', hint: 'Machines get their own subnet, routed by the host (needs a route on your router).' },
    open: { label: 'Open', hint: 'Routed without any firewall rules from libvirt.' },
    isolated: { label: 'Isolated', hint: 'Machines only talk to each other and to the host.' },
    bridge: { label: 'Bridged', hint: 'Machines join the physical LAN through a host bridge, like real computers.' },
  }
  const modeLabel = (m: string) => MODE[m]?.label ?? m
  const netLine = (n: VNetwork) =>
    [n.subnet, n.bridge && `bridge ${n.bridge}`, n.dhcpStart && `DHCP ${n.dhcpStart} – ${n.dhcpEnd}`].filter(Boolean).join(' · ')

  // ---- New network ------------------------------------------------------------------
  let dlg = $state(false)
  let spec = $state<NetworkSpec>({ name: '', mode: 'nat', subnet: '192.168.100.0/24', dhcp: true, bridge: '' })
  let busy = $state(false)
  let formErr = $state('')
  function openNew() {
    // Suggest a free 192.168.X.0/24.
    const used = new Set((nets ?? []).map((n) => n.subnet?.split('.').slice(0, 3).join('.')))
    let x = 100
    while (used.has(`192.168.${x}`) && x < 250) x++
    spec = { name: '', mode: 'nat', subnet: `192.168.${x}.0/24`, dhcp: true, bridge: '' }
    formErr = ''
    dlg = true
  }
  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    formErr = ''
    try {
      const body: NetworkSpec =
        spec.mode === 'bridge' ? { name: spec.name, mode: 'bridge', bridge: spec.bridge, dhcp: false } : { ...spec, bridge: undefined }
      await api.createNetwork(body)
      dlg = false
      toasts.push({ kind: 'info', title: `Network ${spec.name} created` })
      await load()
    } catch (err) {
      formErr = errorText(err)
    } finally {
      busy = false
    }
  }

  // ---- Reservations -------------------------------------------------------------------
  let resNet = $state<VNetwork | null>(null)
  let resMAC = $state('')
  let resIP = $state('')
  let resName = $state('')
  let resErr = $state('')
  let resBusy = $state(false)
  function openReserve(n: VNetwork, l?: Lease) {
    resNet = n
    resMAC = l?.mac ?? ''
    resIP = l?.ip.split('/')[0] ?? ''
    resName = l?.machine ?? l?.hostname ?? ''
    resErr = ''
  }
  async function reserve(e: SubmitEvent) {
    e.preventDefault()
    if (!resNet) return
    resBusy = true
    resErr = ''
    try {
      await api.addDHCPHost(resNet.name, { mac: resMAC.trim(), ip: resIP.trim(), name: resName.trim() || undefined })
      toasts.push({ kind: 'info', title: `${resIP} reserved`, detail: 'The machine gets it the next time it renews its lease (or reboots).' })
      resNet = null
      await load()
    } catch (err) {
      resErr = errorText(err)
    } finally {
      resBusy = false
    }
  }
  const reserved = (n: VNetwork, mac: string) => n.dhcpHosts?.some((h) => h.mac === mac)

  const expires = (s: number) => {
    const m = Math.round((s * 1000 - Date.now()) / 60000)
    return m <= 0 ? 'expired' : m < 60 ? `${m} min` : `${Math.round(m / 60)} h`
  }
</script>

<svelte:head><title>Networks · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Networks</h1>
    <p class="lead">Virtual networks connect machines to each other, to the host and to the outside world.</p>
  </div>
  <button class="ui-btn primary" disabled={!session.can('admin')} title={session.why('admin')} onclick={openNew}><Plus size={15} /> New network</button>
</header>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if nets === null}
  <p class="muted">Loading…</p>
{:else if nets.length === 0}
  <div class="empty ui-panel">
    <Network size={28} strokeWidth={1.5} />
    <strong>No virtual networks</strong>
    <span>Create a NAT network so machines can reach the internet.</span>
  </div>
{:else}
  <div class="list">
    {#each nets as n (n.name)}
      <article class="net ui-panel" class:open={open === n.name}>
        <button class="head" aria-expanded={open === n.name} onclick={() => toggle(n)}>
          <span class="icon" class:on={n.active}><Network size={17} /></span>
          <span class="titles">
            <span class="name">{n.name} <span class="mode" title={MODE[n.mode]?.hint}>{modeLabel(n.mode)}</span></span>
            <span class="sub mono">{netLine(n)}</span>
          </span>
          <span class="state" class:on={n.active}>{n.active ? 'active' : 'stopped'}</span>
          <span class="users">{n.usedBy.length ? `${n.usedBy.length} machine${n.usedBy.length > 1 ? 's' : ''}` : 'unused'}</span>
          <span class="chev"><ChevronDown size={16} /></span>
        </button>

        {#if open === n.name}
          <div class="body">
            <p class="hint">{MODE[n.mode]?.hint ?? ''}</p>
            <div class="controls" inert={!session.can('admin')} title={session.why('admin')}>
              <label class="ui-switch small">
                <input type="checkbox" checked={n.autostart} disabled={!n.persistent} onchange={(e) => autostart(n, e.currentTarget.checked)} />
                Start with the host
              </label>
              <span class="spacer"></span>
              {#if n.active}
                <ConfirmButton
                  label="Stop"
                  confirm={n.usedBy.length ? 'Machines lose network — stop?' : 'Stop network?'}
                  icon={Square}
                  run={() => act(n, 'stop')}
                />
              {:else}
                <button class="ui-btn sm" onclick={() => act(n, 'start')}><Play size={14} /> Start</button>
              {/if}
              <ConfirmButton
                label="Delete"
                confirm="Delete network?"
                icon={Trash}
                disabled={n.usedBy.length > 0}
                title={n.usedBy.length ? 'Used by ' + n.usedBy.join(', ') : 'Delete this network'}
                failure="Could not delete {n.name}"
                run={async () => {
                  await api.deleteNetwork(n.name)
                  toasts.push({ kind: 'info', title: `Network ${n.name} deleted` })
                  open = null
                  await load()
                }}
              />
            </div>

            {#if n.usedBy.length}
              <div class="block">
                <h3>Connected machines</h3>
                <div class="chips">{#each n.usedBy as m (m)}<span class="chip">{m}</span>{/each}</div>
              </div>
            {/if}

            {#if n.dhcpStart}
              <div class="block">
                <div class="block-head">
                  <h3>Reserved addresses</h3>
                  <button class="ui-btn sm ghost" inert={!session.can('admin')} onclick={() => openReserve(n)}><Pin size={14} /> Reserve an address</button>
                </div>
                {#if !n.dhcpHosts?.length}
                  <p class="muted">None. Reserve one so a machine always gets the same IP.</p>
                {:else}
                  <div class="leases" role="table">
                    {#each n.dhcpHosts as h (h.mac)}
                      <div class="lease" role="row">
                        <span class="mono" role="cell">{h.ip}</span>
                        <span role="cell">{h.name ?? '—'}</span>
                        <span class="mono muted" role="cell">{h.mac}</span>
                        <span role="cell" inert={!session.can('admin')}>
                          <ConfirmButton
                            label="Remove"
                            confirm="Remove?"
                            failure="Could not remove the reservation"
                            run={async () => {
                              await api.deleteDHCPHost(n.name, h.mac)
                              await load()
                            }}
                          />
                        </span>
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>

              <div class="block">
                <h3>DHCP leases</h3>
                {#if !n.active}
                  <p class="muted">Start the network to see leases.</p>
                {:else if typeof leases[n.name] === 'string'}
                  <div class="ui-error-box">{leases[n.name]}</div>
                {:else if !leases[n.name]}
                  <p class="muted">Loading…</p>
                {:else if (leases[n.name] as Lease[]).length === 0}
                  <p class="muted">No leases handed out right now.</p>
                {:else}
                  <div class="leases" role="table">
                    {#each leases[n.name] as Lease[] as l (l.mac + l.ip)}
                      <div class="lease" role="row">
                        <span class="mono" role="cell">{l.ip}</span>
                        <span role="cell">{l.machine ?? l.hostname ?? '—'}</span>
                        <span class="mono muted" role="cell">{l.mac}</span>
                        <span class="muted" role="cell" title="Lease expires in">
                          {#if reserved(n, l.mac)}<span class="pinned"><Pin size={12} /> fixed</span>{:else}
                            {expires(l.expires)}
                            {#if session.can('admin')}<button class="pin" title="Always give this machine this address" onclick={() => openReserve(n, l)}><Pin size={13} /></button>{/if}
                          {/if}
                        </span>
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        {/if}
      </article>
    {/each}
  </div>
{/if}

<Dialog open={resNet !== null} title="Reserve an address on {resNet?.name ?? ''}" onclose={() => (resNet = null)}>
  <form id="res-form" class="form" onsubmit={reserve}>
    <label class="ui-field">
      <span class="label">MAC address of the machine's adapter</span>
      <input class="ui-input mono" bind:value={resMAC} placeholder="52:54:00:12:34:56" required />
    </label>
    <div class="two-col">
      <label class="ui-field">
        <span class="label">IP address</span>
        <input class="ui-input mono" bind:value={resIP} placeholder={resNet?.subnet?.replace(/\.\d+\/\d+$/, '.50')} required />
      </label>
      <label class="ui-field">
        <span class="label">Host name (optional)</span>
        <input class="ui-input" bind:value={resName} />
      </label>
    </div>
    <span class="hint">Must be inside {resNet?.subnet ?? 'the subnet'}; addresses outside the DHCP range ({resNet?.dhcpStart} – {resNet?.dhcpEnd}) avoid clashes.</span>
    {#if resErr}<div class="ui-error-box">{resErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (resNet = null)}>Cancel</button>
    <button class="ui-btn primary" form="res-form" disabled={resBusy}>{resBusy ? 'Saving…' : 'Reserve'}</button>
  {/snippet}
</Dialog>

<Dialog open={dlg} title="New network" onclose={() => (dlg = false)} width={520}>
  <form id="net-form" class="form" onsubmit={create}>
    <label class="ui-field">
      <span class="label">Name</span>
      <input class="ui-input" bind:value={spec.name} placeholder="lab" required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <div class="modes" role="radiogroup" aria-label="Network type">
      {#each ['nat', 'isolated', 'route', 'bridge'] as m (m)}
        <label class="mode-opt" class:on={spec.mode === m}>
          <input type="radio" name="mode" value={m} bind:group={spec.mode} />
          <strong>{MODE[m].label}</strong>
          <span>{MODE[m].hint}</span>
        </label>
      {/each}
    </div>
    {#if spec.mode === 'bridge'}
      <label class="ui-field">
        <span class="label">Host bridge</span>
        <input class="ui-input mono" bind:value={spec.bridge} placeholder="br0" required />
        <span class="hint">The bridge must already exist on the host (e.g. created with NetworkManager or netplan).</span>
      </label>
    {:else}
      <label class="ui-field">
        <span class="label">Subnet</span>
        <input class="ui-input mono" bind:value={spec.subnet} placeholder="192.168.100.0/24" required />
        <span class="hint">The host becomes .1; with DHCP, machines get the rest.</span>
      </label>
      <label class="ui-switch"><input type="checkbox" bind:checked={spec.dhcp} /> Hand out addresses with DHCP</label>
    {/if}
    {#if formErr}<div class="ui-error-box">{formErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (dlg = false)}>Cancel</button>
    <button class="ui-btn primary" form="net-form" disabled={busy}>{busy ? 'Creating…' : 'Create network'}</button>
  {/snippet}
</Dialog>

<style>
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .list {
    display: grid;
    gap: 10px;
  }
  .net {
    overflow: hidden;
  }
  .head {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) 80px 110px auto;
    align-items: center;
    gap: 14px;
    width: 100%;
    padding: 14px 16px;
    border: 0;
    background: transparent;
    text-align: left;
    cursor: pointer;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 36px;
    height: 36px;
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--text-3);
  }
  .icon.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .titles {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 14.5px;
  }
  .mode {
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11.5px;
    font-weight: 500;
  }
  .sub {
    color: var(--text-3);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .state,
  .users {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .state.on {
    color: var(--ok);
  }
  .chev {
    display: grid;
    color: var(--text-3);
    transition: transform var(--dur) var(--ease-out);
  }
  .open .chev {
    transform: rotate(180deg);
  }
  .body {
    padding: 4px 16px 16px 66px;
    border-top: 1px solid var(--border);
  }
  .hint {
    margin: 10px 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .spacer {
    flex: 1;
  }
  .small {
    font-size: 12.5px;
    color: var(--text-2);
  }
  .block {
    margin-top: 14px;
  }
  h3 {
    margin: 0 0 8px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .leases {
    display: grid;
  }
  .block-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .block-head h3 {
    margin: 0;
  }
  .pin {
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    margin-left: 6px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
    vertical-align: middle;
  }
  .pin:hover {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .pinned {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--accent);
  }
  .two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .lease {
    display: grid;
    grid-template-columns: 150px minmax(0, 1fr) 150px 110px;
    align-items: center;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid var(--border);
    font-size: 13px;
  }
  .lease:first-child {
    border-top: 0;
  }
  .muted {
    color: var(--text-3);
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 56px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .modes {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .mode-opt {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    cursor: pointer;
    transition: border-color var(--dur-fast);
  }
  .mode-opt input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .mode-opt.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .mode-opt:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .mode-opt strong {
    font-size: 13.5px;
  }
  .mode-opt span {
    color: var(--text-3);
    font-size: 12px;
  }
  @media (max-width: 760px) {
    .head {
      grid-template-columns: auto minmax(0, 1fr) auto;
    }
    .head .users,
    .head .state {
      display: none;
    }
    .body {
      padding-left: 16px;
    }
    .lease {
      grid-template-columns: 1fr 1fr;
    }
    .modes {
      grid-template-columns: 1fr;
    }
  }
</style>
