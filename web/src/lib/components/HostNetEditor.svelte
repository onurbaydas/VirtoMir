<script lang="ts">
  import { t } from '../i18n.svelte'
  // Bridges, bonds and VLAN interfaces on the host (ADR-0042). A change is applied at once and
  // must be kept within the confirmation window from this browser: if the host can no longer be
  // reached (wrong port, address moved), nobody can confirm and the host undoes it by itself.
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { errorText } from '../api'
  import { ent, type HostIfSpec, type HostInterface, type HostNetPending, type HostNetStatus } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  let { ifs, onchanged }: { ifs: HostInterface[]; onchanged: () => void } = $props()

  let admin = $derived(session.can('admin'))
  let st = $state<HostNetStatus | null>(null)
  let err = $state('')
  async function load() {
    try {
      st = await ent.hostNet()
      err = ''
    } catch (e) {
      st = null
      err = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  // ---- Pending change with its countdown -------------------------------------------------
  let now = $state(Date.now())
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
  let pending = $derived(st?.pending)
  let left = $derived(pending ? Math.max(0, Math.round((new Date(pending.deadline).getTime() - now) / 1000)) : 0)
  $effect(() => {
    // After the deadline the host has rolled back by itself: show that.
    if (pending && left === 0) {
      const t = setTimeout(async () => {
        await load()
        onchanged()
      }, 3000)
      return () => clearTimeout(t)
    }
  })

  let keepBusy = $state(false)
  let keepErr = $state('')
  async function keep(p: HostNetPending) {
    keepBusy = true
    keepErr = ''
    try {
      st = await ent.confirmHostNet(p.id)
      toasts.push({ kind: 'info', title: t('Network change kept'), detail: p.what })
      onchanged()
    } catch (e) {
      keepErr = t('{e} — if the host cannot be reached any more, it undoes the change by itself when the time is up.', { e: errorText(e) })
    } finally {
      keepBusy = false
    }
  }
  async function undo(p: HostNetPending) {
    try {
      st = await ent.rollbackHostNet(p.id)
      toasts.push({ kind: 'info', title: t('Network change undone'), detail: p.what })
      onchanged()
    } catch (e) {
      keepErr = errorText(e)
    }
  }

  async function started(p: Promise<HostNetPending>) {
    const res = await p
    await load()
    onchanged()
    return res
  }

  // ---- New interface ---------------------------------------------------------------------
  let dlg = $state(false)
  let spec = $state<HostIfSpec>({ kind: 'bridge', name: 'br0', ports: [], stp: false })
  let busy = $state(false)
  let formErr = $state('')
  // Ports that can join: free physical ports, bonds and VLANs (not libvirt's, not in use).
  let candidates = $derived(
    ifs.filter((i) => !i.master && (i.kind === 'physical' || (spec.kind === 'bridge' && (i.kind === 'bond' || i.kind === 'vlan')))),
  )
  let parents = $derived(ifs.filter((i) => i.kind === 'physical' || i.kind === 'bond' || (i.kind === 'bridge' && !i.name.startsWith('virbr'))))
  const hasAddress = (i?: HostInterface) => !!i?.addresses?.some((a) => !a.startsWith('fe80:'))
  let addrPorts = $derived((spec.ports ?? []).filter((p) => hasAddress(ifs.find((i) => i.name === p))))

  function freeName(prefix: string) {
    let n = 0
    while (ifs.some((i) => i.name === `${prefix}${n}`)) n++
    return `${prefix}${n}`
  }
  function openNew(kind: HostIfSpec['kind']) {
    spec =
      kind === 'vlan'
        ? { kind, name: '', parent: parents[0]?.name ?? '', vlan: 10 }
        : { kind, name: freeName(kind === 'bond' ? 'bond' : 'br'), ports: [], stp: false, bondMode: kind === 'bond' ? 'active-backup' : undefined }
    formErr = ''
    dlg = true
  }
  $effect(() => {
    if (spec.kind === 'vlan' && spec.parent && spec.vlan && (!spec.name || /\.\d+$/.test(spec.name))) spec.name = `${spec.parent}.${spec.vlan}`
  })
  function togglePort(name: string, on: boolean) {
    const ports = new Set(spec.ports ?? [])
    if (on) ports.add(name)
    else ports.delete(name)
    spec.ports = [...ports]
    if (spec.takeAddressFrom && !ports.has(spec.takeAddressFrom)) spec.takeAddressFrom = undefined
    if (!spec.takeAddressFrom && addrPorts.length === 1) spec.takeAddressFrom = addrPorts[0]
  }
  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    formErr = ''
    try {
      await started(ent.createHostIf($state.snapshot(spec)))
      dlg = false
    } catch (e2) {
      formErr = errorText(e2)
    } finally {
      busy = false
    }
  }
  const remove = (name: string) => started(ent.deleteHostIf(name))
  const BOND_MODES: [string, string][] = [
    ['active-backup', 'Active-backup — one link at a time, any switch'],
    ['802.3ad', 'LACP (802.3ad) — all links, the switch must be set up for LACP'],
    ['balance-alb', 'Adaptive load balancing — all links, any switch'],
    ['balance-tlb', 'Transmit load balancing'],
    ['balance-xor', 'XOR (static aggregation on the switch)'],
    ['balance-rr', 'Round robin'],
    ['broadcast', 'Broadcast'],
  ]
  const BACKEND: Record<string, string> = { netplan: 'netplan', networkmanager: 'NetworkManager', networkd: 'systemd-networkd' }
</script>

{#if pending}
  <div class="pending" role="alert">
    <TriangleAlert size={18} />
    <div class="txt">
      <b>{t('Keep this network change?')}</b>
      <span>{t('{what} — applied with {v}.', { what: pending.what, v: BACKEND[pending.backend] ?? pending.backend })}</span>
      {#if left > 0}
        <span class="mono">{t('Undone automatically in {left} s unless you keep it.', { left: left })}</span>
      {:else}
        <span class="mono">{t('Time is up: the host is undoing it.')}</span>
      {/if}
      {#if keepErr}<span class="ui-error-box">{keepErr}</span>{/if}
    </div>
    {#if admin && left > 0}
      <button class="ui-btn primary" disabled={keepBusy} onclick={() => keep(pending)}>{keepBusy ? 'Keeping…' : 'Keep'}</button>
      <button class="ui-btn ghost" onclick={() => undo(pending)}>{t('Undo now')}</button>
    {/if}
  </div>
{/if}

{#if st && admin}
  <div class="bar">
    {#if st.backend}
      <span class="muted small">Managed by {BACKEND[st.backend]}.</span>
      <button class="ui-btn sm" disabled={!!pending} onclick={() => openNew('bridge')}><Plus size={13} /> {t('Bridge')}</button>
      <button class="ui-btn sm" disabled={!!pending} onclick={() => openNew('bond')}><Plus size={13} /> {t('Bond')}</button>
      <button class="ui-btn sm" disabled={!!pending} onclick={() => openNew('vlan')}><Plus size={13} /> {t('VLAN interface')}</button>
    {:else}
      <span class="muted small">{st.reason}</span>
    {/if}
  </div>
  {#if st.created.length}
    <ul class="mine">
      {#each st.created as c (c.name)}
        <li class="mono">
          <b>{c.name}</b>
          <span class="muted">{c.kind}{c.ports?.length ? ` · ${c.ports.join(', ')}` : ''}{c.parent ? ` · VLAN ${c.vlan} on ${c.parent}` : ''}{c.takeAddressFrom ? t(' · address from {takeAddressFrom}', { takeAddressFrom: c.takeAddressFrom }) : ''}</span>
          <ConfirmButton label={t('Remove')} confirm={`Remove ${c.name}?`} icon={Trash} disabled={!!pending} run={() => remove(c.name)} />
        </li>
      {/each}
    </ul>
  {/if}
{:else if err && admin}
  <p class="muted small">{err}</p>
{/if}

<Dialog
  open={dlg}
  title={spec.kind === 'vlan' ? 'New VLAN interface' : spec.kind === 'bond' ? 'New bond' : t('New bridge')}
  subtitle={t('On this host, with its own network tools')}
  onclose={() => (dlg = false)}
>
  <form id="hostif-form" class="form" onsubmit={create}>
    <label class="ui-field">
      <span class="label">{t('Name')}</span>
      <input class="ui-input mono" bind:value={spec.name} required maxlength="15" />
    </label>
    {#if spec.kind === 'vlan'}
      <div class="row2">
        <label class="ui-field">
          <span class="label">{t('On')}</span>
          <select class="ui-input" bind:value={spec.parent}>
            {#each parents as p (p.name)}<option value={p.name}>{p.name} ({p.kind})</option>{/each}
          </select>
        </label>
        <label class="ui-field">
          <span class="label">{t('VLAN ID')}</span>
          <input class="ui-input mono" type="number" min="1" max="4094" bind:value={spec.vlan} required />
        </label>
      </div>
      <span class="hint">{t('Tagged traffic of that VLAN arrives here; put a bridge on it for machines, or use it as a bridge port.')}</span>
    {:else}
      <fieldset class="ports">
        <legend class="label">{spec.kind === 'bond' ? 'Members' : 'Ports'} <span class="opt">{spec.kind === 'bridge' ? '(optional: a bridge without ports only connects machines with each other)' : ''}</span></legend>
        {#each candidates as c (c.name)}
          <label class="check">
            <input type="checkbox" checked={spec.ports?.includes(c.name)} onchange={(e) => togglePort(c.name, e.currentTarget.checked)} />
            <span class="mono">{c.name}</span>
            <span class="muted">{c.kind}{c.speedMbps ? ` · ${c.speedMbps >= 1000 ? c.speedMbps / 1000 + 'G' : c.speedMbps + 'M'}` : ''}{hasAddress(c) ? ` · ${c.addresses?.filter((a) => !a.startsWith('fe80:')).join(', ')}` : ''}</span>
          </label>
        {:else}
          <span class="muted small">{t('No free ports.')}</span>
        {/each}
      </fieldset>
      {#if spec.kind === 'bond'}
        <label class="ui-field">
          <span class="label">{t('Mode')}</span>
          <select class="ui-input" bind:value={spec.bondMode}>
            {#each BOND_MODES as [v, l] (v)}<option value={v}>{l}</option>{/each}
          </select>
        </label>
      {:else}
        <label class="check"><input type="checkbox" bind:checked={spec.stp} /> <span>{t('Spanning tree (STP) — only if this bridge could form a loop with other switches')}</span></label>
      {/if}
      {#if addrPorts.length}
        <label class="ui-field">
          <span class="label">{t('Host address')}</span>
          <select class="ui-input" bind:value={spec.takeAddressFrom}>
            <option value={undefined}>{t('— none: keep the ports address-free —')}</option>
            {#each addrPorts as p (p)}<option value={p}>{t('Take over the address of {p}', { p: p })}</option>{/each}
          </select>
        </label>
        <div class="warn">
          {t('A port loses its own address in a {kind}. Taking it over moves the host\'s address (DHCP or static, routes, DNS) to {v}. With DHCP the host may get a different address. If this page cannot reach the host afterwards, the change is undone by itself after two minutes.', { kind: spec.kind, v: spec.name || 'the new interface' })}
        </div>
      {/if}
    {/if}
    {#if formErr}<div class="ui-error-box">{formErr}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (dlg = false)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="hostif-form" disabled={busy}>{busy ? 'Applying…' : t('Apply and confirm')}</button>
  {/snippet}
</Dialog>

<style>
  .pending {
    display: flex;
    gap: 12px;
    align-items: center;
    padding: 12px 14px;
    margin: 10px 0;
    border: var(--frame) solid var(--warn);
    background: color-mix(in srgb, var(--warn) 12%, var(--bg-elev));
  }
  .pending :global(svg) { color: var(--warn); flex: none; }
  .txt { display: flex; flex-direction: column; gap: 2px; flex: 1; font-size: 13px; }
  .bar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 10px 0 4px; }
  .mine { list-style: none; padding: 0; margin: 6px 0 0; display: flex; flex-direction: column; gap: 4px; font-size: 12.5px; }
  .mine li { display: flex; gap: 8px; align-items: center; }
  .mine .muted { flex: 1; }
  .muted { color: var(--text-2); }
  .small { font-size: 12px; }
  .ports { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
  .check { display: flex; gap: 8px; align-items: center; font-size: 13px; }
  .row2 { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .opt { color: var(--text-2); font-weight: normal; }
  .warn {
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border-left: 3px solid var(--warn);
    color: var(--text-2);
    font-size: 12.5px;
  }
  @media (max-width: 560px) {
    .pending { flex-wrap: wrap; }
    .row2 { grid-template-columns: 1fr; }
  }
</style>
