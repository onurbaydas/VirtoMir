<script lang="ts">
  import { t } from '../i18n.svelte'
  // Physical network (ADR-0075): SR-IOV virtual functions and BGP on this host, IP pools that
  // give machines fixed addresses and dynamic DNS for their names (the same on every member).
  import '../deck.css'
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import { api, errorText } from '../api'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import { physnet, poolUse, rangeSize, type BGPConfig, type BGPView, type DNSConfig, type IPPool, type SRIOVDevice } from '../physnet'

  let { tab = '' }: { tab?: string } = $props()
  const TABS = [
    { key: 'pools', label: t('IP pools') },
    { key: 'dns', label: t('Dynamic DNS') },
    { key: 'bgp', label: t('BGP / EVPN') },
    { key: 'sriov', label: t('SR-IOV') },
  ]
  let view = $derived(TABS.some((x) => x.key === tab) ? tab : 'pools')
  const go = (k: string) => router.navigate(`/physnet${k === 'pools' ? '' : '#' + k}`, { replace: true })
  let admin = $derived(session.can('admin'))

  let error = $state('')
  async function act(fn: () => Promise<unknown>, done: string, reload: () => Promise<unknown>): Promise<boolean> {
    try {
      await fn()
      toasts.push({ kind: 'info', title: done })
      return true
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not done'), detail: errorText(e) })
      return false
    } finally {
      await reload()
    }
  }

  // ---- IP pools ---------------------------------------------------------------------------
  let pools = $state<IPPool[] | null>(null)
  const loadPools = async () => {
    try {
      pools = await physnet.pools()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  let networks = $state<string[]>([])
  type PoolForm = Omit<IPPool, 'allocations' | 'dnsServers' | 'reserved'> & { isNew: boolean; dns: string; reservedText: string }
  let poolForm = $state<PoolForm | null>(null)
  let poolProblem = $derived(
    !poolForm ? '' : poolForm.isNew && !/^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$/.test(poolForm.id) ? t('The ID is 2 to 40 lower-case letters, digits and dashes.')
      : !poolForm.name.trim() ? t('Give the pool a name.')
      : !/^[0-9a-fA-F.:]+\/\d{1,3}$/.test(poolForm.cidr) ? t('The network is a CIDR like 10.20.0.0/24.')
      : !poolForm.rangeStart || !poolForm.rangeEnd ? t('Give the first and the last address of the range.') : '',
  )
  function newPool() {
    poolForm = { isNew: true, id: '', name: '', description: '', network: networks[0] ?? '', cidr: '', gateway: '', rangeStart: '', rangeEnd: '', domain: '', dns: '', reservedText: '' }
  }
  function editPool(p: IPPool) {
    poolForm = { ...p, isNew: false, dns: (p.dnsServers ?? []).join(', '), reservedText: (p.reserved ?? []).join(', ') }
  }
  async function savePool() {
    if (!poolForm || poolProblem) return
    const { isNew, dns, reservedText, ...p } = poolForm
    const split = (s: string) => s.split(/[\s,]+/).filter(Boolean)
    const body = { ...p, dnsServers: split(dns), reserved: split(reservedText), gateway: p.gateway || undefined, network: p.network || undefined }
    if (await act(() => (isNew ? physnet.createPool(body) : physnet.updatePool(p.id, body)), t('Pool saved'), loadPools)) poolForm = null
  }
  let reserveFor = $state<{ pool: IPPool; name: string; ip: string } | null>(null)

  // ---- DNS --------------------------------------------------------------------------------
  let dns = $state<(DNSConfig & { reverse: string }) | null>(null)
  let hasSecret = $state(false)
  const loadDNS = async () => {
    try {
      const d = await physnet.dns()
      dns = { ...d.config, secret: '', reverse: (d.config.reverseZones ?? []).join(', ') }
      hasSecret = d.hasSecret
    } catch (e) {
      error = errorText(e)
    }
  }
  let dnsBusy = $state(false)
  async function saveDNS() {
    if (!dns) return
    const { reverse, ...c } = dns
    await act(() => physnet.setDNS({ ...c, reverseZones: reverse.split(/[\s,]+/).filter(Boolean), ttl: Number(c.ttl) || undefined }), t('DNS settings saved'), loadDNS)
  }
  async function testDNS() {
    dnsBusy = true
    try {
      const r = await physnet.testDNS()
      toasts.push({ kind: r.ok ? 'info' : 'error', title: r.ok ? t('The DNS server took the update') : t('The DNS server refused the update'), detail: r.message })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not tested'), detail: errorText(e) })
    } finally {
      dnsBusy = false
    }
  }

  // ---- BGP --------------------------------------------------------------------------------
  let bgp = $state<BGPView | null>(null)
  let bgpForm = $state<(BGPConfig & { announceText: string }) | null>(null)
  const loadBGP = async () => {
    try {
      bgp = await physnet.bgp()
    } catch (e) {
      error = errorText(e)
    }
  }
  function editBGP() {
    if (!bgp) return
    const c = bgp.config
    bgpForm = { ...c, localAs: c.localAs || 65000, neighbors: c.neighbors.map((n) => ({ ...n })), announceText: c.announce.join(', ') }
  }
  async function saveBGP() {
    if (!bgpForm) return
    const { announceText, ...c } = bgpForm
    const body = { ...c, localAs: Number(c.localAs), neighbors: c.neighbors.map((n) => ({ ...n, remoteAs: Number(n.remoteAs) })), announce: announceText.split(/[\s,]+/).filter(Boolean) }
    if (await act(() => physnet.setBGP(body), t('BGP settings applied'), loadBGP)) bgpForm = null
  }
  const stateClass = (s: string) => (s === 'Established' ? '' : s === 'Active' || s === 'Connect' || s === 'OpenSent' ? 'warn' : 'drop')

  // ---- SR-IOV -----------------------------------------------------------------------------
  let devs = $state<SRIOVDevice[] | null>(null)
  const loadSRIOV = async () => {
    try {
      devs = await physnet.sriov()
    } catch (e) {
      error = errorText(e)
    }
  }
  let vfForm = $state<{ dev: SRIOVDevice; vfs: number; persistent: boolean } | null>(null)
  let netForm = $state<{ dev: SRIOVDevice; name: string; vlan: number } | null>(null)

  $effect(() => {
    void router.host
    error = ''
    if (view === 'pools') {
      void loadPools()
      void api.networks().then((n) => (networks = n.map((x) => x.name))).catch(() => {})
    } else if (view === 'dns') void loadDNS()
    else if (view === 'bgp') void loadBGP()
    else void loadSRIOV()
  })
</script>

<svelte:head><title>{t('Physical network · VirtoMir')}</title></svelte:head>

<div class="deck">
  <header class="page-head">
    <div>
      <h1>{t('Physical network')}</h1>
      <p class="lead">{t('Where machines meet the real network: fixed addresses from IP pools with their names in DNS, BGP routes to your routers, and SR-IOV network cards shared out to machines.')}</p>
    </div>
  </header>

  <div class="tabs" role="tablist">
    {#each TABS as x (x.key)}
      <button role="tab" aria-selected={view === x.key} class:on={view === x.key} onclick={() => go(x.key)}>{x.label}</button>
    {/each}
  </div>

  {#if error}<div class="ui-error-box">{error}</div>{/if}

  {#if view === 'pools'}
    <div class="bar">
      <p class="muted">{t('An IP pool is a range of a network that VirtoMir hands out as fixed addresses: a machine made from a cloud image gets one through cloud-init, and its name goes into DNS. Every member sees the same pools, so addresses never collide.')}</p>
      {#if admin}<button class="ui-btn primary" onclick={newPool}><Plus size={14} /> {t('New IP pool')}</button>{/if}
    </div>
    {#if pools === null}
      <p class="muted">{t('Loading…')}</p>
    {:else}
      {#each pools as p (p.id)}
        <section class="ui-panel card">
          <header class="card-head">
            <div>
              <h3>{p.name} <span class="mono small muted">{p.cidr}</span> {#if p.network}<span class="tag">{p.network}</span>{/if}</h3>
              <p class="muted small">
                {t('{start} – {end}, {n} addresses', { start: p.rangeStart, end: p.rangeEnd, n: rangeSize(p.rangeStart, p.rangeEnd) })}
                {#if p.gateway}· {t('gateway {gw}', { gw: p.gateway })}{/if}
                {#if p.dnsServers?.length}· {t('DNS {list}', { list: p.dnsServers.join(', ') })}{/if}
                {#if p.domain}· {p.domain}{/if}
              </p>
              <div class="meter" class:warn={poolUse(p) > 80} class:danger={poolUse(p) > 95}><span style:width="{poolUse(p)}%"></span></div>
            </div>
            {#if admin}
              <div class="right">
                <button class="ui-btn sm" onclick={() => (reserveFor = { pool: p, name: '', ip: '' })}><Plus size={13} /> {t('Reserve')}</button>
                <button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => editPool(p)}><Pencil size={13} /></button>
                <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this pool')} run={() => act(() => physnet.deletePool(p.id), t('Pool deleted'), loadPools)} />
              </div>
            {/if}
          </header>
          <table class="t">
            <thead><tr><th>{t('Address')}</th><th>{t('Holder')}</th><th>{t('DNS name')}</th><th>{t('MAC')}</th><th></th></tr></thead>
            <tbody>
              {#each p.allocations as a (a.ip)}
                <tr>
                  <td class="mono">{a.ip}</td>
                  <td>{a.vmName ?? '—'}{#if a.host}<span class="muted small"> · {a.host}</span>{/if}{#if !a.vmUuid}<span class="tag">{t('reserved')}</span>{/if}</td>
                  <td class="mono small">{a.hostname ? a.hostname : '—'}</td>
                  <td class="mono small">{a.mac ?? ''}</td>
                  <td class="right-cell">{#if admin}<ConfirmButton label={t('Release')} icon={Trash} confirm={a.vmUuid ? t('The machine keeps it until it is reconfigured') : t('Release this address')} run={() => act(() => physnet.release(p.id, a.ip), t('Address released'), loadPools)} />{/if}</td>
                </tr>
              {:else}
                <tr><td colspan="5" class="muted">{t('No address is handed out yet.')}</td></tr>
              {/each}
            </tbody>
          </table>
        </section>
      {:else}
        <section class="ui-panel card"><p class="muted">{t('No IP pools yet.')}</p></section>
      {/each}
    {/if}
  {:else if view === 'dns'}
    <p class="muted" style="margin-bottom: 12px">{t('Machines with an address from an IP pool get an A or AAAA record in the zone and a PTR record in a reverse zone (RFC 2136 updates, signed with a TSIG key). Works with BIND, Knot, PowerDNS and Windows DNS.')}</p>
    {#if dns}
      <section class="ui-panel card">
        <form class="form" onsubmit={(e) => (e.preventDefault(), saveDNS())} inert={!admin}>
          <label class="ui-switch"><input type="checkbox" bind:checked={dns.enabled} /> {t('Register machines in DNS')}</label>
          <div class="row">
            <label class="ui-field"><span class="label">{t('Primary DNS server')}</span><input class="ui-input mono" bind:value={dns.server} placeholder="10.0.0.53" /></label>
            <label class="ui-field"><span class="label">{t('Zone')}</span><input class="ui-input mono" bind:value={dns.zone} placeholder="prod.example" /></label>
          </div>
          <label class="ui-field"><span class="label">{t('Reverse zones')}</span><input class="ui-input mono" bind:value={dns.reverse} placeholder="20.10.in-addr.arpa" /><span class="hint">{t('PTR records go into the reverse zone that holds the address; without one, none are written.')}</span></label>
          <div class="row">
            <label class="ui-field"><span class="label">{t('TSIG key name')}</span><input class="ui-input mono" bind:value={dns.keyName} placeholder="virtomir" /></label>
            <label class="ui-field"><span class="label">{t('Algorithm')}</span>
              <select class="ui-input" bind:value={dns.algorithm}><option value="hmac-sha256">hmac-sha256</option><option value="hmac-sha512">hmac-sha512</option><option value="hmac-md5">hmac-md5</option></select>
            </label>
          </div>
          <label class="ui-field"><span class="label">{t('Key secret')}</span><input class="ui-input mono" type="password" bind:value={dns.secret} placeholder={hasSecret ? t('stored; leave empty to keep it') : 'base64'} autocomplete="off" /></label>
          <label class="ui-field"><span class="label">{t('TTL (seconds)')}</span><input class="ui-input" type="number" min="30" max="86400" bind:value={dns.ttl} placeholder="300" /></label>
          {#if admin}
            <div class="right">
              <button class="ui-btn primary">{t('Save')}</button>
              <button type="button" class="ui-btn" class:busy={dnsBusy} disabled={dnsBusy || !dns.enabled} onclick={testDNS}>{t('Test with a throw-away name')}</button>
            </div>
          {/if}
        </form>
      </section>
    {/if}
  {:else if view === 'bgp'}
    {#if bgp}
      <div class="grid">
        <section class="ui-panel card">
          <h3>{t('FRR on this host')}</h3>
          {#if !bgp.frr.installed}
            <p class="muted">{t('FRR is not installed. Install it (apt install frr, or dnf install frr) and set bgpd=yes in /etc/frr/daemons.')}</p>
          {:else}
            <dl>
              <dt>{t('Version')}</dt><dd class="mono">{bgp.frr.version ?? '?'}</dd>
              <dt>bgpd</dt><dd><span class="pill" class:drop={!bgp.frr.bgpd}>{bgp.frr.bgpd ? t('running') : t('off: set bgpd=yes in /etc/frr/daemons')}</span></dd>
              <dt>{t('This host')}</dt><dd>{bgp.config.enabled ? `AS ${bgp.config.localAs} · ${bgp.config.routerId}` : t('BGP off')}{bgp.config.evpn ? ' · EVPN' : ''}</dd>
              <dt>{t('Announces')}</dt><dd class="mono small">{bgp.config.announce.join(', ') || '—'}</dd>
            </dl>
          {/if}
          {#if admin}<button class="ui-btn" onclick={editBGP}><Pencil size={14} /> {t('Settings')}</button>{/if}
        </section>
      </div>
      {#if bgp.error}<div class="ui-error-box">{bgp.error}</div>{/if}
      <table class="t">
        <thead><tr><th>{t('Neighbor')}</th><th>AS</th><th>{t('State')}</th><th>{t('Up for')}</th><th>{t('Routes in / out')}</th><th>{t('Families')}</th></tr></thead>
        <tbody>
          {#each bgp.sessions as s (s.address)}
            <tr>
              <td class="mono">{s.address}{#if s.description} <span class="muted small">{s.description}</span>{/if}</td>
              <td class="mono">{s.remoteAs}</td>
              <td><span class="pill {stateClass(s.state)}">{s.state}</span></td>
              <td class="mono small">{s.uptime ?? ''}</td>
              <td class="mono">{s.prefixesReceived} / {s.prefixesSent}</td>
              <td class="small">{s.families.join(', ')}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">{t('No BGP sessions.')}</td></tr>
          {/each}
        </tbody>
      </table>
      <p class="hint">{t('experimental, not tested on hardware')} · {t('VirtoMir writes only its own router bgp section; the rest of FRR’s configuration stays yours. With EVPN, the host’s VXLAN overlays are announced to the fabric.')}</p>
    {/if}
  {:else}
    <p class="muted" style="margin-bottom: 12px"><span class="tag first">{t('experimental, not tested on hardware')}</span> {t('An SR-IOV network card splits into virtual functions, each its own PCI device. Make a network of the card: every machine on it gets a free virtual function, passed through, at nearly the speed of the card itself.')}</p>
    {#if devs === null}
      <p class="muted">{t('Loading…')}</p>
    {:else}
      {#each devs as d (d.interface)}
        <section class="ui-panel card">
          <header class="card-head">
            <div>
              <h3>{d.interface} <span class="mono small muted">{d.pci}</span> <span class="tag">{d.driver}</span></h3>
              <p class="muted small">{t('{n} of {max} virtual functions', { n: d.currentVfs, max: d.totalVfs })}{d.persistent ? ' · ' + t('set again at boot') : ''}</p>
            </div>
            {#if admin}
              <div class="right">
                <button class="ui-btn sm" onclick={() => (vfForm = { dev: d, vfs: d.currentVfs || Math.min(8, d.totalVfs), persistent: true })}>{t('Virtual functions…')}</button>
                {#if d.currentVfs > 0}<button class="ui-btn sm" onclick={() => (netForm = { dev: d, name: `sriov-${d.interface}`, vlan: 0 })}><Plus size={13} /> {t('Network')}</button>{/if}
              </div>
            {/if}
          </header>
          {#if d.vfs.length}
            <table class="t">
              <thead><tr><th>VF</th><th>PCI</th><th>{t('On the host as')}</th><th>MAC</th></tr></thead>
              <tbody>
                {#each d.vfs as vf (vf.index)}
                  <tr><td class="mono">{vf.index}</td><td class="mono">{vf.pci}</td><td class="mono">{vf.netdev ?? t('— (given to a machine or not bound)')}</td><td class="mono small">{vf.mac ?? ''}</td></tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </section>
      {:else}
        <section class="ui-panel card"><p class="muted">{t('This host has no SR-IOV capable network card (or IOMMU/SR-IOV is off in the firmware).')}</p></section>
      {/each}
    {/if}
  {/if}
</div>

<Dialog open={!!poolForm} title={poolForm?.isNew ? t('New IP pool') : t('Edit {name}', { name: poolForm?.name ?? '' })} width={560} onclose={() => (poolForm = null)}>
  {#if poolForm}
    <form id="ippool" class="deck-form" onsubmit={(e) => (e.preventDefault(), savePool())}>
      <div class="row">
        {#if poolForm.isNew}<label class="ui-field"><span class="label">{t('ID')}</span><input class="ui-input mono" bind:value={poolForm.id} placeholder="prod-v20" required /></label>{/if}
        <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={poolForm.name} placeholder={t('Production VLAN 20')} required /></label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Network')}</span><input class="ui-input mono" bind:value={poolForm.cidr} placeholder="10.20.0.0/24" required /></label>
        <label class="ui-field"><span class="label">{t('Gateway')}</span><input class="ui-input mono" bind:value={poolForm.gateway} placeholder="10.20.0.1" /></label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('First address')}</span><input class="ui-input mono" bind:value={poolForm.rangeStart} placeholder="10.20.0.100" required /></label>
        <label class="ui-field"><span class="label">{t('Last address')}</span><input class="ui-input mono" bind:value={poolForm.rangeEnd} placeholder="10.20.0.199" required /></label>
      </div>
      <label class="ui-field">
        <span class="label">{t('Machines join')}</span>
        <select class="ui-input" bind:value={poolForm.network}><option value="">{t('— any network —')}</option>{#each networks as n (n)}<option value={n}>{n}</option>{/each}</select>
        <span class="hint">{t('The libvirt network that reaches this subnet (usually a bridge to its VLAN); a machine using the pool must be on it.')}</span>
      </label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('DNS servers')}</span><input class="ui-input mono" bind:value={poolForm.dns} placeholder="10.20.0.53" /></label>
        <label class="ui-field"><span class="label">{t('Search domain')}</span><input class="ui-input mono" bind:value={poolForm.domain} placeholder="prod.example" /></label>
      </div>
      <label class="ui-field"><span class="label">{t('Never hand out')}</span><input class="ui-input mono" bind:value={poolForm.reservedText} placeholder="10.20.0.150, 10.20.0.151" /></label>
      {#if poolProblem}<p class="problem">{poolProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (poolForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="ippool" disabled={!!poolProblem}>{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!reserveFor} title={t('Reserve an address of {pool}', { pool: reserveFor?.pool.name ?? '' })} width={440} onclose={() => (reserveFor = null)}>
  {#if reserveFor}
    <form id="reserve" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => physnet.reserve(reserveFor!.pool.id, reserveFor!.name, reserveFor!.ip || undefined), t('Address reserved'), loadPools).then((ok) => ok && (reserveFor = null)))}>
      <label class="ui-field"><span class="label">{t('For')}</span><input class="ui-input" bind:value={reserveFor.name} placeholder={t('printer, load balancer VIP, …')} required /></label>
      <label class="ui-field"><span class="label">{t('Address')}</span><input class="ui-input mono" bind:value={reserveFor.ip} placeholder={t('the first free one')} /></label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (reserveFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="reserve">{t('Reserve')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!bgpForm} title={t('BGP on this host')} width={640} onclose={() => (bgpForm = null)}>
  {#if bgpForm}
    <form id="bgp" class="deck-form" onsubmit={(e) => (e.preventDefault(), saveBGP())}>
      <label class="ui-switch"><input type="checkbox" bind:checked={bgpForm.enabled} /> {t('Speak BGP')}</label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Local AS')}</span><input class="ui-input mono" type="number" min="1" max="4294967295" bind:value={bgpForm.localAs} /></label>
        <label class="ui-field"><span class="label">{t('Router ID')}</span><input class="ui-input mono" bind:value={bgpForm.routerId} placeholder="10.0.0.11" /></label>
      </div>
      <span class="label">{t('Neighbors')}</span>
      {#each bgpForm.neighbors as n, i (i)}
        <div class="row">
          <input class="ui-input mono" bind:value={n.address} placeholder="10.0.0.1" aria-label={t('Address')} />
          <input class="ui-input mono" type="number" bind:value={n.remoteAs} placeholder="65000" aria-label={t('Remote AS')} />
          <input class="ui-input" bind:value={n.description} placeholder={t('leaf1')} aria-label={t('Description')} />
          <input class="ui-input mono" type="password" bind:value={n.password} placeholder={t('password')} aria-label={t('Password')} autocomplete="off" />
          <label class="ui-switch"><input type="checkbox" bind:checked={n.bfd} /> BFD</label>
          <button type="button" class="ui-btn sm ghost" aria-label={t('Remove')} onclick={() => bgpForm!.neighbors.splice(i, 1)}><Trash size={13} /></button>
        </div>
      {/each}
      <button type="button" class="ui-btn sm" onclick={() => bgpForm!.neighbors.push({ address: '', remoteAs: 65000, description: '', password: '', bfd: false })}><Plus size={13} /> {t('Neighbor')}</button>
      <label class="ui-field"><span class="label">{t('Announce these networks')}</span><input class="ui-input mono" bind:value={bgpForm.announceText} placeholder="10.20.0.0/24, fd00:20::/64" /></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={bgpForm.evpn} /> {t('EVPN: announce this host’s VXLAN overlays (advertise-all-vni)')}</label>
      <p class="hint">{t('Saving replaces VirtoMir’s router bgp section in the running FRR and saves it; sessions reconnect.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (bgpForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="bgp">{t('Apply')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!vfForm} title={t('Virtual functions of {nic}', { nic: vfForm?.dev.interface ?? '' })} width={460} onclose={() => (vfForm = null)}>
  {#if vfForm}
    <form id="vfs" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => physnet.setVFs(vfForm!.dev.interface, Number(vfForm!.vfs), vfForm!.persistent), t('Virtual functions set'), loadSRIOV).then((ok) => ok && (vfForm = null)))}>
      <label class="ui-field"><span class="label">{t('How many')}</span><input class="ui-input" type="number" min="0" max={vfForm.dev.totalVfs} bind:value={vfForm.vfs} /><span class="hint">{t('The card supports up to {n}. Changing the number removes every virtual function first: machines must not be using them.', { n: vfForm.dev.totalVfs })}</span></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={vfForm.persistent} /> {t('Set them again at boot (udev rule)')}</label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (vfForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="vfs">{t('Apply')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!netForm} title={t('Network of {nic}', { nic: netForm?.dev.interface ?? '' })} width={460} onclose={() => (netForm = null)}>
  {#if netForm}
    <form id="vfnet" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => api.createNetwork({ name: netForm!.name, mode: 'hostdev', pf: netForm!.dev.interface, dhcp: false, vlan: Number(netForm!.vlan) || undefined }), t('Network created'), loadSRIOV).then((ok) => ok && (netForm = null)))}>
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input mono" bind:value={netForm.name} required /></label>
      <label class="ui-field"><span class="label">{t('VLAN')} <span class="muted">{t('(optional)')}</span></span><input class="ui-input" type="number" min="0" max="4094" bind:value={netForm.vlan} /></label>
      <p class="hint">{t('Machines on this network get a free virtual function each; the switch port must carry the VLAN.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (netForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="vfnet">{t('Create')}</button>
  {/snippet}
</Dialog>
