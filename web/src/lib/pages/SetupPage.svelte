<script lang="ts">
  import { t } from '../i18n.svelte'
  // First-run setup (ADR-0045): what this host needs before the first machine, each with a
  // one-click fix. Reachable again from Host settings.
  import Check from '@lucide/svelte/icons/check'
  import CircleAlert from '@lucide/svelte/icons/circle-alert'
  import Info from '@lucide/svelte/icons/info'
  import { api, errorText, type Pool, type VNetwork } from '../api'
  import { ent } from '../enterprise'
  import { track } from '../jobs'
  import { router } from '../router.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import SetupBridge from '../components/SetupBridge.svelte'

  interface SetupCheck {
    id: string
    ok: boolean
    needed: boolean
    title: string
    detail: string
  }
  let checks = $state<SetupCheck[] | null>(null)
  let arch = $state('')
  let backupDir = $state('')
  let error = $state('')
  let pools = $state<Pool[]>([])

  async function load() {
    try {
      const s = await api.get<{ done: boolean; arch: string; backupDir?: string; checks: SetupCheck[] }>('/api/v1/setup')
      checks = s.checks
      arch = s.arch
      if (!backupDir) backupDir = s.backupDir ?? ''
      pools = (await api.pools()).filter((p) => p.active && ['dir', 'netfs', 'fs'].includes(p.type))
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })
  const check = (id: string) => checks?.find((c) => c.id === id)
  let ready = $derived(!!checks && checks.filter((c) => c.needed).every((c) => c.ok))

  let busy = $state('')
  async function step(id: string, fn: () => Promise<unknown>, done: string) {
    busy = id
    try {
      await fn()
      toasts.push({ kind: 'info', title: done })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('That did not work'), detail: errorText(e) })
    } finally {
      busy = ''
    }
  }

  // ---- fixes --------------------------------------------------------------------------
  let poolPath = $state('/var/lib/libvirt/images')
  // A pool or network that exists but is stopped (Debian and Ubuntu ship libvirt's
  // "default" network that way) is started and set to autostart instead of adding another.
  let stoppedPool = $state<Pool | null>(null)
  let stoppedNet = $state<VNetwork | null>(null)
  $effect(() => {
    void checks
    api.pools().then((p) => (stoppedPool = p.find((x) => !x.active && x.type === 'dir') ?? null)).catch(() => {})
    api
      .networks()
      .then((n) => (stoppedNet = n.find((x) => !x.active && x.mode !== 'isolated' && x.name === 'default') ?? n.find((x) => !x.active && x.mode !== 'isolated') ?? null))
      .catch(() => {})
  })
  const startPool = (p: Pool) =>
    step('pool', async () => {
      await api.poolAction(p.name, 'start')
      await api.poolAutostart(p.name, true)
    }, t('Pool {name} started', { name: p.name }))
  const startNetwork = (n: VNetwork) =>
    step('network', async () => {
      await api.networkAction(n.name, 'start')
      await api.networkAutostart(n.name, true)
    }, t('Network {name} started', { name: n.name }))

  const createPool = () =>
    step('pool', async () => {
      const taken = new Set((await api.pools()).map((p) => p.name))
      await api.createPool({ name: taken.has('default') ? 'images' : 'default', path: poolPath.trim() })
    }, t('Storage pool created'))

  let bridgeOpen = $state(false)
  const createNetwork = () =>
    step('network', async () => {
      const nets = await api.networks()
      const used = new Set(nets.map((n) => n.subnet?.split('.').slice(0, 3).join('.')))
      let x = 122
      while (used.has(`192.168.${x}`) && x < 250) x++
      const name = nets.some((n) => n.name === 'default') ? 'nat' : 'default'
      await api.createNetwork({ name, mode: 'nat', subnet: `192.168.${x}.0/24`, dhcp: true })
    }, t('NAT network created'))

  const saveBackups = () => step('backups', () => api.saveBackupSettings(backupDir.trim()), t('Backup location saved'))

  // A first image: a cloud image boots in seconds with cloud-init (user, password, SSH key).
  let x86 = $derived(!/aarch64|arm/i.test(arch))
  let images = $derived([
    {
      label: t('Debian 13 (trixie) cloud image'),
      url: x86 ? 'https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2' : 'https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2',
    },
    {
      label: t('Ubuntu 24.04 LTS cloud image'),
      url: x86 ? 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img' : 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-arm64.img',
    },
  ])
  let imagePool = $derived(pools[0]?.name ?? '')
  async function download(url: string) {
    try {
      const name = url.split('/').pop()!.replace(/\.img$/, '.qcow2')
      const job = await ent.download(imagePool, url, name, '')
      track(job, { success: `${name} is ready in ${imagePool}` })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Download not started'), detail: errorText(e) })
    }
  }

  async function finish(to: string) {
    try {
      await api.post('/api/v1/setup/done')
    } catch {
      /* not fatal: the page simply shows again next time */
    }
    router.navigate(to)
  }
</script>

<svelte:head><title>{t('Setup · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Welcome to VirtoMir')}</h1>
    <p class="lead">{t('A few things this host needs before the first machine. Each can be fixed from here; you can also skip and do it later.')}</p>
  </div>
</header>

{#if !session.can('admin')}
  <p class="muted">{t('Only admins set up a host.')}</p>
{:else if error}
  <div class="ui-error-box">{error}</div>
{:else if !checks}
  <p class="muted">{t('Looking at this host…')}</p>
{:else}
  <ol class="steps">
    {#each checks as c (c.id)}
      <li class="ui-panel step" class:ok={c.ok} class:needed={!c.ok && c.needed}>
        <span class="mark">
          {#if c.ok}<Check size={18} />{:else if c.needed}<CircleAlert size={18} />{:else}<Info size={18} />{/if}
        </span>
        <div class="body">
          <b>{c.title}</b>
          <span class="detail">{c.detail}</span>
          {#if c.id === 'pool' && !c.ok && stoppedPool}
            <div class="fix">
              <button class="ui-btn primary" disabled={busy === 'pool'} onclick={() => startPool(stoppedPool!)}>{t('Start {name} and start it with the host', { name: stoppedPool.name })}</button>
            </div>
          {:else if c.id === 'pool' && !c.ok}
            <div class="fix">
              <input class="ui-input mono" bind:value={poolPath} aria-label={t('Directory for disks and images')} />
              <button class="ui-btn primary" disabled={busy === 'pool'} onclick={createPool}>{busy === 'pool' ? t('Creating…') : t('Create pool')}</button>
            </div>
          {:else if c.id === 'network' && !c.ok && stoppedNet}
            <div class="fix">
              <button class="ui-btn primary" disabled={busy === 'network'} onclick={() => startNetwork(stoppedNet!)}>{t('Start {name} and start it with the host', { name: stoppedNet.name })}</button>
              <button class="ui-btn ghost" onclick={() => (bridgeOpen = !bridgeOpen)} aria-expanded={bridgeOpen}>{t('Bridge to the LAN instead…')}</button>
            </div>
          {:else if c.id === 'network' && !c.ok}
            <div class="fix">
              <button class="ui-btn primary" disabled={busy === 'network'} onclick={createNetwork}>{busy === 'network' ? t('Creating…') : t('Create a NAT network')}</button>
              <button class="ui-btn ghost" onclick={() => (bridgeOpen = !bridgeOpen)} aria-expanded={bridgeOpen}>{t('Bridge to the LAN instead…')}</button>
            </div>
          {:else if c.id === 'network' && c.ok && !bridgeOpen}
            <div class="fix"><button class="ui-btn ghost" onclick={() => (bridgeOpen = true)}>{t('Also bridge machines to the LAN…')}</button></div>
          {/if}
          {#if c.id === 'network' && bridgeOpen}
            <SetupBridge ondone={() => ((bridgeOpen = false), void load())} />
          {/if}
          {#if c.id === 'backups'}
            <div class="fix">
              <input class="ui-input mono" bind:value={backupDir} aria-label={t('Backup location')} />
              <button class="ui-btn" disabled={busy === 'backups' || !backupDir.trim()} onclick={saveBackups}>{t('Save')}</button>
            </div>
          {:else if c.id === 'offsite' && c.ok}
            <div class="fix"><a class="ui-btn ghost" href="/backups" onclick={router.link}>{t('Set up off-site copies…')}</a></div>
          {/if}
        </div>
      </li>
    {/each}
    {#if check('pool')?.ok}
      <li class="ui-panel step">
        <span class="mark"><Info size={18} /></span>
        <div class="body">
          <b>{t('Something to start from')}</b>
          <span class="detail">{t('Download a cloud image into {imagePool} (the host fetches it; nothing passes through this browser), or upload an installer ISO on the Storage page.', { imagePool: imagePool })}</span>
          <div class="fix">
            {#each images as img (img.url)}<button class="ui-btn" onclick={() => download(img.url)}>{img.label}</button>{/each}
            <a class="ui-btn ghost" href="/storage" onclick={router.link}>{t('Upload an ISO…')}</a>
          </div>
        </div>
      </li>
    {/if}
  </ol>
  <div class="end">
    <button class="ui-btn primary" disabled={!ready} title={ready ? '' : t('Fix the red items first')} onclick={() => finish('/machines/new')}>{t('Create the first machine')}</button>
    <button class="ui-btn ghost" onclick={() => finish('/')}>{ready ? 'Done' : t('Skip for now')}</button>
  </div>
{/if}

<style>
  .lead { margin: 0; color: var(--text-3); font-size: 13.5px; }
  .muted { color: var(--text-3); }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; max-width: 820px; }
  .step { display: flex; gap: 12px; padding: 14px 16px; border-left: 4px solid var(--border); }
  .step.ok { border-left-color: var(--ok, var(--accent)); }
  .step.needed { border-left-color: var(--danger, var(--warn)); }
  .mark { flex: none; margin-top: 1px; color: var(--text-3); }
  .ok .mark { color: var(--ok, var(--accent)); }
  .needed .mark { color: var(--danger, var(--warn)); }
  .body { display: flex; flex-direction: column; gap: 4px; flex: 1; min-width: 0; }
  .detail { color: var(--text-2); font-size: 13px; }
  .fix { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 6px; }
  .fix input { flex: 1; min-width: 200px; max-width: 420px; }
  .end { display: flex; gap: 10px; margin: 18px 0; }
</style>
