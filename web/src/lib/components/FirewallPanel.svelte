<script lang="ts">
  import { t } from '../i18n.svelte'
  // A machine's firewall (ADR-0033): a libvirt network filter on the host side of its network
  // adapters, so the guest cannot switch it off. IPv4 and IPv6, stateful (replies are let through).
  import Shield from '@lucide/svelte/icons/shield'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import ArrowUp from '@lucide/svelte/icons/arrow-up'
  import ArrowDown from '@lucide/svelte/icons/arrow-down'
  import Save from '@lucide/svelte/icons/save'
  import { errorText } from '../api'
  import { ent, type Firewall, type FirewallRule } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let { uuid, name, active }: { uuid: string; name: string; active: boolean } = $props()

  let fw = $state<Firewall | null>(null)
  let error = $state('')
  let dirty = $state(false)
  let saving = $state(false)

  async function load() {
    try {
      fw = await ent.firewall(uuid)
      dirty = false
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void uuid
    void load()
  })

  const PRESETS: { label: string; rule: FirewallRule }[] = [
    { label: t('SSH (22)'), rule: { direction: 'in', action: 'accept', protocol: 'tcp', ports: '22' } },
    { label: t('Web (80, 443)'), rule: { direction: 'in', action: 'accept', protocol: 'tcp', ports: '80' } },
    { label: t('RDP (3389)'), rule: { direction: 'in', action: 'accept', protocol: 'tcp', ports: '3389' } },
    { label: t('Ping'), rule: { direction: 'in', action: 'accept', protocol: 'icmp' } },
  ]

  function add(r?: FirewallRule) {
    if (!fw) return
    fw.rules.push(r ? { ...r } : { direction: 'in', action: 'accept', protocol: 'tcp', ports: '', remote: '', comment: '' })
    if (r?.ports === '80') fw.rules.push({ ...r, ports: '443' })
    dirty = true
  }
  function move(i: number, d: number) {
    if (!fw) return
    const j = i + d
    if (j < 0 || j >= fw.rules.length) return
    ;[fw.rules[i], fw.rules[j]] = [fw.rules[j], fw.rules[i]]
    dirty = true
  }
  function del(i: number) {
    fw?.rules.splice(i, 1)
    dirty = true
  }

  async function save() {
    if (!fw) return
    saving = true
    try {
      const clean = { ...fw, rules: fw.rules.map((r) => ({ ...r, ports: r.protocol === 'tcp' || r.protocol === 'udp' ? r.ports || undefined : undefined, remote: r.remote || undefined, comment: r.comment || undefined })) }
      const res = await ent.saveFirewall(uuid, clean)
      toasts.push({
        kind: res.liveError ? 'warn' : 'info',
        title: fw.enabled ? t('Firewall of {name} saved', { name: name }) : t('Firewall of {name} off', { name: name }),
        detail: res.liveError ?? (active ? 'In effect now.' : t('In effect from the next start.')),
      })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }
  let admin = $derived(session.can('admin'))
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !fw}
  <p class="muted">{t('Reading the firewall…')}</p>
{:else}
  <section class="ui-panel fw">
    <header>
      <h2><Shield size={16} /> {t('Firewall')}</h2>
      <label class="ui-switch"><input type="checkbox" bind:checked={fw.enabled} onchange={() => (dirty = true)} disabled={!admin} /> {fw.enabled ? 'On' : 'Off'}</label>
    </header>
    <p class="muted">{t('Enforced on the host; the first matching rule wins. A rule without an address applies to IPv4 and IPv6.')}</p>
    {#if fw.enabled && !fw.rules.length && fw.defaultIn === 'accept' && fw.defaultOut === 'accept'}
      <p class="warn">{t('On, but with no rules and both defaults on Allow it blocks nothing.')}</p>
    {:else if fw.enabled && fw.defaultOut === 'drop' && !fw.rules.some((r) => r.direction === 'out' && r.action === 'accept')}
      <p class="warn">{t('Outgoing is blocked with no exception: the machine gets no DHCP lease, DNS or updates.')}</p>
    {/if}
    {#if fw.adapters?.length === 0}<div class="ui-error-box">{t('This machine has no adapter the firewall can apply to.')}</div>{/if}
    {#if fw.unsupported?.length}<p class="warn">{t('Not covered: {v} (macvtap/direct or passthrough adapters bypass host filters).', { v: fw.unsupported.join(', ') })}</p>{/if}

    {#if fw.enabled}
      <div class="defaults">
        <label class="ui-field"><span class="label">{t('Incoming, when no rule matches')}</span>
          <select class="ui-input" bind:value={fw.defaultIn} onchange={() => (dirty = true)} disabled={!admin}><option value="accept">{t('Allow')}</option><option value="drop">{t('Block')}</option></select></label>
        <label class="ui-field"><span class="label">{t('Outgoing, when no rule matches')}</span>
          <select class="ui-input" bind:value={fw.defaultOut} onchange={() => (dirty = true)} disabled={!admin}><option value="accept">{t('Allow')}</option><option value="drop">{t('Block')}</option></select></label>
        <label class="ui-switch"><input type="checkbox" bind:checked={fw.antiSpoof} onchange={() => (dirty = true)} disabled={!admin} /> {t('Anti-spoofing: only its own MAC and IP (libvirt clean-traffic)')}</label>
      </div>

      <table class="t">
        <thead><tr><th></th><th>{t('Direction')}</th><th>{t('Action')}</th><th>{t('Protocol')}</th><th>{t('Port(s)')}</th><th>{t('Remote address')}</th><th>{t('Comment')}</th><th></th></tr></thead>
        <tbody>
          {#each fw.rules as r, i (i)}
            <tr>
              <td class="mono muted">{i + 1}</td>
              <td><select class="ui-input" bind:value={r.direction} onchange={() => (dirty = true)} disabled={!admin}><option value="in">{t('In')}</option><option value="out">{t('Out')}</option></select></td>
              <td><select class="ui-input act {r.action}" bind:value={r.action} onchange={() => (dirty = true)} disabled={!admin}><option value="accept">{t('Allow')}</option><option value="drop">{t('Drop')}</option><option value="reject">{t('Reject')}</option></select></td>
              <td><select class="ui-input" bind:value={r.protocol} onchange={() => (dirty = true)} disabled={!admin}><option value="tcp">{t('TCP')}</option><option value="udp">{t('UDP')}</option><option value="icmp">{t('ICMP')}</option><option value="all">{t('Any')}</option></select></td>
              <td><input class="ui-input mono" placeholder={r.protocol === 'tcp' || r.protocol === 'udp' ? 'any' : '—'} disabled={!admin || (r.protocol !== 'tcp' && r.protocol !== 'udp')} bind:value={r.ports} oninput={() => (dirty = true)} /></td>
              <td><input class="ui-input mono" placeholder={t('anyone (or 10.0.0.0/8, 2001:db8::/32)')} bind:value={r.remote} oninput={() => (dirty = true)} disabled={!admin} /></td>
              <td><input class="ui-input" bind:value={r.comment} oninput={() => (dirty = true)} disabled={!admin} /></td>
              <td class="row-acts">{#if admin}
                <button class="ui-btn sm ghost" title={t('Up')} onclick={() => move(i, -1)}><ArrowUp size={12} /></button>
                <button class="ui-btn sm ghost" title={t('Down')} onclick={() => move(i, 1)}><ArrowDown size={12} /></button>
                <button class="ui-btn sm ghost" title={t('Remove')} onclick={() => del(i)}><Trash size={12} /></button>
              {/if}</td>
            </tr>
          {:else}
            <tr><td colspan="8" class="muted">{t('No rules: only the defaults above apply.')}</td></tr>
          {/each}
        </tbody>
      </table>
      {#if admin}
        <div class="adds">
          <button class="ui-btn sm" onclick={() => add()}><Plus size={13} /> {t('Rule')}</button>
          {#each PRESETS as p (p.label)}<button class="ui-btn sm ghost" onclick={() => add(p.rule)}>+ {p.label}</button>{/each}
        </div>
      {/if}
    {/if}
    {#if admin}
      <footer>
        <button class="ui-btn primary" disabled={!dirty || saving} onclick={save}><Save size={14} /> {saving ? 'Saving…' : t('Save firewall')}</button>
        {#if dirty}<button class="ui-btn ghost" onclick={load}>{t('Discard')}</button>{/if}
      </footer>
    {/if}
  </section>
{/if}

<style>
  .fw { padding: 16px; display: flex; flex-direction: column; gap: 12px; }
  header { display: flex; justify-content: space-between; align-items: center; }
  h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .muted { color: var(--text-2); font-size: 13px; margin: 0; }
  .warn { color: var(--warn); font-size: 13px; margin: 0; }
  .defaults { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 10px; align-items: end; }
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 6px; border-bottom: 1px solid var(--border); }
  .t td { padding: 4px 6px; border-bottom: 1px solid var(--border); }
  .t .ui-input { min-width: 70px; }
  .act.accept { color: var(--ok); }
  .act.drop, .act.reject { color: var(--danger); }
  .row-acts { white-space: nowrap; }
  .adds, footer { display: flex; gap: 6px; flex-wrap: wrap; }
  @media (max-width: 900px) { .t { display: block; overflow-x: auto; } }
</style>
