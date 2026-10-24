<script lang="ts">
  import { t } from '../i18n.svelte'
  import ObjectXmlDialog from '../components/ObjectXmlDialog.svelte'
  import FileCode from '@lucide/svelte/icons/file-code'
  let xmlNet = $state<string | null>(null)
  import { session } from '../session.svelte'
  import Network from '@lucide/svelte/icons/network'
  import Plus from '@lucide/svelte/icons/plus'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import Trash from '@lucide/svelte/icons/trash'
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import Pin from '@lucide/svelte/icons/pin'
  import { api, errorText, type Lease, type NetworkSpec, type VNetwork } from '../api'
  import { cluster } from '../cluster.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Radio from '@lucide/svelte/icons/radio'
  import SdnTopologyDiagram from '../components/SdnTopologyDiagram.svelte'
  import PcapSnifferModal from '../components/PcapSnifferModal.svelte'

  let nets = $state<VNetwork[] | null>(null)
  let error = $state('')
  let open = $state<string | null>(null)
  let leases = $state<Record<string, Lease[] | string>>({})
  let snifferOpen = $state(false)

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
      toasts.push({ kind: 'error', title: t('Could not {action} {name}', { action: action, name: n.name }), detail: errorText(e) })
    }
  }
  async function autostart(n: VNetwork, on: boolean) {
    try {
      await api.networkAutostart(n.name, on)
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Autostart not changed'), detail: errorText(e) })
    }
  }

  const MODE: Record<string, { label: string; hint: string }> = {
    nat: { label: t('NAT'), hint: t('Machines reach the outside world through the host; they are not reachable from the LAN.') },
    route: { label: t('Routed'), hint: t('Machines get their own subnet, routed by the host (needs a route on your router).') },
    open: { label: t('Open'), hint: t('Routed without any firewall rules from libvirt.') },
    isolated: { label: t('Isolated'), hint: t('Machines only talk to each other and to the host.') },
    bridge: { label: t('Bridged'), hint: t('Machines join the physical LAN through a host bridge, like real computers.') },
    vxlan: { label: t('VXLAN overlay'), hint: t('One layer-2 network spanning several hosts, tunnelled over their existing network (UDP 4789).') },
    hostdev: { label: t('SR-IOV'), hint: t('Each machine gets its own virtual function of an SR-IOV network card, passed through (Physical network page).') },
  }

  const modeLabel = (m: string) => MODE[m]?.label ?? m
  const netLine = (n: VNetwork) =>
    [
      n.subnet,
      n.subnet6,
      n.bridge && `bridge ${n.bridge}`,
      n.mode === 'vxlan' && `VNI ${n.vni}`,
      n.mode === 'vxlan' && (n.peers?.length ? t('peers {list}', { list: n.peers.join(', ') }) : t('no peers')),
      n.dhcpStart && `DHCP ${n.dhcpStart} – ${n.dhcpEnd}`,
      n.subnet6 && (n.dhcp6Start ? 'DHCPv6' : 'SLAAC'),
    ]
      .filter(Boolean)
      .join(' · ')

  // ---- New network ------------------------------------------------------------------
  let dlg = $state(false)
  let spec = $state<NetworkSpec>({ name: '', mode: 'nat', subnet: '192.168.100.0/24', dhcp: true, bridge: '' })
  let busy = $state(false)
  let formErr = $state('')
  let withV4 = $state(true)
  let withV6 = $state(false)
  let peersText = $state('')
  // A random unique local /64 (RFC 4193): fdXX:XXXX:XXXX:1::/64.
  function randomULA() {
    const b = crypto.getRandomValues(new Uint8Array(5))
    const h = (i: number) => b[i].toString(16).padStart(2, '0')
    return `fd${h(0)}:${h(1)}${h(2)}:${h(3)}${h(4)}:1::/64`.replace(/:0+([0-9a-f])/g, ':$1')
  }
  function openNew() {
    // Suggest a free 192.168.X.0/24.
    const used = new Set((nets ?? []).map((n) => n.subnet?.split('.').slice(0, 3).join('.')))
    let x = 100
    while (used.has(`192.168.${x}`) && x < 250) x++
    spec = { name: '', mode: 'nat', subnet: `192.168.${x}.0/24`, dhcp: true, bridge: '', subnet6: randomULA(), dhcp6: false, vni: 100 }
    peersText = ''
    withV4 = true
    withV6 = false
    formErr = ''
    dlg = true
  }
  let everywhere = $state(true)
  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    formErr = ''
    try {
      const body: NetworkSpec =
        spec.mode === 'bridge'
          ? { name: spec.name, mode: 'bridge', bridge: spec.bridge, dhcp: false, ovs: spec.ovs, vlan: spec.ovs && spec.vlan ? Number(spec.vlan) : undefined }
          : spec.mode === 'vxlan'
            ? {
                name: spec.name,
                mode: 'vxlan',
                vni: Number(spec.vni),
                peers: peersText.split(/[\s,]+/).filter(Boolean),
                subnet: withV4 ? spec.subnet : undefined,
                dhcp: withV4 && spec.dhcp,
                subnet6: withV6 ? spec.subnet6?.trim() : undefined,
                dhcp6: withV6 && spec.dhcp6,
              }
            : {
                ...spec,
                bridge: undefined,
                ovs: undefined,
                vlan: undefined,
                subnet: withV4 ? spec.subnet : undefined,
                dhcp: withV4 && spec.dhcp,
                subnet6: withV6 ? spec.subnet6?.trim() : undefined,
                dhcp6: withV6 && spec.dhcp6,
              }

      if (cluster.clustered && everywhere) {
        const res = await api.createClusterNetwork(body)
        const failed = res.filter((r) => !r.ok)
        if (failed.length === res.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
        toasts.push({
          kind: failed.length ? 'warn' : 'info',
          title: failed.length ? t('{name} created on {v} of {length} hosts', { name: spec.name, v: res.length - failed.length, length: res.length }) : t('{name} created on every host', { name: spec.name }),
          detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') || undefined,
        })
      } else {
        await api.createNetwork(body)
        toasts.push({ kind: 'info', title: t('Network {name} created', { name: spec.name }) })
      }
      dlg = false
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
      toasts.push({ kind: 'info', title: `${resIP} reserved`, detail: t('The machine gets it the next time it renews its lease (or reboots).') })
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

<ObjectXmlDialog
  open={!!xmlNet}
  title={t('Network {v}', { v: xmlNet ?? '' })}
  kind="network"
  load={() => api.networkXML(xmlNet!)}
  save={(x) => api.saveNetworkXML(xmlNet!, x)}
  onclose={(saved) => {
    xmlNet = null
    if (saved) void load()
  }}
/>

<svelte:head><title>{t('Networks · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Networks')}</h1>
    <p class="lead">{t('Virtual networks connect machines to each other, to the host and to the outside world.')}</p>
  </div>
  <div class="head-right">
    <button class="ui-btn" onclick={() => (snifferOpen = true)}><Radio size={14} /> {t('Packet capture')}</button>
    <button class="ui-btn primary" disabled={!session.can('admin')} title={session.why('admin')} onclick={openNew}><Plus size={15} /> {t('New network')}</button>
  </div>
</header>

<SdnTopologyDiagram />

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if nets === null}
  <p class="muted">{t('Loading…')}</p>
{:else if nets.length === 0}
  <div class="empty ui-panel">
    <Network size={28} strokeWidth={1.5} />
    <strong>{t('No virtual networks')}</strong>
    <span>{t('Create a NAT network so machines can reach the internet.')}</span>
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
                {t('Start with the host')}
              </label>
              <span class="spacer"></span>
              <button class="ui-btn sm ghost" onclick={() => (xmlNet = n.name)}><FileCode size={14} /> {t('XML')}</button>
              {#if n.active}
                <ConfirmButton
                  label={t('Stop')}
                  confirm={n.usedBy.length ? 'Machines lose network — stop?' : t('Stop network?')}
                  icon={Square}
                  run={() => act(n, 'stop')}
                />
              {:else}
                <button class="ui-btn sm" onclick={() => act(n, 'start')}><Play size={14} /> {t('Start')}</button>
              {/if}
              <ConfirmButton
                label={t('Delete')}
                confirm={t('Delete network?')}
                icon={Trash}
                disabled={n.usedBy.length > 0}
                title={n.usedBy.length ? 'Used by ' + n.usedBy.join(', ') : t('Delete this network')}
                failure={t('Could not delete {name}', { name: n.name })}
                run={async () => {
                  await api.deleteNetwork(n.name)
                  toasts.push({ kind: 'info', title: t('Network {name} deleted', { name: n.name }) })
                  open = null
                  await load()
                }}
              />
            </div>

            {#if n.usedBy.length}
              <div class="block">
                <h3>{t('Connected machines')}</h3>
                <div class="chips">{#each n.usedBy as m (m)}<span class="chip">{m}</span>{/each}</div>
              </div>
            {/if}

            {#if n.dhcpStart}
              <div class="block">
                <div class="block-head">
                  <h3>{t('Reserved addresses')}</h3>
                  <button class="ui-btn sm ghost" inert={!session.can('admin')} onclick={() => openReserve(n)}><Pin size={14} /> {t('Reserve an address')}</button>
                </div>
                {#if !n.dhcpHosts?.length}
                  <p class="muted">{t('None. Reserve one so a machine always gets the same IP.')}</p>
                {:else}
                  <div class="leases" role="table">
                    {#each n.dhcpHosts as h (h.mac)}
                      <div class="lease" role="row">
                        <span class="mono" role="cell">{h.ip}</span>
                        <span role="cell">{h.name ?? '—'}</span>
                        <span class="mono muted" role="cell">{h.mac}</span>
                        <span role="cell" inert={!session.can('admin')}>
                          <ConfirmButton
                            label={t('Remove')}
                            confirm={t('Remove?')}
                            failure={t('Could not remove the reservation')}
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
                <h3>{t('DHCP leases')}</h3>
                {#if !n.active}
                  <p class="muted">{t('Start the network to see leases.')}</p>
                {:else if typeof leases[n.name] === 'string'}
                  <div class="ui-error-box">{leases[n.name]}</div>
                {:else if !leases[n.name]}
                  <p class="muted">{t('Loading…')}</p>
                {:else if (leases[n.name] as Lease[]).length === 0}
                  <p class="muted">{t('No leases handed out right now.')}</p>
                {:else}
                  <div class="leases" role="table">
                    {#each leases[n.name] as Lease[] as l (l.mac + l.ip)}
                      <div class="lease" role="row">
                        <span class="mono" role="cell">{l.ip}</span>
                        <span role="cell">{l.machine ?? l.hostname ?? '—'}</span>
                        <span class="mono muted" role="cell">{l.mac}</span>
                        <span class="muted" role="cell" title={t('Lease expires in')}>
                          {#if reserved(n, l.mac)}<span class="pinned"><Pin size={12} /> {t('fixed')}</span>{:else}
                            {expires(l.expires)}
                            {#if session.can('admin')}<button class="pin" title={t('Always give this machine this address')} onclick={() => openReserve(n, l)}><Pin size={13} /></button>{/if}
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


<PcapSnifferModal open={snifferOpen} onclose={() => (snifferOpen = false)} />

<Dialog open={resNet !== null} title={t('Reserve an address on {v}', { v: resNet?.name ?? '' })} onclose={() => (resNet = null)}>
  <form id="res-form" class="form" onsubmit={reserve}>
    <label class="ui-field">
      <span class="label">{t('MAC address of the machine\'s adapter')}</span>
      <input class="ui-input mono" bind:value={resMAC} placeholder="52:54:00:12:34:56" required />
    </label>
    <div class="two-col">
      <label class="ui-field">
        <span class="label">{t('IP address')}</span>
        <input class="ui-input mono" bind:value={resIP} placeholder={resNet?.subnet?.replace(/\.\d+\/\d+$/, '.50')} required />
      </label>
      <label class="ui-field">
        <span class="label">{t('Host name (optional)')}</span>
        <input class="ui-input" bind:value={resName} />
      </label>
    </div>
    <span class="hint">{t('Must be inside {v}; addresses outside the DHCP range ({dhcpStart} – {dhcpEnd}) avoid clashes.', { v: resNet?.subnet ?? 'the subnet', dhcpStart: resNet?.dhcpStart, dhcpEnd: resNet?.dhcpEnd })}</span>
    {#if resErr}<div class="ui-error-box">{resErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (resNet = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="res-form" disabled={resBusy}>{resBusy ? 'Saving…' : 'Reserve'}</button>
  {/snippet}
</Dialog>

<Dialog open={dlg} title={t('New network')} onclose={() => (dlg = false)} width={520}>
  <form id="net-form" class="form" onsubmit={create}>
    <label class="ui-field">
      <span class="label">{t('Name')}</span>
      <input class="ui-input" bind:value={spec.name} placeholder={t('lab')} required pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
    </label>
    <div class="modes" role="radiogroup" aria-label={t('Network type')}>
      {#each ['nat', 'isolated', 'route', 'bridge', 'vxlan'] as m (m)}
        <label class="mode-opt" class:on={spec.mode === m}>
          <input type="radio" name="mode" value={m} bind:group={spec.mode} />
          <strong>{MODE[m].label}</strong>
          <span>{MODE[m].hint}</span>
        </label>
      {/each}
    </div>
    {#if spec.mode === 'bridge'}
      <label class="ui-field">
        <span class="label">{t('Host bridge')}</span>
        <input class="ui-input mono" bind:value={spec.bridge} placeholder="br0" required />
        <span class="hint">{t('The bridge must already exist on the host (e.g. created with NetworkManager, netplan or ovs-vsctl).')}</span>
      </label>
      <label class="ui-switch"><input type="checkbox" bind:checked={spec.ovs} /> {t('It is an Open vSwitch bridge')}</label>
      {#if spec.ovs}
        <label class="ui-field">
          <span class="label">{t('VLAN')} <span class="opt">{t('(optional)')}</span></span>
          <input class="ui-input mono" type="number" min="1" max="4094" bind:value={spec.vlan} placeholder={t('untagged')} />
          <span class="hint">{t('Every machine on this network is put into that VLAN. Adapters can also get their own tag.')}</span>
        </label>
      {:else}
        <span class="hint">{t('VLANs on a plain Linux bridge: create a bridge on the VLAN interface (e.g. br0.10) and use that.')}</span>
      {/if}
    {:else if spec.mode === 'vxlan'}
      <label class="ui-field">
        <span class="label">{t('VXLAN network identifier (VNI)')}</span>
        <input class="ui-input mono" type="number" min="1" max="16777215" bind:value={spec.vni} placeholder="100" required />
        <span class="hint">{t('The same number on every host of this overlay. Its bridge is vxbr{vni}, with an MTU of 1450 to leave room for the tunnel header.', { vni: spec.vni || 'N' })}</span>
      </label>
      {#if cluster.clustered && everywhere}
        <span class="hint">{t('Every cluster member joins: each gets the others as peers, and this host ({host}) is the gateway if you give a subnet.', { host: cluster.current?.name ?? '' })}</span>
      {:else}
        <label class="ui-field">
          <span class="label">{t('Peers')}</span>
          <textarea class="ui-input mono" rows="2" bind:value={peersText} placeholder="192.168.1.11, 192.168.1.12"></textarea>
          <span class="hint">{t('The IP addresses of the other hosts on this overlay. Their firewalls must let UDP port 4789 through.')}</span>
        </label>
      {/if}
      <label class="ui-switch"><input type="checkbox" bind:checked={withV4} /> {t('This host is the gateway (NAT and DHCP for the overlay)')}</label>
      {#if withV4}
        <label class="ui-field">
          <span class="label">{t('Subnet')}</span>
          <input class="ui-input mono" bind:value={spec.subnet} placeholder="10.100.0.0/24" required />
          <span class="hint">{t('Give the subnet on one host only; on the others leave this off, so the overlay has a single gateway and DHCP server.')}</span>
        </label>
        <label class="ui-switch"><input type="checkbox" bind:checked={spec.dhcp} /> {t('Hand out addresses with DHCP')}</label>
      {/if}
    {:else}
      <label class="ui-switch"><input type="checkbox" bind:checked={withV4} /> {t('IPv4')}</label>
      {#if withV4}

        <label class="ui-field">
          <span class="label">{t('Subnet')}</span>
          <input class="ui-input mono" bind:value={spec.subnet} placeholder="192.168.100.0/24" required />
          <span class="hint">{t('The host becomes .1; with DHCP, machines get the rest.')}</span>
        </label>
        <label class="ui-switch"><input type="checkbox" bind:checked={spec.dhcp} /> {t('Hand out addresses with DHCP')}</label>
      {/if}
      <label class="ui-switch"><input type="checkbox" bind:checked={withV6} /> {t('IPv6')}</label>
      {#if withV6}
        <label class="ui-field">
          <span class="label">{t('IPv6 subnet')}</span>
          <input class="ui-input mono" bind:value={spec.subnet6} placeholder={t('fd12:3456:789a:1::/64')} required />
          <span class="hint">{t('A /64. The host becomes ::1 and announces the prefix, so machines configure themselves (SLAAC){v}. A random private (fd…) prefix is filled in; use your own global prefix for routed networks.', { v: spec.mode === 'nat' ? '; their IPv6 traffic is translated to the host’s address (NAT66)' : '' })}</span>
        </label>
        <label class="ui-switch"><input type="checkbox" bind:checked={spec.dhcp6} /> {t('Also hand out addresses with DHCPv6 (::100 – ::ffff)')}</label>
      {/if}
      {#if !withV4 && !withV6}<div class="ui-error-box">{t('Choose IPv4, IPv6 or both.')}</div>{/if}
    {/if}
    {#if cluster.clustered}
      <label class="ui-switch"><input type="checkbox" bind:checked={everywhere} /> {t('Create it on every host of the cluster (machines keep their network when they move)')}</label>
    {/if}
    {#if formErr}<div class="ui-error-box">{formErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (dlg = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="net-form" disabled={busy}>{busy ? 'Creating…' : t('Create network')}</button>
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
    border-radius: var(--radius-sm);
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
    border-radius: var(--radius-sm);
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
    border-radius: var(--radius-sm);
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
    border-radius: var(--radius-sm);
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
