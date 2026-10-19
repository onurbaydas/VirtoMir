<script lang="ts">
  import { t } from '../i18n.svelte'
  // A machine's adapters on SDN subnets (ADR-0072): OVN port, address, floating IP and
  // security groups, and plugging another adapter into a subnet.
  import Plus from '@lucide/svelte/icons/plus'
  import { api, errorText } from '../api'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import { sdn, type FloatingIP, type SDNPort, type SecurityGroup, type Subnet } from '../sdn'

  let { uuid, project = '', onchange }: { uuid: string; project?: string; onchange?: () => void } = $props()

  let ports = $state<SDNPort[]>([])
  let subnets = $state<Subnet[]>([])
  let groups = $state<SecurityGroup[]>([])
  let fips = $state<FloatingIP[]>([])
  let available = $state(false)

  async function load() {
    try {
      ;[ports, subnets, groups, fips] = await Promise.all([sdn.ports({ vm: uuid }), sdn.subnets(), sdn.securityGroups(), sdn.floatingIPs()])
      available = true
    } catch {
      available = false // no SDN on this host
    }
  }
  $effect(() => {
    void uuid
    void load()
  })

  let vpcs = $state<{ id: string; projectId: string }[]>([])
  $effect(() => {
    void sdn.vpcs().then((v) => (vpcs = v)).catch(() => {})
  })
  let pluggable = $derived(subnets.filter((s) => session.can('admin') || vpcs.find((v) => v.id === s.vpcId)?.projectId === project))

  const subnetOf = (id: string) => subnets.find((s) => s.id === id)
  const groupName = (id: string) => groups.find((g) => g.id === id)?.name ?? id
  const fipOf = (p: SDNPort) => fips.find((f) => f.portId === p.id || (f.internalIp && f.internalIp === p.ip))

  let form = $state<{ subnet: string; groups: string[]; ip: string } | null>(null)
  let saving = $state(false)
  async function plug() {
    if (!form?.subnet) return
    saving = true
    try {
      await api.attachNIC(uuid, { ovnSubnet: form.subnet, securityGroups: form.groups.length ? form.groups : undefined, ipAddress: form.ip.trim() || undefined })
      toasts.push({ kind: 'info', title: t('Adapter plugged into the subnet') })
      form = null
      await load()
      onchange?.()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not plugged in'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }
</script>

{#if available && (ports.length || pluggable.length)}
  <section class="group">
    <h2>{t('SDN adapters')} <span class="count num">{ports.length}</span></h2>
    {#if ports.length}
      <table class="t">
        <thead><tr><th>{t('Subnet')}</th><th>{t('Address')}</th><th>{t('Floating IP')}</th><th>{t('Security groups')}</th><th>{t('OVN port')}</th></tr></thead>
        <tbody>
          {#each ports as p (p.id)}
            {@const s = subnetOf(p.subnetId)}
            {@const f = fipOf(p)}
            <tr>
              <td>{s?.name ?? p.subnetId} <span class="mono small muted">{s?.cidr ?? ''}</span></td>
              <td class="mono">{p.ip ?? '—'} <span class="small muted">{p.mac}</span></td>
              <td class="mono">{f?.externalIp ?? '—'}</td>
              <td>{p.securityGroups?.length ? p.securityGroups.map(groupName).join(', ') : '—'}</td>
              <td class="mono small">{p.id}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="hint">{t('Taking such an adapter off frees its address and its floating IP.')}</p>
    {/if}
    {#if session.can('operator') && pluggable.length}
      <button class="ui-btn sm" onclick={() => (form = { subnet: pluggable[0].id, groups: [], ip: '' })}><Plus size={14} /> {t('Plug into an SDN subnet')}</button>
    {/if}
  </section>
{/if}

<Dialog open={!!form} title={t('Plug into an SDN subnet')} width={460} onclose={() => (form = null)}>
  {#if form}
    <form id="sdn-plug" class="form" onsubmit={(e) => (e.preventDefault(), plug())}>
      <label class="ui-field">
        <span class="label">{t('Subnet')}</span>
        <select class="ui-input" bind:value={form.subnet}>
          {#each pluggable as s (s.id)}<option value={s.id}>{s.name} ({s.cidr})</option>{/each}
        </select>
      </label>
      <label class="ui-field">
        <span class="label">{t('Address')}</span>
        <input class="ui-input mono" bind:value={form.ip} placeholder={t('from DHCP')} />
      </label>
      {#each groups.filter((g) => g.vpcId === subnetOf(form!.subnet)?.vpcId) as g (g.id)}
        <label class="ui-switch"><input type="checkbox" bind:group={form.groups} value={g.id} /> {g.name}</label>
      {/each}
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (form = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="sdn-plug" disabled={saving}>{t('Plug in')}</button>
  {/snippet}
</Dialog>

<style>
  .group {
    margin-bottom: 18px;
  }
  h2 {
    font-size: 14px;
    margin: 0 0 8px;
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .count {
    color: var(--text-3);
    font-size: 12px;
  }
  .t {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    margin-bottom: 6px;
  }
  .t th {
    text-align: left;
    font: 500 12px var(--font-sans);
    color: var(--text-2);
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .t td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .small {
    font-size: 11.5px;
  }
  .muted,
  .hint {
    color: var(--text-3);
  }
  .hint {
    font-size: 12px;
    margin: 0 0 8px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  @media (max-width: 720px) {
    .t {
      display: block;
      overflow-x: auto;
    }
  }
</style>
