<script lang="ts">
  import { t } from '../i18n.svelte'
  // Bare-metal onboarding (ADR-0077): register a machine by the MAC it boots from, then
  // VirtoMir installs Debian 13 and VirtoMir on it over the network and it joins the cluster.
  import '../deck.css'
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import Download from '@lucide/svelte/icons/download'
  import Power from '@lucide/svelte/icons/power'
  import { errorText } from '../api'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import Dialog from '../components/Dialog.svelte'
  import { baremetal, hostProblem, moving, type BMHost, type BMView, type HostInput, type NetbootSettings } from '../baremetal'

  let admin = $derived(session.can('admin'))
  let data = $state<BMView | null>(null)
  let error = $state('')
  let open = $state<Record<string, boolean>>({})

  const load = async () => {
    try {
      data = await baremetal.get()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void router.host
    void load()
  })
  $effect(() => {
    if (!data?.hosts.some(moving)) return
    const id = setInterval(() => void load(), 10_000)
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

  const stateClass = (s: string) => (s === 'joined' ? '' : s === 'failed' ? 'drop' : s === 'registered' ? 'muted' : 'warn')
  const stateText = (s: string) =>
    ({
      registered: t('registered'),
      installing: t('installing'),
      installed: t('installed'),
      joining: t('joining'),
      joined: t('in the cluster'),
      failed: t('failed'),
    })[s] ?? s

  // ---- settings ---------------------------------------------------------------------------
  let setForm = $state<NetbootSettings | null>(null)
  let fetching = $state(false)
  async function fetchInstaller() {
    fetching = true
    await act(() => baremetal.fetchInstaller(), t('Installer downloaded'))
    fetching = false
  }

  // ---- hosts ------------------------------------------------------------------------------
  type Form = HostInput & { id?: string; keys: string; useStatic: boolean; dnsText: string }
  let form = $state<Form | null>(null)
  function newHost() {
    form = { name: '', mac: '', bmc: { driver: '' }, disk: '', keys: '', password: '', useStatic: false, static: { address: '', gateway: '' }, dnsText: '' }
  }
  function editHost(h: BMHost) {
    form = {
      id: h.id, name: h.name, mac: h.mac, bmc: { ...h.bmc, password: '' }, disk: h.disk ?? '', keys: (h.sshKeys ?? []).join('\n'), password: '',
      useStatic: !!h.static, static: { address: h.static?.address ?? '', gateway: h.static?.gateway ?? '' }, dnsText: (h.static?.dns ?? []).join(', '),
    }
  }
  let body = $derived<HostInput | null>(
    form
      ? {
          name: form.name.trim(), mac: form.mac.trim(), bmc: form.bmc, disk: form.disk || undefined, password: form.password || undefined,
          sshKeys: form.keys.split('\n').map((k) => k.trim()).filter(Boolean),
          static: form.useStatic ? { address: form.static!.address, gateway: form.static!.gateway || undefined, dns: form.dnsText.split(/[\s,]+/).filter(Boolean) } : undefined,
        }
      : null,
  )
  const problems: Record<string, string> = {
    name: t('The host name is up to 63 lower-case letters, digits and dashes.'),
    mac: t('Give the MAC address of the network card the machine boots from.'),
    access: t('Set a password or an SSH key for the user virtomir.'),
    password: t('The password has at least 8 characters.'),
    bmc: t('Give the BMC’s address.'),
  }
  let problem = $derived(form && body ? (problems[hostProblem(body, !form.id)] ?? '') : '')
  async function save() {
    if (!form || !body || problem) return
    const id = form.id
    if (await act(() => (id ? baremetal.update(id, body!) : baremetal.add(body!)), t('Host saved'))) form = null
  }
  async function power(h: BMHost, action: 'status' | 'off' | 'reboot' | 'pxe') {
    try {
      const r = await baremetal.power(h.id, action)
      toasts.push({ kind: 'info', title: r.message })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('The BMC did not do it'), detail: errorText(e) })
    } finally {
      await load()
    }
  }
</script>

<svelte:head><title>{t('Bare metal · VirtoMir')}</title></svelte:head>

<div class="deck">
  <header class="page-head">
    <div>
      <h1>{t('Bare metal')}</h1>
      <p class="lead">{t('New hosts from zero: register a machine by the MAC address it boots from, and VirtoMir installs Debian 13 and VirtoMir on it over the network. On its first boot it joins this cluster by itself.')}</p>
    </div>
    {#if admin && data?.enabled}<button class="ui-btn primary" onclick={newHost}><Plus size={14} /> {t('Register a host')}</button>{/if}
  </header>

  {#if error}<div class="ui-error-box">{error}</div>{/if}

  {#if data === null}
    <p class="muted">{t('Loading…')}</p>
  {:else if !data.enabled}
    <section class="ui-panel card"><p class="muted">{t('Bare-metal onboarding is not available on this host.')}</p></section>
  {:else}
    <section class="ui-panel card">
      <header class="card-head">
        <div>
          <h3>{t('Network boot')} <span class="pill" class:drop={!data.status.listening}>{data.settings.enabled ? (data.status.listening ? t('serving') : t('not serving')) : t('off')}</span></h3>
          <p class="muted small">
            {#if data.settings.enabled}<span class="mono">{`http://${data.settings.address}:${data.settings.port}/boot/ipxe`}</span> · {data.settings.mode === 'proxy' ? t('proxy DHCP (experimental)') : t('your DHCP server points machines here')}{:else}{t('Turn it on to install hosts.')}{/if}
            · {data.status.installerReady ? t('Debian 13 installer ready') : t('Debian 13 installer not downloaded')}
          </p>
          {#if data.status.error}<p class="problem small">{data.status.error}</p>{/if}
        </div>
        {#if admin}
          <div class="right">
            <button class="ui-btn sm" class:busy={fetching} disabled={fetching} onclick={fetchInstaller}><Download size={13} /> {data.status.installerReady ? t('Download again') : t('Download the installer')}</button>
            <button class="ui-btn sm ghost" aria-label={t('Settings')} onclick={() => (setForm = { ...data!.settings })}><Pencil size={13} /></button>
          </div>
        {/if}
      </header>
      {#if data.settings.enabled && data.settings.mode === 'external'}
        <p class="hint">{t('On your DHCP server, give the machines the boot file name {url} (iPXE, which QEMU and most network cards use). Firmware without iPXE needs proxy DHCP.', { url: `http://${data.settings.address}:${data.settings.port}/boot/ipxe` })}</p>
      {/if}
    </section>

    {#each data.hosts as h (h.id)}
      <section class="ui-panel card">
        <header class="card-head">
          <div>
            <h3>{h.name} <span class="pill {stateClass(h.state)}">{stateText(h.state)}</span> <span class="mono small muted">{h.mac}</span></h3>
            <p class="muted small">
              {h.disk ? t('installs on {disk}', { disk: h.disk }) : t('installs on its first disk')}
              · {h.static ? h.static.address : t('DHCP')}
              · {h.bmc.driver ? `${h.bmc.driver} ${h.bmc.endpoint}` : t('no BMC')}
            </p>
            {#if h.message}<p class="problem small">{h.message}</p>{/if}
          </div>
          {#if admin}
            <div class="right">
              {#if h.state === 'installing'}
                <button class="ui-btn sm" onclick={() => act(() => baremetal.cancel(h.id), t('Installation cancelled'))}>{t('Cancel')}</button>
              {:else if h.state !== 'joined'}
                <ConfirmButton label={t('Install')} confirm={t('Erase its disk and install it')} run={() => act(() => baremetal.install(h.id), t('Installation armed'))} />
              {/if}
              {#if h.bmc.driver}
                <button class="ui-btn sm ghost" aria-label={t('Power status')} title={t('Power status')} onclick={() => power(h, 'status')}><Power size={13} /></button>
                <ConfirmButton label={t('Reboot')} confirm={t('Power cycle it')} run={() => power(h, 'reboot')} />
              {/if}
              {#if h.state !== 'installing' && h.state !== 'joining'}<button class="ui-btn sm ghost" aria-label={t('Edit')} onclick={() => editHost(h)}><Pencil size={13} /></button>{/if}
              <ConfirmButton label={t('Forget')} icon={Trash} confirm={t('Forget this host (the machine is not touched)')} run={() => act(() => baremetal.delete(h.id), t('Host forgotten'))} />
            </div>
          {/if}
        </header>
        <button class="ui-btn sm ghost" onclick={() => (open[h.id] = !open[h.id])}>{open[h.id] ? t('Hide the log') : t('Show the log ({n})', { n: h.events.length })}</button>
        {#if open[h.id]}
          <ol class="log small">
            {#each [...h.events].reverse() as e, i (i)}
              <li class:err={e.error}><span class="mono muted">{new Date(e.at).toLocaleString()}</span> {e.message}</li>
            {/each}
          </ol>
        {/if}
      </section>
    {:else}
      <section class="ui-panel card"><p class="muted">{t('No hosts registered yet.')}</p></section>
    {/each}
  {/if}
</div>

<Dialog open={!!setForm} title={t('Network boot')} width={560} onclose={() => (setForm = null)}>
  {#if setForm}
    <form id="netboot" class="deck-form" onsubmit={(e) => (e.preventDefault(), act(() => baremetal.setSettings({ ...setForm!, port: Number(setForm!.port) }), t('Settings saved')).then((ok) => ok && (setForm = null)))}>
      <label class="ui-switch"><input type="checkbox" bind:checked={setForm.enabled} /> {t('Serve network boots from this host')}</label>
      <div class="row">
        <label class="ui-field"><span class="label">{t('This host’s address on the boot network')}</span><input class="ui-input mono" bind:value={setForm.address} placeholder="10.0.0.11" /></label>
        <label class="ui-field"><span class="label">{t('HTTP port')}</span><input class="ui-input" type="number" min="1" max="65535" bind:value={setForm.port} /></label>
      </div>
      <label class="ui-field"><span class="label">{t('How machines find it')}</span>
        <select class="ui-input" bind:value={setForm.mode}>
          <option value="external">{t('My DHCP server gives them the boot file name')}</option>
          <option value="proxy">{t('Proxy DHCP from this host (experimental)')}</option>
        </select>
      </label>
      {#if setForm.mode === 'proxy'}
        <label class="ui-field"><span class="label">{t('Interface of the boot network')}</span><input class="ui-input mono" bind:value={setForm.interface} placeholder="br0" /><span class="hint">{t('Needs dnsmasq and iPXE (apt install ipxe). It answers only the machines being installed; your DHCP server still hands out the addresses.')}</span></label>
      {/if}
      <div class="row">
        <label class="ui-field"><span class="label">{t('Debian mirror')}</span><input class="ui-input mono" bind:value={setForm.mirror} /></label>
      </div>
      <label class="ui-field"><span class="label">{t('VirtoMir package repository')}</span><input class="ui-input mono" bind:value={setForm.repo} /></label>
      <p class="hint">{t('The boot files are served over plain HTTP, as iPXE does not trust self-signed certificates: use it on a network you control.')}</p>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (setForm = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="netboot">{t('Save')}</button>
  {/snippet}
</Dialog>

<Dialog open={!!form} title={form?.id ? t('Edit {name}', { name: form.name }) : t('Register a host')} width={600} onclose={() => (form = null)}>
  {#if form}
    <form id="bmhost" class="deck-form" onsubmit={(e) => (e.preventDefault(), save())}>
      <div class="row">
        <label class="ui-field"><span class="label">{t('Host name')}</span><input class="ui-input mono" bind:value={form.name} placeholder="hv3" required /></label>
        <label class="ui-field"><span class="label">{t('MAC of its boot network card')}</span><input class="ui-input mono" bind:value={form.mac} placeholder="3c:ec:ef:12:34:56" required /></label>
      </div>
      <label class="ui-field"><span class="label">{t('System disk')} <span class="muted">{t('(optional)')}</span></span><input class="ui-input mono" bind:value={form.disk} placeholder={t('the first disk')} /><span class="hint">{t('Everything on it is erased.')}</span></label>
      <label class="ui-switch"><input type="checkbox" bind:checked={form.useStatic} /> {t('Fixed address once installed')}</label>
      {#if form.useStatic && form.static}
        <div class="row">
          <label class="ui-field"><span class="label">{t('Address')}</span><input class="ui-input mono" bind:value={form.static.address} placeholder="10.0.0.21/24" /></label>
          <label class="ui-field"><span class="label">{t('Gateway')}</span><input class="ui-input mono" bind:value={form.static.gateway} placeholder="10.0.0.1" /></label>
          <label class="ui-field"><span class="label">{t('DNS servers')}</span><input class="ui-input mono" bind:value={form.dnsText} placeholder="10.0.0.53" /></label>
        </div>
      {/if}
      <div class="row">
        <label class="ui-field"><span class="label">{t('BMC')}</span>
          <select class="ui-input" bind:value={form.bmc.driver}><option value="">{t('none: I boot it myself')}</option><option value="redfish">Redfish</option><option value="ipmi">IPMI</option></select>
        </label>
        {#if form.bmc.driver}
          <label class="ui-field"><span class="label">{t('BMC address')}</span><input class="ui-input mono" bind:value={form.bmc.endpoint} placeholder="10.0.0.103" /></label>
        {/if}
      </div>
      {#if form.bmc.driver}
        <div class="row">
          <label class="ui-field"><span class="label">{t('BMC user')}</span><input class="ui-input mono" bind:value={form.bmc.username} /></label>
          <label class="ui-field"><span class="label">{t('BMC password')}</span><input class="ui-input mono" type="password" bind:value={form.bmc.password} placeholder={form.id ? t('stored; leave empty to keep it') : ''} autocomplete="off" /></label>
        </div>
        {#if form.bmc.driver === 'redfish'}<label class="ui-switch"><input type="checkbox" bind:checked={form.bmc.insecureTls} /> {t('Accept the BMC’s self-signed certificate')}</label>{/if}
      {/if}
      <label class="ui-field"><span class="label">{t('Password of the user virtomir')}</span><input class="ui-input mono" type="password" bind:value={form.password} placeholder={form.id ? t('stored; leave empty to keep it') : ''} autocomplete="new-password" /><span class="hint">{t('The user virtomir is an admin of the new host (sudo, and VirtoMir through the libvirt group); root has no password.')}</span></label>
      <label class="ui-field"><span class="label">{t('SSH keys of the user virtomir')} <span class="muted">{t('(optional)')}</span></span><textarea class="ui-input mono" rows="2" bind:value={form.keys} placeholder="ssh-ed25519 AAAA…"></textarea></label>
      {#if problem}<p class="problem">{problem}</p>{/if}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (form = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="bmhost" disabled={!!problem}>{t('Save')}</button>
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
  }
  .log li.err {
    color: var(--danger);
  }
</style>
