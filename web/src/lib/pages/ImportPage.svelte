<script lang="ts">
  import { t } from '../i18n.svelte'
  // Bring machines in from elsewhere: an OVA appliance (VMware, VirtualBox, …), a libvirt
  // domain XML, or a disk image that is already on the host (the new-machine wizard).
  import PackageOpen from '@lucide/svelte/icons/package-open'
  import FileCode from '@lucide/svelte/icons/file-code'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Server from '@lucide/svelte/icons/server'
  import { api, errorText, type Pool } from '../api'
  import { activePools } from '../media'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let pools = $state<Pool[]>([])
  let pool = $state('')
  $effect(() => {
    activePools()
      .then((p) => {
        pools = p.filter((x) => x.type === 'dir' || x.type === 'netfs')
        pool = (pools.find((x) => x.name === 'default') ?? pools[0])?.name ?? ''
        poolErr = pools.length ? '' : t('No started directory or NFS pool to import into. Create or start one under Storage first.')
      })
      .catch((e) => (poolErr = errorText(e)))
  })
  let poolErr = $state('')

  // ---- OVA --------------------------------------------------------------------------------
  let file = $state<File | null>(null)
  let ovaName = $state('')
  let progress = $state<number | null>(null)
  let ovaErr = $state('')
  let abort: AbortController | null = null
  async function importOVA(e: SubmitEvent) {
    e.preventDefault()
    if (!file) return
    ovaErr = ''
    progress = 0
    abort = new AbortController()
    try {
      const r = await api.importOVA(pool, file, ovaName.trim(), (f) => (progress = f), abort.signal)
      toasts.push({ kind: 'info', title: t('Appliance imported'), detail: t('Check its settings, then start it.') })
      router.navigate(machineHref(r.uuid))
    } catch (err) {
      ovaErr = errorText(err)
    } finally {
      progress = null
      abort = null
    }
  }

  // ---- XML --------------------------------------------------------------------------------
  let xml = $state('')
  let xmlErr = $state('')
  let xmlBusy = $state(false)
  async function importXML(e: SubmitEvent) {
    e.preventDefault()
    xmlErr = ''
    xmlBusy = true
    try {
      const r = await api.importXML(xml)
      toasts.push({ kind: 'info', title: t('Machine defined') })
      router.navigate(machineHref(r.uuid))
    } catch (err) {
      xmlErr = errorText(err)
    } finally {
      xmlBusy = false
    }
  }
  const admin = $derived(session.can('admin'))
</script>

<svelte:head><title>{t('Import · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Import a machine')}</h1>
    <p class="lead">{t('Bring a machine over from VMware, VirtualBox or another libvirt host.')}</p>
  </div>
</header>

{#if !admin}<div class="ui-error-box">{session.why('admin')}</div>{/if}
{#if poolErr}<div class="ui-error-box">{poolErr}</div>{/if}

<div class="cards">
  <section class="ui-panel card">
    <h2><PackageOpen size={16} /> {t('OVA appliance')}</h2>
    <p class="hint">{t('An')} <span class="mono">.ova</span> {t('file exported from VMware, VirtualBox or a vendor. Its disks are converted to qcow2; processors, memory, firmware and the guest OS are taken over.')}</p>
    <form class="form" onsubmit={importOVA}>
      <label class="ui-field">
        <span class="label">{t('File')}</span>
        <input class="ui-input" type="file" accept=".ova" onchange={(e) => (file = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
      </label>
      <div class="row2">
        <label class="ui-field">
          <span class="label">{t('Name')} <span class="opt">{t('(optional)')}</span></span>
          <input class="ui-input" bind:value={ovaName} placeholder={t('from the appliance')} pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" />
        </label>
        <label class="ui-field">
          <span class="label">{t('Disks go to')}</span>
          <select class="ui-input" bind:value={pool}>
            {#each pools as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </label>
      </div>
      {#if progress !== null}
        <div class="bar" title={t('Uploading')}><span style:width="{progress * 100}%"></span></div>
        <span class="hint">{progress < 1 ? t('Uploading… {pct} %', { pct: Math.round(progress * 100) }) : t('Converting the disks…')}</span>
      {/if}
      {#if ovaErr}<div class="ui-error-box">{ovaErr}</div>{/if}
      <div class="actions">
        {#if progress !== null && progress < 1}<button type="button" class="ui-btn ghost" onclick={() => abort?.abort()}>{t('Cancel')}</button>{/if}
        <button class="ui-btn primary" disabled={!admin || !file || !pool || progress !== null}>{t('Import')}</button>
      </div>
    </form>
  </section>

  <section class="ui-panel card">
    <h2><FileCode size={16} /> {t('libvirt XML')}</h2>
    <p class="hint">{t('A domain definition, e.g. from')} <span class="mono">{t('virsh dumpxml')}</span> {t('on another host. Its disks must already be at the paths it names.')}</p>
    <form class="form" onsubmit={importXML}>
      <textarea class="ui-input mono" rows="9" bind:value={xml} placeholder="<domain type='kvm'>…" spellcheck="false" aria-label={t('Domain XML')}></textarea>
      {#if xmlErr}<div class="ui-error-box">{xmlErr}</div>{/if}
      <div class="actions">
        <button class="ui-btn primary" disabled={!admin || !xml.trim().startsWith('<') || xmlBusy}>{t('Define machine')}</button>
      </div>
    </form>
  </section>

  <section class="ui-panel card">
    <h2><HardDrive size={16} /> {t('Universal disk image')}</h2>
    <p class="hint">{t('VirtoMir automatically sniffs image headers and supports all major virtualization formats. Place images into a storage pool or upload via the Storage deck, then wrap a virtual machine around it.')}</p>
    <div class="supported-badges">
      <span class="fmt-pill">QCOW2</span>
      <span class="fmt-pill">VMDK (VMware)</span>
      <span class="fmt-pill">VHDX (Hyper-V)</span>
      <span class="fmt-pill">VHD</span>
      <span class="fmt-pill">VDI (VirtualBox)</span>
      <span class="fmt-pill">RAW / IMG</span>
    </div>
    <div class="actions">
      <a class="ui-btn" href={router.scoped('/machines/new')} onclick={router.link}>{t('Open the new-machine wizard')}</a>
    </div>
  </section>

  <section class="ui-panel card">
    <h2><Server size={16} /> {t('VMware vCenter / ESXi')}</h2>
    <p class="hint">{t('Connect to a vCenter or standalone ESXi host. Inspect inventory, map storage and networks, and migrate virtual machines directly.')}</p>
    <div class="actions">
      <a class="ui-btn" href={router.scoped('/machines/import/vmware')} onclick={router.link}>{t('Start VMware migration')}</a>
    </div>
  </section>
</div>

<style>
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
    gap: 14px;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 18px;
  }
  .card h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
  }
  .card h2 :global(svg) {
    color: var(--accent);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: auto;
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .opt {
    color: var(--text-3);
    font-weight: 400;
  }
  textarea {
    resize: vertical;
    font-size: 12px;
  }
  .bar {
    height: 6px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.3s;
  }
  .supported-badges {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 4px 0 8px;
  }
  .fmt-pill {
    display: inline-flex;
    align-items: center;
    padding: 3px 8px;
    font-size: 11px;
    font-family: var(--font-mono, monospace);
    font-weight: 600;
    border-radius: var(--radius-sm, 4px);
    background: var(--surface-2);
    color: var(--text-2);
    border: 1px solid var(--border);
  }
</style>
