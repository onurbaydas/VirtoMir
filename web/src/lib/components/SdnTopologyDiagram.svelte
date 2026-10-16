<script lang="ts">
  // How this host's network is wired, read from /sys: physical ports, bonds, VLANs, bridges
  // and the machines' adapters (tap devices) plugged into them.
  import Network from '@lucide/svelte/icons/network'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import { errorText } from '../api'
  import { ent, type HostInterface } from '../enterprise'
  import { live } from '../live.svelte'

  let ifs = $state<HostInterface[]>([])
  let error = $state('')
  async function load() {
    try {
      ifs = await ent.interfaces()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  let byName = $derived(new Map(ifs.map((i) => [i.name, i])))
  // tap device → machine, from the live samples.
  let vmOf = $derived.by(() => {
    const m = new Map<string, string>()
    for (const d of live.snapshot?.domains ?? []) for (const n of d.nets ?? []) m.set(n.name, d.name)
    return m
  })
  let bridges = $derived(ifs.filter((i) => i.kind === 'bridge'))
  // Uplinks (physical, bond, VLAN) not inside a bridge: still worth seeing.
  let loose = $derived(ifs.filter((i) => (i.kind === 'physical' || i.kind === 'bond' || i.kind === 'vlan') && !i.master))

  const speed = (i?: HostInterface) => (i?.speedMbps ? (i.speedMbps >= 1000 ? `${i.speedMbps / 1000}G` : `${i.speedMbps}M`) : '')
  const down = (i?: HostInterface) => !!i && i.state !== 'up' && i.state !== 'unknown'
  function describe(name: string): string {
    const i = byName.get(name)
    if (!i) return name
    switch (i.kind) {
      case 'tap': return vmOf.get(name) ? `${vmOf.get(name)} (${name})` : name
      case 'bond': return `${name} · bond ${i.bondMode ?? ''} of ${(i.members ?? []).map((m) => `${m}${speed(byName.get(m)) ? ' ' + speed(byName.get(m)) : ''}`).join(' + ')}`
      case 'vlan': return `${name} · VLAN ${i.vlanId} on ${describe(i.parent ?? '')}`
      case 'physical': return `${name}${speed(i) ? ' · ' + speed(i) : ''}${i.driver ? ' · ' + i.driver : ''}`
    }
    return `${name} · ${i.kind}`
  }
  const isVM = (name: string) => byName.get(name)?.kind === 'tap'
</script>

<section class="ui-panel topo">
  <header>
    <h2><Network size={16} /> Host wiring</h2>
    <button class="ui-btn sm ghost" onclick={load}><RefreshCw size={13} /> Refresh</button>
  </header>
  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if !ifs.length}
    <p class="muted">Reading the interfaces…</p>
  {:else}
    <div class="grid">
      {#each bridges as b (b.name)}
        <div class="br" class:down={down(b)}>
          <div class="head mono"><b>{b.name}</b> <span class="muted">bridge{b.mtu ? ` · MTU ${b.mtu}` : ''}{b.state !== 'up' ? ` · ${b.state}` : ''}</span></div>
          {#if b.addresses?.length}<div class="addr mono">{b.addresses.join(', ')}</div>{/if}
          <ul>
            {#each (b.members ?? []).filter((m) => !isVM(m)) as m (m)}<li class="up mono" class:down={down(byName.get(m))}>⇅ {describe(m)}</li>{/each}
            {#each (b.members ?? []).filter(isVM) as m (m)}<li class="vm mono" class:down={down(byName.get(m))}>▣ {describe(m)}</li>{/each}
            {#if !b.members?.length}<li class="muted">nothing plugged in</li>{/if}
          </ul>
        </div>
      {/each}
      {#if loose.length}
        <div class="br">
          <div class="head mono"><b>Not in a bridge</b></div>
          <ul>
            {#each loose as i (i.name)}<li class="up mono" class:down={down(i)}>{describe(i.name)}{i.addresses?.length ? ` · ${i.addresses.join(', ')}` : ''}</li>{/each}
          </ul>
        </div>
      {/if}
    </div>
    <p class="muted small">⇅ uplink · ▣ machine adapter · grey: link down. Bonds and bridges are created with the host's own network tools (or a VLAN network in VirtoMir).</p>
  {/if}
</section>

<style>
  .topo { padding: 14px 16px; margin: 14px 0; }
  header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
  h2 { display: flex; gap: 8px; align-items: center; margin: 0; font-size: 15px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 10px; }
  .br { border: var(--frame) solid var(--border); padding: 8px 10px; background: var(--bg-elev); }
  .head { font-size: 13px; border-bottom: 1px solid var(--border); padding-bottom: 4px; margin-bottom: 4px; }
  .addr { font-size: 12px; color: var(--accent); margin-bottom: 4px; }
  ul { list-style: none; margin: 0; padding: 0; font-size: 12px; display: flex; flex-direction: column; gap: 2px; }
  .up { color: var(--text); }
  .vm { color: var(--accent-2); }
  .down { opacity: 0.45; }
  .muted { color: var(--text-2); }
  .small { font-size: 12px; margin: 8px 0 0; }
</style>
