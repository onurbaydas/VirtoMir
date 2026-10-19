<script lang="ts">
  import { t } from '../i18n.svelte'
  // Software-defined networking on OVN (ADR-0072): per-project VPCs with subnets and a
  // distributed router, security groups, load balancers, floating IPs and the overlay's
  // health. Experimental: not tested on a real OVN cluster yet.
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Link from '@lucide/svelte/icons/link'
  import Unlink from '@lucide/svelte/icons/unlink'
  import { errorText } from '../api'
  import { ent } from '../enterprise'
  import { live } from '../live.svelte'
  import { projects } from '../projects.svelte'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import {
    cidrContains, hostCount, isCIDR, isIPv4, ruleText, sdn,
    type ExternalGateway, type FloatingIP, type IPsecConfig, type LoadBalancer, type SDNPort, type SDNStatus,
    type SecurityGroup, type SecurityRule, type Subnet, type VPC, type VPCDetail,
  } from '../sdn'

  let { tab = '' }: { tab?: string } = $props()
  const TABS = [
    { key: 'vpcs', label: t('VPCs & subnets') },
    { key: 'security', label: t('Security groups') },
    { key: 'loadbalancers', label: t('Load balancers') },
    { key: 'floating-ips', label: t('Floating IPs') },
    { key: 'status', label: t('OVN & tunnels') },
  ]
  let view = $derived(TABS.some((x) => x.key === tab) ? tab : 'vpcs')
  const go = (k: string) => router.navigate(`/sdn${k === 'vpcs' ? '' : '#' + k}`, { replace: true })

  let op = $derived(session.can('operator'))
  let admin = $derived(session.can('admin'))

  let vpcs = $state<VPCDetail[]>([])
  let subnets = $state<Subnet[]>([])
  let ports = $state<SDNPort[]>([])
  let groups = $state<SecurityGroup[]>([])
  let lbs = $state<LoadBalancer[]>([])
  let fips = $state<FloatingIP[]>([])
  let status = $state<SDNStatus | null>(null)
  let ipsec = $state<IPsecConfig | null>(null)
  let error = $state('')
  let loaded = $state(false)
  // Machine names across the cluster: ports name machines on any member.
  let names = $state<Record<string, string>>({})

  async function load() {
    try {
      const list = await sdn.vpcs()
      ;[vpcs, subnets, ports, groups, lbs, fips] = await Promise.all([
        Promise.all(list.map((v) => sdn.vpc(v.id))),
        sdn.subnets(),
        sdn.ports(),
        sdn.securityGroups(),
        sdn.loadBalancers(),
        sdn.floatingIPs(),
      ])
      error = ''
    } catch (e) {
      error = errorText(e)
    } finally {
      loaded = true
    }
  }
  async function loadStatus() {
    try {
      ;[status, ipsec] = await Promise.all([sdn.status(), sdn.ipsec()])
    } catch (e) {
      toasts.push({ kind: 'error', title: t('OVN status not read'), detail: errorText(e) })
    }
  }
  $effect(() => {
    void load()
    if (!projects.loaded) void projects.load()
    void ent.clusterInventoryMachines().then((list) => (names = Object.fromEntries(list.map((m) => [m.uuid, m.name])))).catch(() => {})
  })
  $effect(() => {
    if (view === 'status' && !status) void loadStatus()
  })

  // Changes answer 502 when the state is saved but OVN refused it: still reload.
  async function act(fn: () => Promise<unknown>, done: string): Promise<boolean> {
    try {
      await fn()
      toasts.push({ kind: 'info', title: done })
      return true
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not done'), detail: errorText(e) })
      return false
    } finally {
      await load()
    }
  }

  const vmName = (uuid: string) => names[uuid] ?? live.snapshot?.domains.find((d) => d.uuid === uuid)?.name ?? uuid.slice(0, 8)
  const groupName = (id: string) => groups.find((g) => g.id === id)?.name ?? id
  const vpcName = (id: string) => vpcs.find((v) => v.id === id)?.name ?? id
  const portLabel = (id?: string) => {
    const p = ports.find((x) => x.id === id)
    return p ? `${vmName(p.vmUuid)} · ${p.ip ?? p.mac}` : (id ?? '')
  }

  // ---- VPCs and subnets -------------------------------------------------------------------------
  let vpcForm = $state<(Partial<VPC> & { isNew: boolean }) | null>(null)
  let vpcProblem = $derived(
    !vpcForm ? '' : vpcForm.isNew && !/^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(vpcForm.id ?? '') ? t('The ID is 3 to 40 lower-case letters, digits and dashes.')
      : !isCIDR(vpcForm.ipv4Cidr ?? '') ? t('The network is an IPv4 CIDR like 10.0.0.0/16.') : !vpcForm.projectId ? t('Pick a project.') : '',
  )
  async function saveVPC() {
    if (!vpcForm || vpcProblem) return
    const { isNew, ...v } = vpcForm
    if (await act(() => (isNew ? sdn.createVPC(v) : sdn.updateVPC(v.id!, v)), isNew ? t('VPC created') : t('VPC saved'))) vpcForm = null
  }

  let subnetForm = $state<(Partial<Subnet> & { dns: string }) | null>(null)
  let subnetVPC = $derived(vpcs.find((v) => v.id === subnetForm?.vpcId))
  let subnetProblem = $derived(
    !subnetForm ? '' : !/^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(subnetForm.id ?? '') ? t('The ID is 3 to 40 lower-case letters, digits and dashes.')
      : !isCIDR(subnetForm.cidr ?? '', 29) ? t('The network is an IPv4 CIDR like 10.0.1.0/24 (at most /29).')
      : subnetVPC && !cidrContains(subnetVPC.ipv4Cidr, subnetForm.cidr!) ? t('The subnet lies outside the VPC network {cidr}.', { cidr: subnetVPC.ipv4Cidr })
      : subnetForm.gatewayIp && !isIPv4(subnetForm.gatewayIp) ? t('The gateway is an IPv4 address.') : '',
  )
  async function saveSubnet() {
    if (!subnetForm || subnetProblem) return
    const { dns, ...s } = subnetForm
    const body = { ...s, dnsServers: dns.split(/[\s,]+/).filter(Boolean) }
    if (await act(() => sdn.createSubnet(body), t('Subnet created'))) subnetForm = null
  }

  let routesFor = $state<{ vpc: string; text: string } | null>(null)
  async function saveRoutes() {
    if (!routesFor) return
    const routes = routesFor.text.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
      const [destinationCidr, nextHopIp] = l.split(/\s+(?:via\s+)?/)
      return { destinationCidr, nextHopIp }
    })
    if (await act(() => sdn.setRoutes(routesFor!.vpc, routes), t('Routes saved'))) routesFor = null
  }

  let gwFor = $state<(ExternalGateway & { vpc: string }) | null>(null)
  async function saveGateway() {
    if (!gwFor) return
    const { vpc, ...gw } = gwFor
    if (await act(() => sdn.setGateway(vpc, gw), gw.enabled ? t('External gateway set') : t('External gateway removed'))) gwFor = null
  }

  // ---- Security groups --------------------------------------------------------------------------
  let groupForm = $state<(Partial<SecurityGroup> & { isNew: boolean }) | null>(null)
  async function saveGroup() {
    if (!groupForm) return
    const { isNew, ...g } = groupForm
    if (await act(() => (isNew ? sdn.createSecurityGroup(g) : sdn.updateSecurityGroup(g.id!, g)), isNew ? t('Security group created') : t('Security group saved'))) groupForm = null
  }
  let ruleFor = $state<{ group: SecurityGroup; rule: SecurityRule; peer: 'cidr' | 'group' } | null>(null)
  function newRule(g: SecurityGroup) {
    ruleFor = { group: g, peer: 'cidr', rule: { direction: 'ingress', action: 'allow-related', priority: 1000, protocol: 'tcp', portMin: 443, remoteCidr: '0.0.0.0/0' } }
  }
  let ruleProblem = $derived.by(() => {
    if (!ruleFor) return ''
    const r = ruleFor.rule
    if (r.priority < 1 || r.priority > 32767) return t('The priority is 1 to 32767.')
    if ((r.protocol === 'tcp' || r.protocol === 'udp') && r.portMin && (r.portMin < 1 || r.portMin > 65535 || (r.portMax && r.portMax < r.portMin))) return t('The ports are 1 to 65535, the range from low to high.')
    if (ruleFor.peer === 'cidr' && r.remoteCidr && !isCIDR(r.remoteCidr)) return t('The remote network is an IPv4 CIDR like 0.0.0.0/0.')
    if (ruleFor.peer === 'group' && !r.remoteGroupId) return t('Pick the remote group.')
    return ''
  })
  async function saveRule() {
    if (!ruleFor || ruleProblem) return
    const r = { ...ruleFor.rule }
    if (ruleFor.peer === 'cidr') delete r.remoteGroupId
    else delete r.remoteCidr
    if (r.protocol !== 'tcp' && r.protocol !== 'udp') delete r.portMin, delete r.portMax
    if (await act(() => sdn.addRule(ruleFor!.group.id, r), t('Rule added'))) ruleFor = null
  }
  const ACTION: Record<string, string> = { allow: t('allow'), 'allow-related': t('allow'), drop: t('drop'), reject: t('reject') }

  // ---- Load balancers ---------------------------------------------------------------------------
  let lbForm = $state<(Partial<LoadBalancer> & { isNew: boolean; memberText: string; check: boolean }) | null>(null)
  function openLB(lb?: LoadBalancer) {
    lbForm = lb
      ? { ...lb, isNew: false, memberText: lb.members.join('\n'), check: !!lb.healthCheck }
      : { isNew: true, vpcId: vpcs[0]?.id ?? '', name: '', vip: '', port: 80, protocol: 'tcp', memberText: '', check: true }
  }
  let lbProblem = $derived.by(() => {
    if (!lbForm) return ''
    if (!isIPv4(lbForm.vip ?? '')) return t('The virtual IP is an IPv4 address.')
    const members = lbForm.memberText.split(/\s+/).filter(Boolean)
    if (!members.length) return t('Name the members, one ip:port per line.')
    if (members.some((m) => !/^\d+\.\d+\.\d+\.\d+:\d{1,5}$/.test(m) || !isIPv4(m.split(':')[0]))) return t('Every member is ip:port, like 10.0.1.11:8080.')
    return ''
  })
  async function saveLB() {
    if (!lbForm || lbProblem) return
    const { isNew, memberText, check, ...lb } = lbForm
    const body: Partial<LoadBalancer> = {
      ...lb,
      members: memberText.split(/\s+/).filter(Boolean),
      healthCheck: check ? (lb.healthCheck ?? { protocol: lb.protocol ?? 'tcp', port: 0, intervalSeconds: 5, timeoutSeconds: 2, fall: 3, rise: 2 }) : undefined,
    }
    if (await act(() => (isNew ? sdn.createLoadBalancer(body) : sdn.updateLoadBalancer(lb.id!, body)), isNew ? t('Load balancer created') : t('Load balancer saved'))) lbForm = null
  }
  // A member is healthy in the demo's eyes when a machine port has its address.
  const memberUp = (m: string) => ports.some((p) => p.ip === m.split(':')[0])

  // ---- Floating IPs -----------------------------------------------------------------------------
  let fipForm = $state<{ vpcId: string; externalIp: string; description: string } | null>(null)
  async function allocate() {
    if (!fipForm || !isIPv4(fipForm.externalIp)) return
    if (await act(() => sdn.createFloatingIP(fipForm!), t('Floating IP allocated'))) fipForm = null
  }
  let assignFor = $state<{ fip: FloatingIP; portId: string } | null>(null)
  let assignable = $derived(assignFor ? ports.filter((p) => subnets.find((s) => s.id === p.subnetId)?.vpcId === assignFor!.fip.vpcId && p.ip) : [])
  async function assign() {
    if (!assignFor) return
    const p = ports.find((x) => x.id === assignFor!.portId)
    if (!p?.ip) return
    if (await act(() => sdn.assignFloatingIP(assignFor!.fip.id, p.ip!, p.id), t('Floating IP assigned'))) assignFor = null
  }

  // ---- Status and IPsec -------------------------------------------------------------------------
  let reconciling = $state(false)
  async function reconcile() {
    reconciling = true
    try {
      const r = await sdn.reconcile()
      if (r.errors.length) toasts.push({ kind: 'error', title: t('{n} changes not applied', { n: r.errors.length }), detail: r.errors.join('\n') })
      else toasts.push({ kind: 'info', title: t('{n} objects applied to OVN', { n: r.applied }) })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Reconcile failed'), detail: errorText(e) })
    } finally {
      reconciling = false
    }
  }
  let ipsecForm = $state<IPsecConfig | null>(null)
  async function saveIPsec() {
    if (!ipsecForm) return
    try {
      ipsec = await sdn.setIPsec(ipsecForm)
      ipsecForm = null
      toasts.push({ kind: 'info', title: ipsec.enabled ? t('Tunnel encryption on') : t('Tunnel encryption off') })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
      ipsec = await sdn.ipsec().catch(() => ipsec)
    }
  }
</script>

<svelte:head><title>{t('Software-defined network · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Software-defined network')}</h1>
    <p class="lead"><span class="tag first">{t('experimental')}</span> {t('Private networks for projects on OVN: subnets with DHCP joined by a distributed router, security groups, load balancers and floating IPs, the same on every host.')}</p>
  </div>
</header>

<div class="tabs" role="tablist">
  {#each TABS as x (x.key)}
    <button role="tab" aria-selected={view === x.key} class:on={view === x.key} onclick={() => go(x.key)}>{x.label}</button>
  {/each}
</div>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !loaded}
  <p class="muted">{t('Loading…')}</p>
{:else if view === 'vpcs'}
  <div class="bar">
    <p class="muted">{t('A VPC is one project’s private network; its subnets reach each other through the VPC router and the outside through its gateway.')}</p>
    {#if op}<button class="ui-btn primary" onclick={() => (vpcForm = { isNew: true, id: '', name: '', projectId: projects.list[0]?.id ?? '', ipv4Cidr: '10.0.0.0/16', defaultDeny: false })}><Plus size={14} /> {t('New VPC')}</button>{/if}
  </div>
  {#each vpcs as v (v.id)}
    <section class="ui-panel card">
      <header class="card-head">
        <div>
          <h3>{v.name} <span class="tag">{projects.name(v.projectId)}</span> <span class="mono small muted">{v.ipv4Cidr}</span></h3>
          <p class="muted small">
            {v.defaultDeny ? t('Default deny: only what a security group allows reaches the machines.') : t('Open inside: machines reach each other unless a group drops it.')}
            {#if v.gateway}· {t('Out through {ip} on {net} (chassis {chassis})', { ip: v.gateway.externalIp, net: v.gateway.externalNetwork, chassis: v.gateway.gatewayChassis })}{:else}· {t('no external gateway')}{/if}
          </p>
        </div>
        {#if op}
          <div class="right">
            <button class="ui-btn sm" onclick={() => (subnetForm = { vpcId: v.id, id: '', name: '', cidr: '', dhcpEnabled: true, dns: '' })}><Plus size={13} /> {t('Subnet')}</button>
            <button class="ui-btn sm ghost" onclick={() => (routesFor = { vpc: v.id, text: v.router.routes.map((r) => `${r.destinationCidr} via ${r.nextHopIp}`).join('\n') })}>{t('Routes')}</button>
            {#if admin}
              <button class="ui-btn sm ghost" onclick={() => (gwFor = { vpc: v.id, ...(v.gateway ?? { externalNetwork: 'physnet1', gatewayIp: '', externalIp: '', enabled: true, gatewayChassis: '' }) })}>{t('Gateway')}</button>
            {/if}
            <button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => (vpcForm = { ...v, isNew: false })}><Pencil size={13} /></button>
            <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this VPC')} run={() => act(() => sdn.deleteVPC(v.id), t('VPC deleted'))} />
          </div>
        {/if}
      </header>
      {#if v.router.routes.length}
        <p class="small muted">{t('Static routes')}: {#each v.router.routes as r, i (i)}<span class="mono">{r.destinationCidr} → {r.nextHopIp}</span>{i < v.router.routes.length - 1 ? ', ' : ''}{/each}</p>
      {/if}
      <table class="t">
        <thead><tr><th>{t('Subnet')}</th><th>{t('Network')}</th><th>{t('Gateway')}</th><th>{t('DHCP')}</th><th>{t('Machines')}</th><th></th></tr></thead>
        <tbody>
          {#each subnets.filter((s) => s.vpcId === v.id) as s (s.id)}
            {@const plugged = ports.filter((p) => p.subnetId === s.id)}
            <tr>
              <td>{s.name} <span class="mono small muted">{s.id}</span></td>
              <td class="mono">{s.cidr} <span class="muted small">({t('{n} addresses', { n: hostCount(s.cidr) })})</span></td>
              <td class="mono">{s.gatewayIp}</td>
              <td>{#if s.dhcpEnabled}{s.allocationPoolStart ? `${s.allocationPoolStart} – ${s.allocationPoolEnd}` : t('on')}{:else}{t('off')}{/if}</td>
              <td>{plugged.length ? plugged.map((p) => `${vmName(p.vmUuid)} (${p.ip ?? '—'})`).join(', ') : '—'}</td>
              <td class="right-cell">{#if op}<ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this subnet')} run={() => act(() => sdn.deleteSubnet(s.id), t('Subnet deleted'))} />{/if}</td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">{t('No subnets yet.')}</td></tr>
          {/each}
        </tbody>
      </table>
    </section>
  {:else}
    <section class="ui-panel card"><p class="muted">{t('No VPCs yet.')}</p></section>
  {/each}
{:else if view === 'security'}
  <div class="bar">
    <p class="muted">{t('A security group is a set of machine adapters and the traffic allowed to and from them. Rules are stateful: answers to allowed connections come back.')}</p>
    {#if op && vpcs.length}<button class="ui-btn primary" onclick={() => (groupForm = { isNew: true, vpcId: vpcs[0].id, name: '', defaultDeny: true })}><Plus size={14} /> {t('New security group')}</button>{/if}
  </div>
  {#each groups as g (g.id)}
    <section class="ui-panel card">
      <header class="card-head">
        <div>
          <h3>{g.name} <span class="tag">{vpcName(g.vpcId)}</span> {#if g.defaultDeny}<span class="tag danger">{t('default deny')}</span>{/if}</h3>
          <p class="muted small">{g.description ? g.description + ' · ' : ''}{t('{n} adapters', { n: g.memberPorts.length })}{g.memberPorts.length ? ': ' + g.memberPorts.map(portLabel).join(', ') : ''}</p>
        </div>
        {#if op}
          <div class="right">
            <button class="ui-btn sm" onclick={() => newRule(g)}><Plus size={13} /> {t('Rule')}</button>
            <button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => (groupForm = { ...g, isNew: false })}><Pencil size={13} /></button>
            <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this group')} run={() => act(() => sdn.deleteSecurityGroup(g.id), t('Security group deleted'))} />
          </div>
        {/if}
      </header>
      <table class="t">
        <thead><tr><th>{t('Direction')}</th><th>{t('Action')}</th><th>{t('Match')}</th><th>{t('Priority')}</th><th>{t('Note')}</th><th></th></tr></thead>
        <tbody>
          {#each [...g.rules].sort((a, b) => b.priority - a.priority) as r (r.id)}
            <tr>
              <td><span class="tag">{r.direction === 'ingress' ? t('in') : t('out')}</span></td>
              <td><span class="pill" class:drop={r.action === 'drop' || r.action === 'reject'}>{ACTION[r.action]}</span></td>
              <td class="mono">{ruleText(r, groupName)}</td>
              <td class="mono">{r.priority}</td>
              <td class="muted">{r.description ?? ''}</td>
              <td class="right-cell">{#if op}<ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this rule')} run={() => act(() => sdn.deleteRule(g.id, r.id!), t('Rule deleted'))} />{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {:else}
    <section class="ui-panel card"><p class="muted">{t('No security groups yet.')}</p></section>
  {/each}
{:else if view === 'loadbalancers'}
  <div class="bar">
    <p class="muted">{t('A load balancer spreads the connections to a virtual IP over member machines; OVN checks the members over TCP or UDP.')}</p>
    {#if op && vpcs.length}<button class="ui-btn primary" onclick={() => openLB()}><Plus size={14} /> {t('New load balancer')}</button>{/if}
  </div>
  {#each lbs as lb (lb.id)}
    <section class="ui-panel card">
      <header class="card-head">
        <div>
          <h3>{lb.name} <span class="mono small">{lb.vip}:{lb.port}/{lb.protocol}</span> <span class="tag">{vpcName(lb.vpcId)}</span></h3>
          <p class="muted small">
            {#if lb.healthCheck}{t('Checked every {i} s, down after {fall} misses, up after {rise} answers', { i: lb.healthCheck.intervalSeconds, fall: lb.healthCheck.fall, rise: lb.healthCheck.rise })}{:else}{t('No health check')}{/if}
          </p>
        </div>
        {#if op}
          <div class="right">
            <button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => openLB(lb)}><Pencil size={13} /></button>
            <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this load balancer')} run={() => act(() => sdn.deleteLoadBalancer(lb.id), t('Load balancer deleted'))} />
          </div>
        {/if}
      </header>
      <ul class="members">
        {#each lb.members as m (m)}
          <li><span class="dot" class:up={memberUp(m)}></span><span class="mono">{m}</span> <span class="muted small">{memberUp(m) ? portLabel(ports.find((p) => p.ip === m.split(':')[0])?.id) : t('no machine has this address')}</span></li>
        {/each}
      </ul>
    </section>
  {:else}
    <section class="ui-panel card"><p class="muted">{t('No load balancers yet.')}</p></section>
  {/each}
{:else if view === 'floating-ips'}
  <div class="bar">
    <p class="muted">{t('A floating IP is an external address mapped to one machine’s private address; it stays yours when you move it to another machine.')}</p>
    {#if admin && vpcs.length}<button class="ui-btn primary" onclick={() => (fipForm = { vpcId: vpcs[0].id, externalIp: '', description: '' })}><Plus size={14} /> {t('Allocate floating IP')}</button>{/if}
  </div>
  <table class="t">
    <thead><tr><th>{t('External address')}</th><th>{t('VPC')}</th><th>{t('Mapped to')}</th><th>{t('Note')}</th><th></th></tr></thead>
    <tbody>
      {#each fips as f (f.id)}
        <tr>
          <td class="mono">{f.externalIp}</td>
          <td>{vpcName(f.vpcId)}</td>
          <td>{#if f.internalIp}<span class="mono">{f.internalIp}</span> {#if f.portId}<span class="muted small">{portLabel(f.portId)}</span>{/if}{:else}<span class="tag">{t('unassigned')}</span>{/if}</td>
          <td class="muted">{f.description ?? ''}</td>
          <td class="right-cell">
            {#if op}
              {#if f.internalIp}
                <button class="ui-btn sm ghost" onclick={() => act(() => sdn.unassignFloatingIP(f.id), t('Floating IP detached'))}><Unlink size={13} /> {t('Detach')}</button>
              {:else}
                <button class="ui-btn sm" onclick={() => (assignFor = { fip: f, portId: '' })}><Link size={13} /> {t('Assign')}</button>
                {#if admin}<ConfirmButton label={t('Release')} icon={Trash} confirm={t('Release this address')} run={() => act(() => sdn.deleteFloatingIP(f.id), t('Floating IP released'))} />{/if}
              {/if}
            {/if}
          </td>
        </tr>
      {:else}
        <tr><td colspan="5" class="muted">{t('No floating IPs yet.')}</td></tr>
      {/each}
    </tbody>
  </table>
{:else if view === 'status'}
  {#if !status}
    <p class="muted">{t('Loading…')}</p>
  {:else}
    <div class="grid">
      <section class="ui-panel card">
        <h3>{t('This host')}</h3>
        {#if !status.host.installed}
          <p class="muted">{t('OVN is not installed here. Install ovn-host and openvswitch-switch (ovn-central on the database hosts) and point ovn-remote at the central database.')}</p>
        {:else}
          <dl>
            <dt>{t('Role')}</dt><dd>{status.host.role === 'central' ? t('central (runs the OVN databases)') : t('controller')}</dd>
            <dt>{t('Versions')}</dt><dd class="mono">OVS {status.host.ovsVersion} · OVN {status.host.ovnVersion}</dd>
            <dt>{t('Services')}</dt>
            <dd>
              <span class="pill" class:drop={!status.host.serviceOvs}>openvswitch</span>
              <span class="pill" class:drop={!status.host.serviceOvnHost}>ovn-controller</span>
              {#if status.host.role === 'central'}<span class="pill" class:drop={!status.host.serviceOvnCentral}>ovn-northd</span>{/if}
            </dd>
            <dt>{t('Integration bridge')}</dt><dd class="mono">{status.host.integrationBridge || t('missing')}</dd>
            <dt>{t('Tunnel endpoint')}</dt><dd class="mono">{status.host.encapsulationType} {status.host.encapsulationIp}</dd>
            <dt>{t('Central database')}</dt><dd class="mono">{status.host.centralEndpoint || '—'}</dd>
          </dl>
        {/if}
        {#if admin}
          <button class="ui-btn" class:busy={reconciling} disabled={reconciling} onclick={reconcile}><RefreshCw size={14} /> {t('Apply everything to OVN again')}</button>
        {/if}
      </section>
      <section class="ui-panel card">
        <h3>{t('Tunnel mesh')}</h3>
        {#if status.chassisError}<div class="ui-error-box">{status.chassisError}</div>{/if}
        <table class="t">
          <thead><tr><th>{t('Chassis')}</th><th>{t('Host')}</th><th>{t('Endpoint')}</th></tr></thead>
          <tbody>
            {#each status.chassis as c (c.id)}
              <tr><td class="mono">{c.id}</td><td>{c.hostname}</td><td class="mono">{c.encapsulationType} {c.encapsulationIp}</td></tr>
            {:else}
              <tr><td colspan="3" class="muted">{t('No chassis registered.')}</td></tr>
            {/each}
          </tbody>
        </table>
      </section>
      <section class="ui-panel card">
        <h3>{t('Tunnel encryption (IPsec)')} <span class="tag">{t('experimental, not tested on hardware')}</span></h3>
        <p class="muted small">{t('OVN encrypts the Geneve tunnels with each host’s certificate; every host needs its certificate and key at the same paths, and the CA.')}</p>
        {#if ipsec}
          <p>
            <span class="pill" class:drop={ipsec.status === 'error'}>{ipsec.status === 'active' ? t('on') : ipsec.status === 'error' ? t('error') : t('off')}</span>
            {#if ipsec.lastError}<span class="small muted">{ipsec.lastError}</span>{/if}
          </p>
          {#if admin}<button class="ui-btn" onclick={() => (ipsecForm = { ...ipsec!, mode: 'cert' })}>{t('Settings')}</button>{/if}
        {/if}
      </section>
    </div>
  {/if}
{/if}

<Dialog open={!!vpcForm} title={vpcForm?.isNew ? t('New VPC') : t('Edit {name}', { name: vpcForm?.name ?? '' })} width={500} onclose={() => (vpcForm = null)}>
  {#if vpcForm}
    <form id="vpc" class="form" onsubmit={(e) => (e.preventDefault(), saveVPC())}>
      <label class="ui-field"><span class="label">{t('ID')}</span><input class="ui-input mono" bind:value={vpcForm.id} disabled={!vpcForm.isNew} placeholder="vpc-prod" required /></label>
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={vpcForm.name} required /></label>
      <label class="ui-field">
        <span class="label">{t('Project')}</span>
        <select class="ui-input" bind:value={vpcForm.projectId} disabled={!vpcForm.isNew}>
          {#each projects.list as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
      </label>
      <label class="ui-field"><span class="label">{t('Network')}</span><input class="ui-input mono" bind:value={vpcForm.ipv4Cidr} placeholder="10.0.0.0/16" required /></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={vpcForm.defaultDeny} /> {t('Default deny (only security groups let traffic in)')}</label>
      <label class="ui-field"><span class="label">{t('Description')}</span><input class="ui-input" bind:value={vpcForm.description} /></label>
      {#if vpcProblem}<p class="problem">{vpcProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (vpcForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="vpc" disabled={!!vpcProblem}>{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!subnetForm} title={t('New subnet in {vpc}', { vpc: subnetVPC?.name ?? '' })} width={500} onclose={() => (subnetForm = null)}>
  {#if subnetForm}
    <form id="subnet" class="form" onsubmit={(e) => (e.preventDefault(), saveSubnet())}>
      <label class="ui-field"><span class="label">{t('ID')}</span><input class="ui-input mono" bind:value={subnetForm.id} placeholder="prod-web" required /></label>
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={subnetForm.name} required /></label>
      <label class="ui-field"><span class="label">{t('Network')}</span><input class="ui-input mono" bind:value={subnetForm.cidr} placeholder="10.0.1.0/24" required /></label>
      <label class="ui-field"><span class="label">{t('Gateway')}</span><input class="ui-input mono" bind:value={subnetForm.gatewayIp} placeholder={t('the first address')} /></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={subnetForm.dhcpEnabled} /> {t('Hand out addresses by DHCP (OVN)')}</label>
      {#if subnetForm.dhcpEnabled}
        <label class="ui-field"><span class="label">{t('DNS servers')}</span><input class="ui-input mono" bind:value={subnetForm.dns} placeholder="1.1.1.1, 9.9.9.9" /></label>
      {/if}
      {#if subnetProblem}<p class="problem">{subnetProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (subnetForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="subnet" disabled={!!subnetProblem}>{t('Create')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!routesFor} title={t('Static routes')} width={500} onclose={() => (routesFor = null)}>
  {#if routesFor}
    <form id="routes" class="form" onsubmit={(e) => (e.preventDefault(), saveRoutes())}>
      <label class="ui-field">
        <span class="label">{t('One route per line: network via next hop')}</span>
        <textarea class="ui-input mono" rows="5" bind:value={routesFor.text} placeholder="192.168.50.0/24 via 10.0.1.254"></textarea>
      </label>
      <span class="hint">{t('The default route belongs to the external gateway.')}</span>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (routesFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="routes">{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!gwFor} title={t('External gateway')} width={500} onclose={() => (gwFor = null)}>
  {#if gwFor}
    <form id="gw" class="form" onsubmit={(e) => (e.preventDefault(), saveGateway())}>
      <label class="ui-switch"><input type="checkbox" bind:checked={gwFor.enabled} /> {t('Connect the VPC to an external network')}</label>
      {#if gwFor.enabled}
        <label class="ui-field"><span class="label">{t('Provider network')}</span><input class="ui-input mono" bind:value={gwFor.externalNetwork} placeholder="physnet1" required /></label>
        <label class="ui-field"><span class="label">{t('External address')}</span><input class="ui-input mono" bind:value={gwFor.externalIp} placeholder="203.0.113.10/24" required /></label>
        <label class="ui-field"><span class="label">{t('Upstream router')}</span><input class="ui-input mono" bind:value={gwFor.gatewayIp} placeholder="203.0.113.1" required /></label>
        <label class="ui-field">
          <span class="label">{t('Gateway chassis')}</span>
          <input class="ui-input mono" bind:value={gwFor.gatewayChassis} list="chassis" required />
          <datalist id="chassis">{#each status?.chassis ?? [] as c (c.id)}<option value={c.id}>{c.hostname}</option>{/each}</datalist>
          <span class="hint">{t('The host that carries the VPC’s outside traffic (its OVN system-id).')}</span>
        </label>
      {/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (gwFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="gw">{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!groupForm} title={groupForm?.isNew ? t('New security group') : t('Edit {name}', { name: groupForm?.name ?? '' })} width={480} onclose={() => (groupForm = null)}>
  {#if groupForm}
    <form id="sg" class="form" onsubmit={(e) => (e.preventDefault(), saveGroup())}>
      {#if groupForm.isNew}
        <label class="ui-field">
          <span class="label">{t('VPC')}</span>
          <select class="ui-input" bind:value={groupForm.vpcId}>{#each vpcs as v (v.id)}<option value={v.id}>{v.name}</option>{/each}</select>
        </label>
      {/if}
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={groupForm.name} required /></label>
      <label class="ui-field"><span class="label">{t('Description')}</span><input class="ui-input" bind:value={groupForm.description} /></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={groupForm.defaultDeny} /> {t('Drop everything the rules do not allow')}</label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (groupForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="sg">{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!ruleFor} title={t('New rule for {name}', { name: ruleFor?.group.name ?? '' })} width={520} onclose={() => (ruleFor = null)}>
  {#if ruleFor}
    <form id="rule" class="form" onsubmit={(e) => (e.preventDefault(), saveRule())}>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Direction')}</span>
          <select class="ui-input" bind:value={ruleFor.rule.direction}><option value="ingress">{t('in (to the machines)')}</option><option value="egress">{t('out (from the machines)')}</option></select>
        </label>
        <label class="ui-field"><span class="label">{t('Action')}</span>
          <select class="ui-input" bind:value={ruleFor.rule.action}><option value="allow-related">{t('allow')}</option><option value="drop">{t('drop')}</option><option value="reject">{t('reject')}</option></select>
        </label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Protocol')}</span>
          <select class="ui-input" bind:value={ruleFor.rule.protocol}><option value="tcp">TCP</option><option value="udp">UDP</option><option value="icmp">ICMP</option><option value="any">{t('any')}</option></select>
        </label>
        {#if ruleFor.rule.protocol === 'tcp' || ruleFor.rule.protocol === 'udp'}
          <label class="ui-field"><span class="label">{t('Port')}</span><input class="ui-input" type="number" min="1" max="65535" bind:value={ruleFor.rule.portMin} /></label>
          <label class="ui-field"><span class="label">{t('to port')}</span><input class="ui-input" type="number" min="1" max="65535" bind:value={ruleFor.rule.portMax} placeholder={t('same')} /></label>
        {/if}
      </div>
      <fieldset class="kinds">
        <label class="ui-switch"><input type="radio" bind:group={ruleFor.peer} value="cidr" /> {t('Addresses')}</label>
        <label class="ui-switch"><input type="radio" bind:group={ruleFor.peer} value="group" /> {t('Machines of a security group')}</label>
      </fieldset>
      {#if ruleFor.peer === 'cidr'}
        <label class="ui-field"><span class="label">{t('Remote network')}</span><input class="ui-input mono" bind:value={ruleFor.rule.remoteCidr} placeholder="0.0.0.0/0" /></label>
      {:else}
        <label class="ui-field"><span class="label">{t('Remote group')}</span>
          <select class="ui-input" bind:value={ruleFor.rule.remoteGroupId}>
            {#each groups.filter((g) => g.vpcId === ruleFor!.group.vpcId) as g (g.id)}<option value={g.id}>{g.name}</option>{/each}
          </select>
        </label>
      {/if}
      <div class="row">
        <label class="ui-field"><span class="label">{t('Priority')}</span><input class="ui-input" type="number" min="1" max="32767" bind:value={ruleFor.rule.priority} /></label>
        <label class="ui-field"><span class="label">{t('Note')}</span><input class="ui-input" bind:value={ruleFor.rule.description} /></label>
      </div>
      <p class="hint mono">{ruleText(ruleFor.rule, groupName)}</p>
      {#if ruleProblem}<p class="problem">{ruleProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (ruleFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="rule" disabled={!!ruleProblem}>{t('Add rule')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!lbForm} title={lbForm?.isNew ? t('New load balancer') : t('Edit {name}', { name: lbForm?.name ?? '' })} width={520} onclose={() => (lbForm = null)}>
  {#if lbForm}
    <form id="lb" class="form" onsubmit={(e) => (e.preventDefault(), saveLB())}>
      {#if lbForm.isNew}
        <label class="ui-field"><span class="label">{t('VPC')}</span>
          <select class="ui-input" bind:value={lbForm.vpcId}>{#each vpcs as v (v.id)}<option value={v.id}>{v.name}</option>{/each}</select>
        </label>
      {/if}
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={lbForm.name} required /></label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Virtual IP')}</span><input class="ui-input mono" bind:value={lbForm.vip} placeholder="10.0.1.100" required /></label>
        <label class="ui-field"><span class="label">{t('Port')}</span><input class="ui-input" type="number" min="1" max="65535" bind:value={lbForm.port} /></label>
        <label class="ui-field"><span class="label">{t('Protocol')}</span><select class="ui-input" bind:value={lbForm.protocol}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
      </div>
      <label class="ui-field"><span class="label">{t('Members (ip:port, one per line)')}</span><textarea class="ui-input mono" rows="4" bind:value={lbForm.memberText} placeholder="10.0.1.11:8080"></textarea></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={lbForm.check} /> {t('Check the members and skip the ones that do not answer')}</label>
      {#if lbProblem}<p class="problem">{lbProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (lbForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="lb" disabled={!!lbProblem}>{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!fipForm} title={t('Allocate floating IP')} width={460} onclose={() => (fipForm = null)}>
  {#if fipForm}
    <form id="fip" class="form" onsubmit={(e) => (e.preventDefault(), allocate())}>
      <label class="ui-field"><span class="label">{t('VPC')}</span>
        <select class="ui-input" bind:value={fipForm.vpcId}>{#each vpcs as v (v.id)}<option value={v.id}>{v.name}</option>{/each}</select>
      </label>
      <label class="ui-field"><span class="label">{t('External address')}</span><input class="ui-input mono" bind:value={fipForm.externalIp} placeholder="203.0.113.22" required /></label>
      <label class="ui-field"><span class="label">{t('Note')}</span><input class="ui-input" bind:value={fipForm.description} /></label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (fipForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="fip" disabled={!isIPv4(fipForm?.externalIp ?? '')}>{t('Allocate')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!assignFor} title={t('Assign {ip}', { ip: assignFor?.fip.externalIp ?? '' })} width={460} onclose={() => (assignFor = null)}>
  {#if assignFor}
    <form id="assign" class="form" onsubmit={(e) => (e.preventDefault(), assign())}>
      <label class="ui-field"><span class="label">{t('Machine adapter')}</span>
        <select class="ui-input" bind:value={assignFor.portId} required>
          <option value="" disabled>{t('Pick one')}</option>
          {#each assignable as p (p.id)}<option value={p.id}>{vmName(p.vmUuid)} · {p.ip}</option>{/each}
        </select>
      </label>
      {#if !assignable.length}<p class="hint">{t('No machine of this VPC has an address yet.')}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (assignFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="assign" disabled={!assignFor?.portId}>{t('Assign')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!ipsecForm} title={t('Tunnel encryption (IPsec)')} width={520} onclose={() => (ipsecForm = null)}>
  {#if ipsecForm}
    <form id="ipsec" class="form" onsubmit={(e) => (e.preventDefault(), saveIPsec())}>
      <label class="ui-switch"><input type="checkbox" bind:checked={ipsecForm.enabled} /> {t('Encrypt the tunnels between hosts')}</label>
      {#if ipsecForm.enabled}
        <label class="ui-field"><span class="label">{t('Host certificate')}</span><input class="ui-input mono" bind:value={ipsecForm.certFile} placeholder="/etc/ipsec.d/certs/host.pem" required /></label>
        <label class="ui-field"><span class="label">{t('Host private key')}</span><input class="ui-input mono" bind:value={ipsecForm.keyFile} placeholder="/etc/ipsec.d/private/host.key" required /></label>
        <label class="ui-field"><span class="label">{t('CA certificate')}</span><input class="ui-input mono" bind:value={ipsecForm.caCertFile} placeholder="/etc/ipsec.d/cacerts/ca.pem" required /></label>
        <span class="hint">{t('Paths on every host; OVN does not use pre-shared keys.')}</span>
      {/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (ipsecForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="ipsec">{t('Save')}</button>
  {/snippet}
</Dialog>

<style>
  .lead,
  .muted {
    color: var(--text-3);
    margin: 0;
  }
  .small {
    font-size: 12px;
  }
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 16px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
  }
  .tabs button {
    height: 38px;
    padding: 0 12px;
    margin-bottom: -1px;
    border: 0;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--text-2);
    font-size: 13.5px;
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
  }
  .tabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .bar {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 16px;
    margin-bottom: 14px;
  }
  .card {
    padding: 14px 16px;
    margin-bottom: 12px;
  }
  .card-head {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .right-cell {
    text-align: right;
    white-space: nowrap;
  }
  h3 {
    margin: 0 0 4px;
    font-size: 15px;
  }
  .tag {
    display: inline-block;
    margin-left: 4px;
    padding: 1px 7px;
    border: var(--frame) solid var(--ink);
    font: 600 11px var(--font-mono);
    color: var(--text-2);
    vertical-align: middle;
  }
  .tag.first {
    margin: 0 6px 0 0;
  }
  .tag.danger {
    color: var(--danger);
  }
  .pill {
    display: inline-block;
    padding: 1px 8px;
    border: 1px solid var(--ok);
    color: var(--ok);
    font: 600 11px var(--font-mono);
    margin-right: 4px;
  }
  .pill.drop {
    border-color: var(--danger);
    color: var(--danger);
  }
  .t {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .t th {
    text-align: left;
    font: 500 12px var(--font-sans);
    color: var(--text-2);
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .t td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .members {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .members li {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .dot {
    width: 9px;
    height: 9px;
    background: var(--danger);
    border: 1px solid var(--ink);
  }
  .dot.up {
    background: var(--ok);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 12px;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 8px 0 14px;
    font-size: 13px;
  }
  dt {
    color: var(--text-2);
  }
  dd {
    margin: 0;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row {
    display: flex;
    gap: 10px;
  }
  .row > * {
    flex: 1;
  }
  .kinds {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    gap: 14px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
    margin: 0;
  }
  .problem {
    color: var(--danger);
    font-size: 12.5px;
    margin: 0;
  }
  @media (max-width: 720px) {
    .bar,
    .card-head,
    .row {
      flex-direction: column;
    }
    .t {
      display: block;
      overflow-x: auto;
    }
  }
</style>
