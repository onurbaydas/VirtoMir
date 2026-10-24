<script lang="ts">
  import { t } from '../i18n.svelte'
  // Hyper-converged Ceph (ADR-0073): bootstrap a cluster on the VirtoMir hosts, add hosts and
  // OSDs (empty disks only), watch health and capacity, create RBD pools that become libvirt
  // pools on every member, and storage policies that name a pool for new disks. One member
  // manages the cluster; this page talks to it whichever member is shown.
  import '../deck.css'
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import { api, errorText, type Job, type Pool } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatBytes } from '../format'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import {
    cephOn, crushTree, efficiency, policies, protection,
    type CephHost, type CephStatus, type DiskInfo, type PoolSpec, type RBDPool, type StoragePolicy,
  } from '../ceph'

  let { tab = '' }: { tab?: string } = $props()
  const TABS = [
    { key: 'overview', label: t('Health & capacity') },
    { key: 'hosts', label: t('Hosts & disks') },
    { key: 'pools', label: t('RBD pools') },
    { key: 'policies', label: t('Storage policies') },
  ]
  let view = $derived(TABS.some((x) => x.key === tab) ? tab : 'overview')
  const go = (k: string) => router.navigate(`/ceph${k === 'overview' ? '' : '#' + k}`, { replace: true })
  let admin = $derived(session.can('admin'))

  // The member that manages the cluster: undefined = the member shown.
  let manager = $state<string | undefined>(undefined)
  let c = $derived(cephOn(manager))
  let status = $state<CephStatus | null>(null)
  let error = $state('')
  let hosts = $state<CephHost[]>([])
  let rbdPools = $state<RBDPool[]>([])
  let policyList = $state<StoragePolicy[]>([])
  let libvirtPools = $state<Pool[]>([])

  async function load() {
    try {
      let st = await cephOn().status()
      manager = undefined
      if (!st.managed && st.managedBy) {
        manager = st.managedBy.id
        st = await cephOn(manager).status()
      }
      status = st
      error = ''
      if (st.managed) {
        const cc = cephOn(manager)
        ;[hosts, rbdPools] = await Promise.all([cc.hosts().catch(() => []), cc.pools().catch(() => [])])
      }
    } catch (e) {
      error = errorText(e)
    }
    policyList = await policies.list().catch(() => [])
    libvirtPools = await api.pools().catch(() => [])
  }
  $effect(() => {
    void router.host
    void load()
  })

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

  async function follow(job: Job, host?: string): Promise<Job> {
    let j = job
    while (j.state === 'running') {
      await new Promise((r) => setTimeout(r, 2000))
      j = await api.job(job.id, host)
    }
    if (j.state === 'failed') throw new Error(j.message ?? t('failed'))
    return j
  }

  let tel = $derived(status?.telemetry)
  const healthClass = (s?: string) => (s === 'HEALTH_OK' ? '' : s === 'HEALTH_WARN' ? 'warn' : 'drop')
  const healthText = (s?: string) => (s === 'HEALTH_OK' ? t('healthy') : s === 'HEALTH_WARN' ? t('warning') : s === 'HEALTH_ERR' ? t('error') : t('unknown'))
  let tree = $derived(tel ? crushTree(tel.osdTree) : [])
  let pgStates = $derived(tel ? Object.entries(tel.pgs.states).sort((a, b) => b[1] - a[1]) : [])

  // ---- Bootstrap --------------------------------------------------------------------------
  let boot = $state<{ monIp: string; clusterNetwork: string; initialOsdDisk: string; singleHostDefaults: boolean } | null>(null)
  let bootDisks = $state<DiskInfo[]>([])
  let booting = $state(false)
  let bootProblem = $derived(
    !boot ? '' : !/^[0-9a-fA-F.:]+$/.test(boot.monIp) ? t('The monitor address is this host’s IP address on the storage network.')
      : boot.clusterNetwork && !/^[0-9a-fA-F.:]+\/\d{1,3}$/.test(boot.clusterNetwork) ? t('The cluster network is a CIDR like 10.10.0.0/24.') : '',
  )
  async function openBoot() {
    boot = { monIp: '', clusterNetwork: '', initialOsdDisk: '', singleHostDefaults: !cluster.clustered }
    bootDisks = await cephOn().disks().catch(() => [])
  }
  async function runBoot() {
    if (!boot || bootProblem) return
    booting = true
    const spec = { ...boot, clusterNetwork: boot.clusterNetwork || undefined, initialOsdDisk: boot.initialOsdDisk || undefined }
    boot = null
    toasts.push({ kind: 'info', title: t('Bootstrapping Ceph'), detail: t('cephadm downloads the Ceph image and starts the first monitor; this takes a few minutes.') })
    await act(async () => follow(await cephOn().bootstrap(spec)), t('Ceph cluster bootstrapped'))
    booting = false
  }

  // ---- Hosts and disks --------------------------------------------------------------------
  let addHostFor = $state<{ member: string } | null>(null)
  let candidates = $derived(cluster.members.filter((m) => !hosts.some((h) => h.hostname === m.name)))
  let disksOf = $state<Record<string, DiskInfo[] | string>>({})
  async function loadDisks(h: CephHost) {
    const memberID = h.memberId && !cluster.members.find((m) => m.id === h.memberId)?.self ? h.memberId : undefined
    try {
      disksOf[h.hostname] = await cephOn(memberID).disks()
    } catch (e) {
      disksOf[h.hostname] = errorText(e)
    }
  }
  async function addOSD(h: CephHost, d: DiskInfo) {
    await act(async () => follow(await c.addOSD(h.hostname, d.path), manager), t('OSD added on {host}', { host: h.hostname }))
    await loadDisks(h)
  }

  // ---- Pools ------------------------------------------------------------------------------
  let poolForm = $state<(PoolSpec & { register: boolean }) | null>(null)
  let poolProblem = $derived(
    !poolForm ? '' : !/^[A-Za-z0-9][A-Za-z0-9._-]{0,58}$/.test(poolForm.name) ? t('The pool name is letters, digits, dots, dashes and underscores.')
      : poolForm.register && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(poolForm.libvirtPool ?? '') ? t('Name the libvirt pool the hosts will use.') : '',
  )
  async function savePool() {
    if (!poolForm || poolProblem) return
    const { register, ...spec } = poolForm
    if (!register) delete spec.libvirtPool
    if (spec.type === 'erasure') delete spec.replicaSize
    else {
      delete spec.ecK
      delete spec.ecM
    }
    poolForm = null
    await act(async () => {
      const res = await c.createPool(spec)
      const bad = (res.results ?? []).filter((r) => !r.ok)
      if (bad.length) toasts.push({ kind: 'warn', title: t('The libvirt pool is missing on {n} hosts', { n: bad.length }), detail: bad.map((r) => `${r.host}: ${r.error}`).join('\n') })
    }, t('Pool {name} created', { name: spec.name }))
  }

  // ---- Policies ---------------------------------------------------------------------------
  let policyForm = $state<(StoragePolicy & { isNew: boolean }) | null>(null)
  let policyProblem = $derived(
    !policyForm ? '' : policyForm.isNew && !/^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$/.test(policyForm.id) ? t('The ID is 2 to 40 lower-case letters, digits and dashes.')
      : !policyForm.name.trim() ? t('Give the policy a name.') : !policyForm.pool ? t('Pick the pool its disks go to.') : '',
  )
  async function savePolicy() {
    if (!policyForm || policyProblem) return
    const { isNew, ...p } = policyForm
    if (await act(() => (isNew ? policies.create(p) : policies.update(p.id, p)), t('Policy saved'))) policyForm = null
  }
  const poolOf = (name: string) => libvirtPools.find((p) => p.name === name)
  const rbdFor = (libvirtName: string) => rbdPools.find((p) => p.libvirtPool === libvirtName)
</script>

<svelte:head><title>{t('Ceph storage · VirtoMir')}</title></svelte:head>

<div class="deck">
  <header class="page-head">
    <div>
      <h1>{t('Ceph storage')}</h1>
      <p class="lead"><span class="tag first">{t('experimental, not tested on hardware')}</span> {t('Hyper-converged storage on the VirtoMir hosts themselves: their disks become one replicated Ceph cluster, and its pools hold machine disks every host can run.')}</p>
    </div>
    {#if status}<button class="ui-btn ghost" onclick={load}><RefreshCw size={14} /> {t('Refresh')}</button>{/if}
  </header>

  <div class="tabs" role="tablist">
    {#each TABS as x (x.key)}
      <button role="tab" aria-selected={view === x.key} class:on={view === x.key} onclick={() => go(x.key)}>{x.label}</button>
    {/each}
  </div>

  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if !status}
    <p class="muted">{t('Loading…')}</p>
  {:else if view === 'policies'}
    <div class="bar">
      <p class="muted">{t('A storage policy names the pool new disks of that kind go to, so people choose "fast" or "cold" instead of a pool. The default policy is used when a new machine names neither.')}</p>
      {#if admin}<button class="ui-btn primary" onclick={() => (policyForm = { isNew: true, id: '', name: '', description: '', pool: libvirtPools[0]?.name ?? '', default: policyList.length === 0 })}><Plus size={14} /> {t('New policy')}</button>{/if}
    </div>
    <table class="t">
      <thead><tr><th>{t('Policy')}</th><th>{t('Pool')}</th><th>{t('Protection')}</th><th>{t('Description')}</th><th></th></tr></thead>
      <tbody>
        {#each policyList as p (p.id)}
          {@const rbd = rbdFor(p.pool)}
          <tr>
            <td>{p.name} <span class="mono small muted">{p.id}</span> {#if p.default}<span class="pill">{t('default')}</span>{/if}</td>
            <td class="mono">{p.pool} {#if !poolOf(p.pool)}<span class="tag danger">{t('missing on this host')}</span>{/if}</td>
            <td>{rbd ? protection(rbd) : (poolOf(p.pool)?.type ?? '—')}</td>
            <td class="muted">{p.description ?? ''}</td>
            <td class="right-cell">
              {#if admin}
                <button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => (policyForm = { ...p, isNew: false })}><Pencil size={13} /></button>
                <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this policy')} run={() => act(() => policies.delete(p.id), t('Policy deleted'))} />
              {/if}
            </td>
          </tr>
        {:else}
          <tr><td colspan="5" class="muted">{t('No storage policies yet.')}</td></tr>
        {/each}
      </tbody>
    </table>
  {:else if !status.managed}
    <section class="ui-panel card">
      <h3>{t('No Ceph cluster yet')}</h3>
      {#if !status.installed && !status.cephadm}
        <p class="muted">{t('Install cephadm on this host (apt install cephadm, or dnf install cephadm) together with podman, then bootstrap the first monitor here. Every host that should store data needs podman too.')}</p>
      {:else}
        <p class="muted">{t('Bootstrapping starts the first monitor and manager on this host; then add the other members as Ceph hosts and give Ceph their empty disks. Three hosts with at least one disk each keep three copies of everything.')}</p>
      {/if}
      {#if admin}
        <div class="right" style="margin-top: 10px">
          <button class="ui-btn primary" class:busy={booting} disabled={!status.cephadm || booting} onclick={openBoot}><Plus size={14} /> {t('Bootstrap Ceph here')}</button>
          {#if status.installed}<button class="ui-btn" onclick={() => act(() => cephOn().adopt(), t('Ceph cluster adopted'))}>{t('Use the existing cluster on this host')}</button>{/if}
        </div>
      {/if}
    </section>
  {:else if view === 'overview'}
    {#if manager}<p class="muted small">{t('This cluster is managed from {host}.', { host: status.managedBy?.name ?? '' })}</p>{/if}
    {#if status.error}<div class="ui-error-box">{t('Ceph did not answer: {error}', { error: status.error })}</div>{/if}
    {#if tel}
      <div class="grid">
        <section class="ui-panel card">
          <h3>{t('Health')} <span class="pill {healthClass(tel.health.status)}">{healthText(tel.health.status)}</span></h3>
          <p class="mono small muted">{tel.fsid}</p>
          {#each tel.health.checks as ch (ch.name)}
            <p class="small"><span class="pill {ch.severity === 'HEALTH_ERR' ? 'drop' : 'warn'}">{ch.name}</span> {ch.summary}</p>
            {#each ch.detail ?? [] as d, i (i)}<p class="small muted mono">· {d}</p>{/each}
          {:else}
            <p class="muted small">{t('Ceph reports no problems.')}</p>
          {/each}
        </section>
        <section class="ui-panel card">
          <h3>{t('Daemons')}</h3>
          <dl>
            <dt>{t('Monitors')}</dt><dd>{t('{q} of {n} in quorum', { q: tel.daemons.quorum.length, n: tel.daemons.mons })} <span class="muted small">{tel.daemons.quorum.join(', ')}</span></dd>
            <dt>{t('Manager')}</dt><dd><span class="pill {tel.daemons.mgrUp ? '' : 'drop'}">{tel.daemons.mgrUp ? t('active') : t('none active')}</span></dd>
            <dt>OSDs</dt><dd>{t('{up} up, {in} in, of {n}', { up: tel.daemons.osdsUp, in: tel.daemons.osdsIn, n: tel.daemons.osds })}</dd>
            <dt>noout</dt>
            <dd>
              {#if admin}
                <label class="ui-switch"><input type="checkbox" checked={tel.daemons.noout} onchange={(e) => act(() => c.setNoout((e.target as HTMLInputElement).checked), (e.target as HTMLInputElement).checked ? t('noout set: down OSDs stay in') : t('noout cleared'))} /> {tel.daemons.noout ? t('set (no rebalancing while hosts are serviced)') : t('off')}</label>
              {:else}{tel.daemons.noout ? t('set') : t('off')}{/if}
            </dd>
          </dl>
        </section>
        <section class="ui-panel card">
          <h3>{t('Capacity')}</h3>
          <p class="big">{formatBytes(tel.capacity.usedBytes)} <span class="muted small">/ {formatBytes(tel.capacity.totalBytes)} {t('raw')}</span></p>
          <div class="meter" class:warn={tel.capacity.percentUsed > 70} class:danger={tel.capacity.percentUsed > 85}><span style:width="{Math.min(100, tel.capacity.percentUsed)}%"></span></div>
          <p class="muted small">{t('{free} free · Ceph stops writes at 95 %, so keep it under 80 %.', { free: formatBytes(tel.capacity.availBytes) })}</p>
          <h3 style="margin-top: 12px">{t('Placement groups')}</h3>
          <p>{#each pgStates as [st, n] (st)}<span class="pill {st === 'active+clean' ? '' : 'warn'}">{st} {n}</span>{/each}</p>
        </section>
      </div>
      <section class="ui-panel card">
        <h3>{t('OSD tree')}</h3>
        <ul class="tree">
          {#each tree as root (root.node.id)}
            <li>
              <span class="mono">{root.node.name}</span> <span class="tag">{root.node.type}</span>
              <ul>
                {#each root.children as h (h.node.id)}
                  <li>
                    <strong>{h.node.name}</strong> {#if status.config?.maintenanceHosts?.includes(h.node.name)}<span class="pill warn">{t('maintenance')}</span>{/if}
                    <ul>
                      {#each h.children as o (o.id)}
                        <li><span class="dot" class:up={o.status === 'up'}></span><span class="mono">{o.name}</span> {#if o.deviceClass}<span class="tag">{o.deviceClass}</span>{/if} <span class="muted small">{o.status ?? ''} · {o.in ? t('in') : t('out')} · {t('weight {w}', { w: o.weight.toFixed(3) })}</span></li>
                      {:else}
                        <li class="muted small">{t('no OSDs')}</li>
                      {/each}
                    </ul>
                  </li>
                {/each}
              </ul>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {:else if view === 'hosts'}
    <div class="bar">
      <p class="muted">{t('Ceph hosts run its daemons; an OSD is one whole empty disk. Disks with partitions, file systems or LVM/ZFS labels are never touched.')}</p>
      {#if admin && candidates.length}<button class="ui-btn primary" onclick={() => (addHostFor = { member: candidates[0].id })}><Plus size={14} /> {t('Add host')}</button>{/if}
    </div>
    {#each hosts as h (h.hostname)}
      {@const disks = disksOf[h.hostname]}
      <section class="ui-panel card">
        <header class="card-head">
          <div>
            <h3><span class="dot" class:up={h.status === 'online'} class:warn={h.status === 'maintenance'}></span>{h.hostname} <span class="mono small muted">{h.ip}</span> {#each h.labels as l (l)}<span class="tag">{l}</span>{/each}</h3>
            <p class="muted small">{h.status === 'online' ? t('online') : h.status === 'maintenance' ? t('in maintenance: its daemons are stopped and noout is set') : t('offline')}{h.memberId ? '' : ' · ' + t('not a VirtoMir member')}</p>
          </div>
          <div class="right">
            <button class="ui-btn sm" onclick={() => loadDisks(h)}>{t('Disks')}</button>
            {#if admin}
              {#if h.status === 'maintenance'}
                <button class="ui-btn sm" onclick={() => act(() => c.maintenance('exit', h.hostname), t('{host} is back', { host: h.hostname }))}>{t('End maintenance')}</button>
              {:else}
                <ConfirmButton label={t('Maintenance')} confirm={t('Stop Ceph on {host}', { host: h.hostname })} run={() => act(() => c.maintenance('enter', h.hostname), t('{host} in maintenance', { host: h.hostname }))} />
              {/if}
            {/if}
          </div>
        </header>
        {#if typeof disks === 'string'}
          <div class="ui-error-box">{disks}</div>
        {:else if disks}
          <table class="t">
            <thead><tr><th>{t('Disk')}</th><th>{t('Size')}</th><th>{t('Kind')}</th><th>{t('State')}</th><th></th></tr></thead>
            <tbody>
              {#each disks as d (d.path)}
                <tr class:disabled={!d.empty}>
                  <td class="mono">{d.path}</td>
                  <td class="mono">{formatBytes(d.sizeBytes)}</td>
                  <td>{d.deviceType.toUpperCase()}</td>
                  <td>{#if d.empty}<span class="pill">{t('empty')}</span>{:else}<span class="pill drop">{t('contains data')}</span> <span class="small">{d.refusalReason}</span>{/if}</td>
                  <td class="right-cell">{#if admin && d.empty}<ConfirmButton label={t('Make OSD')} confirm={t('Give {disk} to Ceph', { disk: d.name })} run={() => addOSD(h, d)} />{/if}</td>
                </tr>
              {:else}
                <tr><td colspan="5" class="muted">{t('No disks found.')}</td></tr>
              {/each}
            </tbody>
          </table>
          {#if disks.some((d) => !d.empty)}<p class="hint">{t('A disk with data needs wiping by hand first (wipefs -a), if you are sure it holds nothing you need.')}</p>{/if}
        {/if}
      </section>
    {:else}
      <section class="ui-panel card"><p class="muted">{t('No Ceph hosts.')}</p></section>
    {/each}
  {:else if view === 'pools'}
    <div class="bar">
      <p class="muted">{t('RBD pools hold machine disks. Replicated pools keep whole copies; erasure-coded pools split data into chunks plus parity and use less space for colder data.')}</p>
      {#if admin}<button class="ui-btn primary" onclick={() => (poolForm = { name: '', type: 'replicated', replicaSize: 3, ecK: 4, ecM: 2, deviceClass: '', register: true, libvirtPool: '' })}><Plus size={14} /> {t('New pool')}</button>{/if}
    </div>
    <table class="t">
      <thead><tr><th>{t('Pool')}</th><th>{t('Protection')}</th><th>{t('Stored')}</th><th>{t('Used')}</th><th>{t('libvirt pool')}</th><th></th></tr></thead>
      <tbody>
        {#each rbdPools as p (p.name)}
          <tr>
            <td class="mono">{p.name}{#if p.dataPool} <span class="muted small">+ {p.dataPool}</span>{/if}</td>
            <td>{protection(p)}</td>
            <td class="mono">{formatBytes(p.storedBytes)}</td>
            <td class="mono">{p.percentUsed.toFixed(1)} % <span class="muted small">{t('{free} left', { free: formatBytes(p.maxAvailBytes) })}</span></td>
            <td>{#if p.libvirtPool}<span class="tag">{p.libvirtPool}</span>{:else}<span class="muted">—</span>{/if}</td>
            <td class="right-cell">{#if admin && !p.libvirtPool}<ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete {name} and all its images', { name: p.name })} run={() => act(() => c.deletePool(p.name), t('Pool deleted'))} />{/if}</td>
          </tr>
        {:else}
          <tr><td colspan="6" class="muted">{t('No RBD pools yet.')}</td></tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<Dialog open={!!boot} title={t('Bootstrap a Ceph cluster')} subtitle={t('The first monitor and manager run on this host.')} width={520} onclose={() => (boot = null)}>
  {#if boot}
    <form id="boot" class="deck-form" onsubmit={(e) => (e.preventDefault(), runBoot())}>
      <label class="ui-field"><span class="label">{t('Monitor address')}</span><input class="ui-input mono" bind:value={boot.monIp} placeholder="10.0.0.11" required /><span class="hint">{t('This host’s IP on the network the other hosts and clients reach Ceph on.')}</span></label>
      <label class="ui-field"><span class="label">{t('Cluster network (optional)')}</span><input class="ui-input mono" bind:value={boot.clusterNetwork} placeholder="10.10.0.0/24" /><span class="hint">{t('A separate network for replication between OSDs; empty uses the same one.')}</span></label>
      <label class="ui-field">
        <span class="label">{t('First OSD (optional)')}</span>
        <select class="ui-input" bind:value={boot.initialOsdDisk}>
          <option value="">{t('— none yet —')}</option>
          {#each bootDisks as d (d.path)}<option value={d.path} disabled={!d.empty}>{d.path} · {formatBytes(d.sizeBytes)} · {d.empty ? d.deviceType.toUpperCase() : t('contains data')}</option>{/each}
        </select>
      </label>
      <label class="ui-switch"><input type="checkbox" bind:checked={boot.singleHostDefaults} /> {t('Single host: keep copies on different disks of this host instead of different hosts')}</label>
      {#if bootProblem}<p class="problem">{bootProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (boot = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="boot" disabled={!!bootProblem}>{t('Bootstrap')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!addHostFor} title={t('Add a host to Ceph')} width={460} onclose={() => (addHostFor = null)}>
  {#if addHostFor}
    <form id="addhost" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => c.addHost(addHostFor!.member), t('Host added')).then((ok) => ok && (addHostFor = null)))}>
      <label class="ui-field">
        <span class="label">{t('Member')}</span>
        <select class="ui-input" bind:value={addHostFor.member}>{#each candidates as m (m.id)}<option value={m.id}>{m.name}</option>{/each}</select>
      </label>
      <p class="hint">{t('The cluster’s SSH key goes to the member’s root account so cephadm can run containers there; the member needs podman.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (addHostFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="addhost">{t('Add')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!poolForm} title={t('New RBD pool')} width={520} onclose={() => (poolForm = null)}>
  {#if poolForm}
    <form id="pool" class="deck-form" onsubmit={(e) => (e.preventDefault(), savePool())}>
      <label class="ui-field"><span class="label">{t('Ceph pool name')}</span><input class="ui-input mono" bind:value={poolForm.name} placeholder="rbd-fast" required /></label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Protection')}</span>
          <select class="ui-input" bind:value={poolForm.type}><option value="replicated">{t('copies (replicated)')}</option><option value="erasure">{t('erasure coding')}</option></select>
        </label>
        <label class="ui-field"><span class="label">{t('Disks')}</span>
          <select class="ui-input" bind:value={poolForm.deviceClass}><option value="">{t('any')}</option><option value="ssd">SSD</option><option value="nvme">NVMe</option><option value="hdd">HDD</option></select>
        </label>
      </div>
      {#if poolForm.type === 'replicated'}
        <label class="ui-field"><span class="label">{t('Copies')}</span><input class="ui-input" type="number" min="1" max="5" bind:value={poolForm.replicaSize} /></label>
      {:else}
        <div class="row">
          <label class="ui-field"><span class="label">{t('Data chunks (k)')}</span><input class="ui-input" type="number" min="2" max="16" bind:value={poolForm.ecK} /></label>
          <label class="ui-field"><span class="label">{t('Parity chunks (m)')}</span><input class="ui-input" type="number" min="1" max="8" bind:value={poolForm.ecM} /></label>
        </div>
        <p class="hint">{t('Needs k+m hosts with OSDs; survives m of them failing.')}</p>
      {/if}
      <p class="hint">{t('{pct} % of the raw space is usable.', { pct: Math.round(efficiency(poolForm) * 100) })}</p>
      <label class="ui-switch"><input type="checkbox" bind:checked={poolForm.register} /> {t('Make it a libvirt pool on every host')}</label>
      {#if poolForm.register}<label class="ui-field"><span class="label">{t('libvirt pool name')}</span><input class="ui-input mono" bind:value={poolForm.libvirtPool} placeholder="ceph-fast" /></label>{/if}
      {#if poolProblem}<p class="problem">{poolProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (poolForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="pool" disabled={!!poolProblem}>{t('Create')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!policyForm} title={policyForm?.isNew ? t('New storage policy') : t('Edit {name}', { name: policyForm?.name ?? '' })} width={480} onclose={() => (policyForm = null)}>
  {#if policyForm}
    <form id="policy" class="deck-form" onsubmit={(e) => (e.preventDefault(), savePolicy())}>
      {#if policyForm.isNew}<label class="ui-field"><span class="label">{t('ID')}</span><input class="ui-input mono" bind:value={policyForm.id} placeholder="fast" required /></label>{/if}
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={policyForm.name} placeholder={t('Fast (SSD, 3 copies)')} required /></label>
      <label class="ui-field"><span class="label">{t('Description')}</span><input class="ui-input" bind:value={policyForm.description} /></label>
      <label class="ui-field">
        <span class="label">{t('Pool')}</span>
        <select class="ui-input" bind:value={policyForm.pool}>{#each libvirtPools as p (p.name)}<option value={p.name}>{p.name} ({p.type})</option>{/each}</select>
        <span class="hint">{t('The same pool name has to exist on every host where machines are created with this policy.')}</span>
      </label>
      <label class="ui-switch"><input type="checkbox" bind:checked={policyForm.default} /> {t('Default for new disks')}</label>
      {#if policyProblem}<p class="problem">{policyProblem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (policyForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="policy" disabled={!!policyProblem}>{t('Save')}</button>
  {/snippet}
</Dialog>
