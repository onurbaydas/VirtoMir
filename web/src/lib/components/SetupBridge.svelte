<script lang="ts">
  import { t } from '../i18n.svelte'
  // First-run "bridge to the LAN": machines on the physical network like real computers.
  // Step 1 makes br0 from the host's main port (its address moves to the bridge) through the
  // host network API (ADR-0042), which undoes the change by itself unless this browser keeps
  // it within the confirmation window. Step 2 is a libvirt network on that bridge.
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { api, errorText } from '../api'
  import { ent, type HostInterface, type HostNetPending, type HostNetStatus } from '../enterprise'
  import { toasts } from '../toast.svelte'

  let { ondone }: { ondone: () => void } = $props()

  let ifs = $state<HostInterface[]>([])
  let st = $state<HostNetStatus | null>(null)
  let error = $state('')
  let busy = $state(false)
  async function load() {
    try {
      ;[ifs, st] = await Promise.all([ent.interfaces(), ent.hostNet()])
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  const v4 = (i: HostInterface) => (i.addresses ?? []).filter((a) => a.includes('.') && !a.startsWith('127.'))
  // A bridge that already carries the host's address is ready for step 2.
  let readyBridge = $derived(ifs.find((i) => i.kind === 'bridge' && !i.name.startsWith('virbr') && v4(i).length > 0))
  // The port the host is reached through: the first physical port (or bond) with an address.
  let mainPort = $derived(ifs.find((i) => (i.kind === 'physical' || i.kind === 'bond') && !i.master && v4(i).length > 0))
  let brName = $derived.by(() => {
    let n = 0
    while (ifs.some((i) => i.name === `br${n}`)) n++
    return `br${n}`
  })

  // ---- Step 1: the bridge, with its confirmation countdown ---------------------------------
  let pending = $state<HostNetPending | null>(null)
  let now = $state(Date.now())
  $effect(() => {
    if (!pending) return
    const iv = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(iv)
  })
  let left = $derived(pending ? Math.max(0, Math.round((new Date(pending.deadline).getTime() - now) / 1000)) : 0)

  async function makeBridge() {
    if (!mainPort) return
    busy = true
    error = ''
    try {
      pending = await ent.createHostIf({ kind: 'bridge', name: brName, ports: [mainPort.name], stp: false, takeAddressFrom: mainPort.name })
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
  async function keep() {
    if (!pending) return
    busy = true
    try {
      await ent.confirmHostNet(pending.id)
      pending = null
      toasts.push({ kind: 'info', title: t('Bridge {name} kept', { name: brName }) })
      await load()
    } catch (e) {
      error = t('{e} — if the host cannot be reached any more, it undoes the change by itself when the time is up.', { e: errorText(e) })
    } finally {
      busy = false
    }
  }
  async function undo() {
    if (!pending) return
    try {
      await ent.rollbackHostNet(pending.id)
    } catch (e) {
      error = errorText(e)
    }
    pending = null
    await load()
  }

  // ---- Step 2: a libvirt network on the bridge ---------------------------------------------
  async function makeNetwork(bridge: string) {
    busy = true
    error = ''
    try {
      const taken = new Set((await api.networks()).map((n) => n.name))
      const name = taken.has('lan') ? `lan-${bridge}` : 'lan'
      await api.createNetwork({ name, mode: 'bridge', bridge, dhcp: false })
      await api.networkAutostart(name, true).catch(() => {})
      toasts.push({ kind: 'info', title: t('Network {name} created', { name }), detail: t('Machines on it get their address from your LAN, like real computers.') })
      ondone()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="bridge">
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if !st && !error}
    <p class="muted">{t('Looking at the host\'s network…')}</p>
  {:else if readyBridge && !pending}
    <p>{t('{name} already carries the host\'s address ({addr}). Machines can join the LAN through it.', { name: readyBridge.name, addr: v4(readyBridge).join(', ') })}</p>
    <button class="ui-btn primary" disabled={busy} onclick={() => makeNetwork(readyBridge!.name)}>{t('Create a LAN network on {name}', { name: readyBridge.name })}</button>
  {:else if st && !st.backend}
    <p class="muted">{t('VirtoMir cannot change this host\'s network ({reason}). Create a bridge with your distribution\'s tools, then come back.', { reason: st.reason ?? t('no supported network manager') })}</p>
  {:else if pending}
    <div class="pending" role="alert">
      <TriangleAlert size={16} />
      <span>{t('{name} is up. If you can still read this, keep it — otherwise the host undoes it in {left} s.', { name: brName, left })}</span>
    </div>
    <div class="row">
      <button class="ui-btn primary" disabled={busy || left === 0} onclick={keep}>{t('Keep the bridge')}</button>
      <button class="ui-btn ghost" disabled={busy} onclick={undo}>{t('Undo')}</button>
    </div>
  {:else if mainPort}
    <p>{t('Makes {bridge} from {port} ({addr}) and moves the host\'s address onto it. The connection may drop for a moment; if this page cannot confirm the change within two minutes, the host undoes it by itself.', { bridge: brName, port: mainPort.name, addr: v4(mainPort).join(', ') })}</p>
    <button class="ui-btn primary" disabled={busy} onclick={makeBridge}>{t('Create {bridge} from {port}', { bridge: brName, port: mainPort.name })}</button>
  {:else}
    <p class="muted">{t('No network port with an address was found to build a bridge on.')}</p>
  {/if}
</div>

<style>
  .bridge {
    display: grid;
    gap: 8px;
    margin-top: 6px;
    font-size: 13px;
  }
  .bridge p {
    margin: 0;
    color: var(--text-2);
  }
  .muted {
    color: var(--text-3);
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .pending {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--warn);
    background: color-mix(in srgb, var(--warn) 12%, transparent);
  }
</style>
