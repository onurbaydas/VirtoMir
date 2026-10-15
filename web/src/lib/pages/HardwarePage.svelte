<script lang="ts">
  // The host's PCI and USB devices: what each is, who uses it, and whether it can be passed
  // through to a machine — with the reason when not.
  import Cpu from '@lucide/svelte/icons/cpu'
  import Usb from '@lucide/svelte/icons/usb'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ShieldAlert from '@lucide/svelte/icons/shield-alert'
  import Layers from '@lucide/svelte/icons/layers'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import { api, errorText, type HostHardware, type HostPCI } from '../api'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'

  let hw = $state<HostHardware | null>(null)
  let error = $state('')
  let showBridges = $state(false)
  const load = () =>
    api
      .hostDevices()
      .then((h) => (hw = h))
      .catch((e) => (error = errorText(e)))
  $effect(() => {
    void load()
  })

  // ---- vGPU slices (mediated devices) --------------------------------------------------
  let cards = $derived((hw?.pci ?? []).filter((p) => p.mdevTypes?.length))
  let mdevs = $derived(hw?.mdevs ?? [])
  /** Chosen type per card address. */
  let pick = $state<Record<string, string>>({})
  let creating = $state('')

  async function createSlice(p: HostPCI) {
    const type = pick[p.address] ?? p.mdevTypes!.find((t) => t.available > 0)?.id
    if (!type) return
    creating = p.address
    try {
      const r = await api.createMDev(p.address, type)
      toasts.push({ kind: 'info', title: 'vGPU slice created', detail: `${r.uuid} — give it to a machine on its Hardware tab (Add other device → vGPU slice).` })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Slice not created', detail: errorText(e) })
    } finally {
      creating = ''
    }
  }

  async function deleteSlice(uuid: string) {
    await api.deleteMDev(uuid)
    toasts.push({ kind: 'info', title: 'vGPU slice deleted' })
    await load()
  }

  // IOMMU groups in order; devices without a group (IOMMU off) form one list.
  let groups = $derived.by(() => {
    const m = new Map<string, HostPCI[]>()
    for (const p of hw?.pci ?? []) {
      if (!showBridges && p.class.toLowerCase().startsWith('0x06')) continue
      const k = p.iommuGroup === undefined ? '' : String(p.iommuGroup)
      m.set(k, [...(m.get(k) ?? []), p])
    }
    return [...m.entries()].sort((a, b) => Number(a[0] || -1) - Number(b[0] || -1))
  })
  let passable = $derived((hw?.pci ?? []).filter((p) => !p.blocked && !p.usedBy.length).length)
</script>

<svelte:head><title>Hardware · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Host hardware</h1>
    <p class="lead">PCI and USB devices of this host, and which of them can be given to a machine (Hardware tab → Add host device).</p>
  </div>
  <label class="ui-switch"><input type="checkbox" bind:checked={showBridges} /> Show bridges</label>
</header>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !hw}
  <p class="muted">Looking at the hardware…</p>
{:else}
  <section class="ui-panel banner" class:ok={hw.iommu}>
    {#if hw.iommu}<ShieldCheck size={20} />{:else}<ShieldAlert size={20} />{/if}
    <div>
      {#if hw.iommu}
        <strong>IOMMU is on</strong>
        <span>Devices are isolated in IOMMU groups. {passable} PCI device{passable === 1 ? '' : 's'} can be passed through right now.</span>
      {:else}
        <strong>IOMMU is off — PCI passthrough is not possible</strong>
        <span>Turn on VT-d (Intel) or AMD-Vi in the firmware settings and add <span class="mono">intel_iommu=on iommu=pt</span> (or <span class="mono">amd_iommu=on</span>) to the kernel command line, then reboot. USB devices work without it.</span>
      {/if}
    </div>
  </section>

  <h2 class="sub"><Cpu size={16} /> PCI devices</h2>
  {#each groups as [g, devs] (g)}
    <section class="ui-panel group">
      {#if g !== ''}<header>IOMMU group {g} <span class="muted">· these go to a machine together</span></header>{/if}
      {#each devs as p (p.address)}
        <div class="dev">
          <span class="dot" class:ok={!p.blocked && !p.usedBy.length} class:used={p.usedBy.length > 0}></span>
          <div class="main">
            <span><b>{p.product || p.productId}</b> <span class="muted">{p.vendor}</span></span>
            <span class="meta mono">{p.address} · {p.className || p.class} · {p.vendorId.replace('0x', '')}:{p.productId.replace('0x', '')}{p.driver ? ` · driver ${p.driver}` : ''}</span>
            {#if p.usedBy.length}<span class="used-by">Given to {p.usedBy.join(', ')}</span>
            {:else if p.blocked && hw.iommu}<span class="why">{p.blocked}</span>{/if}
          </div>
        </div>
      {/each}
    </section>
  {:else}
    <p class="muted">No PCI devices reported.</p>
  {/each}

  {#if cards.length || mdevs.length}
    <h2 class="sub"><Layers size={16} /> vGPU slices</h2>
    <p class="lead sub-lead">Cards whose driver supports mediated devices (NVIDIA vGPU, Intel GVT-g, …) can be split into slices, each given to one machine. Slices start with the host.</p>
    {#each cards as p (p.address)}
      {@const free = p.mdevTypes!.filter((t) => t.available > 0)}
      <section class="ui-panel group">
        <header>{p.product || p.productId} <span class="muted mono">· {p.address}</span></header>
        <div class="slice-new">
          <select class="ui-input" aria-label="Slice type" value={pick[p.address] ?? free[0]?.id ?? ''} onchange={(e) => (pick[p.address] = e.currentTarget.value)} disabled={!free.length}>
            {#each p.mdevTypes! as t (t.id)}
              <option value={t.id} disabled={t.available === 0}>{t.name || t.id} — {t.available} free</option>
            {/each}
          </select>
          <button class="ui-btn sm" disabled={!free.length || creating === p.address || !session.can('admin')} title={session.why('admin') || (free.length ? '' : 'No free slices left on this card')} onclick={() => createSlice(p)}>
            <Plus size={14} /> {creating === p.address ? 'Creating…' : 'Create slice'}
          </button>
        </div>
        {#each mdevs.filter((m) => m.parent === p.address) as m (m.uuid)}
          <div class="dev">
            <span class="dot" class:ok={m.active && !m.usedBy.length} class:used={m.usedBy.length > 0}></span>
            <div class="main">
              <span><b>{m.typeName || m.type}</b> {#if !m.active}<span class="why">not running</span>{/if}</span>
              <span class="meta mono">{m.uuid}{m.autostart ? '' : ' · does not start with the host'}</span>
              {#if m.usedBy.length}<span class="used-by">Given to {m.usedBy.join(', ')}</span>{/if}
            </div>
            <span class="act"><ConfirmButton label="Delete" confirm="Delete this slice?" icon={Trash} failure="Slice not deleted" disabled={m.usedBy.length > 0 || !session.can('admin')} title={m.usedBy.length ? 'Remove it from ' + m.usedBy.join(', ') + ' first' : session.why('admin')} run={() => deleteSlice(m.uuid)} /></span>
          </div>
        {/each}
      </section>
    {/each}
    {@const orphans = mdevs.filter((m) => !cards.some((c) => c.address === m.parent))}
    {#if orphans.length}
      <section class="ui-panel group">
        <header>Other slices</header>
        {#each orphans as m (m.uuid)}
          <div class="dev">
            <span class="dot" class:used={m.usedBy.length > 0}></span>
            <div class="main">
              <span><b>{m.typeName || m.type}</b> <span class="muted">on {m.parent}</span></span>
              <span class="meta mono">{m.uuid}</span>
              {#if m.usedBy.length}<span class="used-by">Given to {m.usedBy.join(', ')}</span>{/if}
            </div>
            <span class="act"><ConfirmButton label="Delete" confirm="Delete this slice?" icon={Trash} failure="Slice not deleted" disabled={m.usedBy.length > 0 || !session.can('admin')} title={session.why('admin')} run={() => deleteSlice(m.uuid)} /></span>
          </div>
        {/each}
      </section>
    {/if}
  {/if}

  <h2 class="sub"><Usb size={16} /> USB devices</h2>
  <section class="ui-panel group">
    {#each hw.usb as u (u.name)}
      <div class="dev">
        <span class="dot" class:ok={!u.blocked && !u.usedBy.length} class:used={u.usedBy.length > 0}></span>
        <div class="main">
          <span><b>{u.product || 'USB device'}</b> <span class="muted">{u.vendor}</span></span>
          <span class="meta mono">bus {u.bus} · device {u.device} · {u.vendorId.replace('0x', '')}:{u.productId.replace('0x', '')}</span>
          {#if u.usedBy.length}<span class="used-by">Given to {u.usedBy.join(', ')}</span>{:else if u.blocked}<span class="why">{u.blocked}</span>{/if}
        </div>
      </div>
    {:else}
      <p class="muted pad">No USB devices are connected.</p>
    {/each}
  </section>
{/if}

<style>
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .banner {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 14px 16px;
    margin-bottom: 18px;
    border-color: color-mix(in srgb, var(--warn) 45%, var(--border));
  }
  .banner :global(svg) {
    color: var(--warn);
    flex: none;
  }
  .banner.ok {
    border-color: var(--border);
  }
  .banner.ok :global(svg) {
    color: var(--ok);
  }
  .banner div {
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 13px;
    color: var(--text-2);
  }
  .banner strong {
    color: var(--text);
  }
  .sub {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 18px 0 10px;
    font-size: 15px;
  }
  .group {
    margin-bottom: 10px;
  }
  .group header {
    padding: 9px 16px;
    border-bottom: 1px solid var(--border);
    font-size: 12.5px;
    font-weight: 600;
  }
  .dev {
    display: flex;
    gap: 12px;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
  }
  .dev:last-child {
    border-bottom: none;
  }
  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    margin-top: 6px;
    border-radius: 50%;
    background: var(--idle);
  }
  .dot.ok {
    background: var(--ok);
  }
  .dot.used {
    background: var(--accent);
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    font-size: 13px;
  }
  .meta {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .why {
    font-size: 12px;
    color: var(--text-3);
  }
  .used-by {
    font-size: 12px;
    color: var(--accent);
  }
  .muted {
    color: var(--text-3);
  }
  .pad {
    padding: 12px 16px;
    margin: 0;
  }
  .sub-lead {
    margin: -4px 0 10px;
  }
  .slice-new {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
  }
  .slice-new select {
    flex: 1;
    min-width: 0;
  }
  .act {
    flex: none;
    margin-left: auto;
    align-self: center;
  }
</style>
