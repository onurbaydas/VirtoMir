<script lang="ts">
  import { t } from '../i18n.svelte'
  // NVMe over TCP (ADR-0074, experimental): discover a storage target's subsystems, connect
  // them (VirtoMir connects them again after a reboot), and make a namespace an LVM pool.
  import '../deck.css'
  import Plus from '@lucide/svelte/icons/plus'
  import Unlink from '@lucide/svelte/icons/unlink'
  import { errorText } from '../api'
  import { formatBytes } from '../format'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'
  import { eff, type NVMeSubsystem, type NVMeTarget } from '../storageeff'

  let { open = $bindable(false), onpool }: { open?: boolean; onpool: (device: string) => void } = $props()
  let admin = $derived(session.can('admin'))

  let subs = $state<NVMeSubsystem[] | null>(null)
  let error = $state('')
  let portal = $state('')
  let port = $state(4420)
  let found = $state<NVMeTarget[] | null>(null)
  let busy = $state(false)

  async function load() {
    try {
      subs = await eff.nvme()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    if (open) void load()
  })

  async function discover(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    try {
      found = await eff.discover(portal.trim(), port || undefined)
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Nothing discovered'), detail: errorText(err) })
    } finally {
      busy = false
    }
  }
  async function connect(tg: NVMeTarget) {
    busy = true
    try {
      await eff.connect(tg)
      toasts.push({ kind: 'info', title: t('Connected {nqn}', { nqn: tg.nqn }) })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not connected'), detail: errorText(e) })
    } finally {
      busy = false
    }
  }
  const connected = (nqn: string) => subs?.some((s) => s.nqn === nqn && s.state === 'live')
</script>

<Dialog bind:open title={t('NVMe over TCP')} subtitle={t('experimental, not tested on hardware')} width={720}>
  <div class="deck">
    {#if error}<div class="ui-error-box">{error}</div>{/if}
    {#if admin}
      <form class="row disc" onsubmit={discover}>
        <label class="ui-field"><span class="label">{t('Target address')}</span><input class="ui-input mono" bind:value={portal} placeholder="192.168.77.50" required /></label>
        <label class="ui-field port"><span class="label">{t('Port')}</span><input class="ui-input" type="number" min="1" max="65535" bind:value={port} /></label>
        <button class="ui-btn" class:busy disabled={busy}>{t('Discover')}</button>
      </form>
      {#if found}
        <table class="t">
          <tbody>
            {#each found as tg (tg.nqn)}
              <tr>
                <td class="mono small">{tg.nqn}</td>
                <td class="mono small">{tg.portal}:{tg.port}</td>
                <td class="right-cell">{#if connected(tg.nqn)}<span class="pill">{t('connected')}</span>{:else}<button class="ui-btn sm" disabled={busy} onclick={() => connect(tg)}><Plus size={13} /> {t('Connect')}</button>{/if}</td>
              </tr>
            {:else}
              <tr><td class="muted">{t('The target offers no NVMe/TCP subsystems.')}</td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/if}
    <h3 class="sub">{t('NVMe subsystems on this host')}</h3>
    {#if subs === null}
      <p class="muted">{t('Loading…')}</p>
    {:else}
      {#each subs as s (s.nqn)}
        <section class="ui-panel card">
          <header class="card-head">
            <div>
              <h3 class="mono small">{s.nqn}</h3>
              <p class="muted small"><span class="pill" class:warn={s.state !== 'live'}>{s.state}</span> {s.transport}{s.address ? ' · ' + s.address : ''}{s.managed ? ' · ' + t('connected by VirtoMir') : ''}</p>
            </div>
            {#if admin && s.transport === 'tcp'}
              <ConfirmButton label={t('Disconnect')} icon={Unlink} confirm={t('Disconnect: its disks vanish from this host')} run={() => eff.disconnect(s.nqn).then(load)} />
            {/if}
          </header>
          <table class="t">
            <tbody>
              {#each s.namespaces as n (n.device)}
                <tr>
                  <td class="mono">{n.device}</td>
                  <td class="mono">{formatBytes(n.sizeBytes)}</td>
                  <td class="muted small">{n.model}</td>
                  <td class="right-cell">{#if admin && s.transport === 'tcp'}<button class="ui-btn sm ghost" onclick={() => onpool(n.device)}>{t('Make an LVM pool')}</button>{/if}</td>
                </tr>
              {:else}
                <tr><td class="muted">{t('No namespaces.')}</td></tr>
              {/each}
            </tbody>
          </table>
        </section>
      {:else}
        <p class="muted">{t('No NVMe subsystems.')}</p>
      {/each}
    {/if}
  </div>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (open = false)}>{t('Close')}</button>
  {/snippet}
</Dialog>

<style>
  .disc {
    align-items: flex-end;
    margin-bottom: 10px;
  }
  .disc .port {
    flex: 0 0 110px;
  }
  .disc button {
    flex: 0 0 auto;
  }
  .sub {
    margin-top: 16px !important;
  }
</style>
