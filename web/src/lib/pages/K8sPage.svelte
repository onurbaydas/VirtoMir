<script lang="ts">
  import { t } from '../i18n.svelte'
  // Kubernetes clusters on VirtoMir machines (ADR-0076): kubeadm through cloud-init, the
  // admin kubeconfig signed by VirtoMir, scaling and minor upgrades one node at a time.
  import '../deck.css'
  import Plus from '@lucide/svelte/icons/plus'
  import Download from '@lucide/svelte/icons/download'
  import Trash from '@lucide/svelte/icons/trash'
  import ArrowUpCircle from '@lucide/svelte/icons/circle-arrow-up'
  import { api, errorText, type Volume } from '../api'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import { cluster } from '../cluster.svelte'
  import { physnet, type IPPool } from '../physnet'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import { busy, k8s, nextVersion, specProblem, type K8sCluster, type K8sList, type K8sSpec } from '../k8s'

  let admin = $derived(session.can('admin'))
  let data = $state<K8sList | null>(null)
  let error = $state('')
  let open = $state<Record<string, boolean>>({})

  const load = async () => {
    try {
      data = await k8s.list()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void router.host
    void load()
  })
  // While something runs, follow it.
  $effect(() => {
    if (!data?.clusters.some(busy)) return
    const id = setInterval(() => void load(), 5000)
    return () => clearInterval(id)
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

  async function download(c: K8sCluster) {
    try {
      const text = await k8s.kubeconfig(c.id)
      const a = document.createElement('a')
      a.href = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
      a.download = `${c.spec.name}.kubeconfig`
      a.click()
      URL.revokeObjectURL(a.href)
    } catch (e) {
      toasts.push({ kind: 'error', title: t('No kubeconfig'), detail: errorText(e) })
    }
  }

  const stateClass = (s: string) => (s === 'ready' ? '' : s === 'failed' ? 'drop' : 'warn')
  const stateText = (s: string) =>
    ({
      provisioning: t('creating'),
      ready: t('ready'),
      scaling: t('scaling'),
      upgrading: t('upgrading'),
      deleting: t('deleting'),
      failed: t('failed'),
      creating: t('creating'),
      joining: t('joining'),
      draining: t('draining'),
    })[s] ?? s
  const hostName = (id: string) => cluster.members.find((m) => m.id === id)?.name ?? (id ? id.slice(0, 8) : t('this host'))

  // ---- new cluster ------------------------------------------------------------------------
  let form = $state<(K8sSpec & { keys: string }) | null>(null)
  let images = $state<{ pool: string; vol: Volume }[]>([])
  let pools = $state<string[]>([])
  let networks = $state<string[]>([])
  let ipPools = $state<IPPool[]>([])
  async function newCluster() {
    form = {
      name: '', version: data?.defaultVersion ?? '', controlPlanes: 1, workers: 2,
      controlPlane: { vcpus: 2, memoryMiB: 2048, diskGiB: 20 }, worker: { vcpus: 2, memoryMiB: 2048, diskGiB: 20 },
      image: '', storagePool: '', network: 'default', ipPool: '', vip: '', cni: 'flannel', podCidr: '10.244.0.0/16', serviceCidr: '10.96.0.0/12',
      hosts: [], keys: '',
    }
    try {
      const [p, n] = await Promise.all([api.pools(), api.networks()])
      pools = p.filter((x) => x.active).map((x) => x.name)
      networks = n.map((x) => x.name)
      ipPools = await physnet.pools().catch(() => [])
      const lists = await Promise.all(pools.map(async (name) => ({ pool: name, v: await api.volumes(name).catch(() => []) })))
      images = lists.flatMap(({ pool, v }) => v.filter((x) => x.format === 'qcow2' && x.usedBy.length === 0 && !x.name.endsWith('-cidata.iso')).map((vol) => ({ pool, vol })))
      const deb = images.find((i) => /debian|ubuntu/i.test(i.vol.name))
      if (form && deb) form.image = deb.vol.path
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not load the choices'), detail: errorText(e) })
    }
  }
  const problems: Record<string, string> = {
    name: t('The name is 3 to 30 lower-case letters, digits and dashes, starting with a letter.'),
    image: t('Choose the cloud image the nodes start from.'),
    network: t('Choose the network of the nodes.'),
    vip: t('Three control planes share one API address: give it, or an IP pool to take it from.'),
    'cp-size': t('A control plane needs at least 2 vCPUs and 1800 MiB of memory.'),
    workers: t('A cluster has 0 to 50 workers.'),
  }
  let problem = $derived(form ? (problems[specProblem(form)] ?? '') : '')
  async function create() {
    if (!form || problem) return
    const { keys, ...s } = form
    const body: K8sSpec = {
      ...s, controlPlanes: Number(s.controlPlanes), workers: Number(s.workers),
      controlPlane: { vcpus: Number(s.controlPlane.vcpus), memoryMiB: Number(s.controlPlane.memoryMiB), diskGiB: Number(s.controlPlane.diskGiB) },
      worker: { vcpus: Number(s.worker.vcpus), memoryMiB: Number(s.worker.memoryMiB), diskGiB: Number(s.worker.diskGiB) },
      sshKeys: keys.split('\n').map((k) => k.trim()).filter(Boolean),
      ipPool: s.ipPool || undefined, vip: s.vip || undefined, storagePool: s.storagePool || undefined,
    }
    if (await act(() => k8s.create(body), t('Cluster is being created'))) form = null
  }

  let scaleFor = $state<{ c: K8sCluster; workers: number } | null>(null)
</script>

<svelte:head><title>{t('Kubernetes · VirtoMir')}</title></svelte:head>

<div class="deck">
  <header class="page-head">
    <div>
      <h1>{t('Kubernetes')}</h1>
      <p class="lead">{t('Kubernetes clusters on VirtoMir machines: kubeadm with containerd, set up through cloud-init, with an admin kubeconfig signed by VirtoMir. Scale the workers and upgrade one minor version at a time, node by node.')}</p>
    </div>
    {#if admin && data?.enabled}<button class="ui-btn primary" onclick={newCluster}><Plus size={14} /> {t('New cluster')}</button>{/if}
  </header>

  {#if error}<div class="ui-error-box">{error}</div>{/if}

  {#if data === null}
    <p class="muted">{t('Loading…')}</p>
  {:else if !data.enabled}
    <section class="ui-panel card"><p class="muted">{t('Kubernetes clusters are not available on this host.')}</p></section>
  {:else}
    {#each data.clusters as c (c.id)}
      {@const next = nextVersion(c.version, data.versions)}
      <section class="ui-panel card">
        <header class="card-head">
          <div>
            <h3>{c.spec.name} <span class="pill {stateClass(c.state)}">{stateText(c.state)}</span> <span class="tag">Kubernetes {c.version}</span> <span class="tag">{c.spec.cni}</span></h3>
            <p class="muted small">
              {t('{cp} control planes, {w} workers', { cp: c.spec.controlPlanes, w: c.nodes.length - c.nodes.filter((n) => n.role === 'control-plane').length })}
              {#if c.endpoint}· <span class="mono">{c.endpoint}</span>{/if}
              · {t('created by {user}', { user: c.createdBy })}
            </p>
            {#if c.message && c.state === 'failed'}<pre class="ui-error-box small">{c.message}</pre>{:else if c.message}<p class="small">{c.message}…</p>{/if}
          </div>
          {#if admin}
            <div class="right">
              {#if c.endpoint}<button class="ui-btn sm" onclick={() => download(c)}><Download size={13} /> {t('kubeconfig')}</button>{/if}
              {#if !busy(c)}
                <button class="ui-btn sm" onclick={() => (scaleFor = { c, workers: c.nodes.length - c.nodes.filter((n) => n.role === 'control-plane').length })}>{t('Scale…')}</button>
                {#if next || c.upgradeTo}
                  <ConfirmButton label={t('Upgrade to {v}', { v: c.upgradeTo || next })} icon={ArrowUpCircle} confirm={t('Every node is drained and upgraded in turn')} run={() => act(() => k8s.upgrade(c.id), t('Upgrade started'))} />
                {/if}
              {/if}
              {#if c.state !== 'deleting'}
                <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete the cluster and its machines')} run={() => act(() => k8s.delete(c.id), t('Cluster is being deleted'))} />
              {/if}
            </div>
          {/if}
        </header>
        <table class="t">
          <thead><tr><th>{t('Node')}</th><th>{t('Role')}</th><th>{t('State')}</th><th>{t('Address')}</th><th>{t('Version')}</th><th>{t('Host')}</th></tr></thead>
          <tbody>
            {#each c.nodes as n (n.name)}
              <tr>
                <td class="mono">{#if n.uuid && n.hostId === cluster.current?.id}<a href={`/machines/${n.uuid}`} onclick={(e) => (e.preventDefault(), router.navigate(`/machines/${n.uuid}`))}>{n.name}</a>{:else}{n.name}{/if}</td>
                <td>{n.role === 'control-plane' ? t('control plane') : t('worker')}</td>
                <td><span class="pill {stateClass(n.state)}" title={n.message ?? ''}>{stateText(n.state)}</span></td>
                <td class="mono small">{n.ip ?? ''}</td>
                <td class="mono small">{n.version ?? ''}</td>
                <td class="small">{hostName(n.hostId)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        <button class="ui-btn sm ghost" onclick={() => (open[c.id] = !open[c.id])}>{open[c.id] ? t('Hide the log') : t('Show the log ({n})', { n: c.events.length })}</button>
        {#if open[c.id]}
          <ol class="log small">
            {#each [...c.events].reverse() as e, i (i)}
              <li class:err={e.error}><span class="mono muted">{new Date(e.at).toLocaleTimeString()}</span> {e.message}</li>
            {/each}
          </ol>
        {/if}
      </section>
    {:else}
      <section class="ui-panel card">
        <p class="muted">{t('No Kubernetes clusters yet. The nodes need a Debian or Ubuntu cloud image in a storage pool and internet access (pkgs.k8s.io, registry.k8s.io).')}</p>
      </section>
    {/each}
  {/if}
</div>

<Dialog open={!!form} title={t('New Kubernetes cluster')} width={640} onclose={() => (form = null)}>
  {#if form}
    <form id="k8s" class="deck-form" onsubmit={(e) => (e.preventDefault(), create())}>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input mono" bind:value={form.name} placeholder="prod" required /></label>
        <label class="ui-field"><span class="label">{t('Kubernetes version')}</span>
          <select class="ui-input" bind:value={form.version}>{#each data?.versions ?? [] as v (v)}<option value={v}>{v}</option>{/each}</select>
        </label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Control planes')}</span>
          <select class="ui-input" bind:value={form.controlPlanes}><option value={1}>1</option><option value={3}>{t('3 (highly available)')}</option></select>
        </label>
        <label class="ui-field"><span class="label">{t('Workers')}</span><input class="ui-input" type="number" min="0" max="50" bind:value={form.workers} /></label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Control plane: vCPUs / MiB / GiB')}</span>
          <span class="row"><input class="ui-input" type="number" min="2" bind:value={form.controlPlane.vcpus} /><input class="ui-input" type="number" min="1800" step="256" bind:value={form.controlPlane.memoryMiB} /><input class="ui-input" type="number" min="10" bind:value={form.controlPlane.diskGiB} /></span>
        </label>
        <label class="ui-field"><span class="label">{t('Worker: vCPUs / MiB / GiB')}</span>
          <span class="row"><input class="ui-input" type="number" min="1" bind:value={form.worker.vcpus} /><input class="ui-input" type="number" min="1024" step="256" bind:value={form.worker.memoryMiB} /><input class="ui-input" type="number" min="10" bind:value={form.worker.diskGiB} /></span>
        </label>
      </div>
      <label class="ui-field"><span class="label">{t('Cloud image')}</span>
        <select class="ui-input" bind:value={form.image}>
          <option value="" disabled>{t('Choose an image…')}</option>
          {#each images as i (i.vol.path)}<option value={i.vol.path}>{i.vol.name} — {i.pool}</option>{/each}
        </select>
        <span class="hint">{t('Debian 12/13 or Ubuntu 22.04 and later (genericcloud images work).')}</span>
      </label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Disks in')}</span>
          <select class="ui-input" bind:value={form.storagePool}><option value="">{t('the default pool')}</option>{#each pools as p (p)}<option value={p}>{p}</option>{/each}</select>
        </label>
        <label class="ui-field"><span class="label">{t('Network')}</span>
          <select class="ui-input" bind:value={form.network}>{#each networks as n (n)}<option value={n}>{n}</option>{/each}</select>
        </label>
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Addresses')}</span>
          <select class="ui-input" bind:value={form.ipPool}><option value="">{t('DHCP')}</option>{#each ipPools as p (p.id)}<option value={p.id}>{t('IP pool {name}', { name: p.name })}</option>{/each}</select>
        </label>
        {#if form.controlPlanes === 3}
          <label class="ui-field"><span class="label">{t('API address (VIP)')}</span><input class="ui-input mono" bind:value={form.vip} placeholder={form.ipPool ? t('taken from the pool') : '10.20.0.10'} /></label>
        {/if}
      </div>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Pod network')}</span>
          <select class="ui-input" bind:value={form.cni}>{#each data?.cnis ?? [] as c (c)}<option value={c}>{c}</option>{/each}</select>
        </label>
        <label class="ui-field"><span class="label">{t('Pod CIDR')}</span><input class="ui-input mono" bind:value={form.podCidr} /></label>
        <label class="ui-field"><span class="label">{t('Service CIDR')}</span><input class="ui-input mono" bind:value={form.serviceCidr} /></label>
      </div>
      {#if cluster.clustered}
        <fieldset class="ui-field">
          <span class="label">{t('Spread the nodes over')}</span>
          {#each cluster.members as m (m.id)}
            <label class="ui-switch"><input type="checkbox" checked={form.hosts?.includes(m.id)} onchange={(e) => (form!.hosts = (e.currentTarget as HTMLInputElement).checked ? [...(form!.hosts ?? []), m.id] : (form!.hosts ?? []).filter((x) => x !== m.id))} /> {m.name}</label>
          {/each}
          <span class="hint">{t('None checked: this host. With several hosts the network must reach across them (a bridge, VXLAN overlay or SDN).')}</span>
        </fieldset>
      {/if}
      <label class="ui-field"><span class="label">{t('SSH keys for the user kube')} <span class="muted">{t('(optional)')}</span></span><textarea class="ui-input mono" rows="2" bind:value={form.keys} placeholder="ssh-ed25519 AAAA…"></textarea></label>
      {#if problem}<p class="problem">{problem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (form = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="k8s" disabled={!!problem}>{t('Create')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!scaleFor} title={t('Workers of {name}', { name: scaleFor?.c.spec.name ?? '' })} width={420} onclose={() => (scaleFor = null)}>
  {#if scaleFor}
    <form id="k8s-scale" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => k8s.scale(scaleFor!.c.id, Number(scaleFor!.workers)), t('Scaling started')).then((ok) => ok && (scaleFor = null)))}>
      <label class="ui-field"><span class="label">{t('Workers')}</span><input class="ui-input" type="number" min="0" max="50" bind:value={scaleFor.workers} />
        <span class="hint">{t('Fewer: the newest workers are drained and their machines deleted. More: new machines join with a fresh token.')}</span>
      </label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (scaleFor = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="k8s-scale">{t('Apply')}</button>
  {/snippet}
</Dialog>

<style>
  .log {
    margin: 8px 0 0;
    padding-left: 0;
    list-style: none;
    max-height: 260px;
    overflow: auto;
  }
  .log li {
    padding: 2px 0;
    white-space: pre-wrap;
  }
  .log li.err {
    color: var(--danger);
  }
  pre.ui-error-box {
    white-space: pre-wrap;
    margin: 6px 0 0;
  }
</style>
