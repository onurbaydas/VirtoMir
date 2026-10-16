<script lang="ts">
  // A machine's place in the cluster's safety nets: HA protection (restart elsewhere when its
  // host dies) and its resource pool (whose limits it counts against).
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import { errorText } from '../api'
  import { ent, PRIORITIES, type HAStatus, type PoolView, type Priority } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let { uuid, name }: { uuid: string; name: string } = $props()

  let ha = $state<HAStatus | null>(null)
  let pools = $state<PoolView[]>([])
  let form = $state({ enabled: false, priority: 'normal' as Priority, restartDelaySec: 0, maxRetries: 2, targetNode: '' })
  let pool = $state('')
  let dirty = $state(false)
  let saving = $state(false)

  $effect(() => {
    const u = uuid.toLowerCase()
    void ent.haStatus().then((s) => {
      ha = s
      const p = s.config.vmPolicies[u]
      if (p) form = { enabled: p.enabled, priority: p.priority, restartDelaySec: p.restartDelaySec, maxRetries: p.maxRetries, targetNode: p.targetNode ?? '' }
    }).catch(() => (ha = null))
    void ent.respools().then((ps) => {
      pools = ps
      pool = ps.find((p) => p.machines.includes(u))?.id ?? ''
    }).catch(() => (pools = []))
  })

  let machine = $derived(ha?.machines.find((m) => m.uuid === uuid.toLowerCase()))
  let admin = $derived(session.can('admin'))

  async function saveHA() {
    saving = true
    try {
      await ent.haSetPolicy({ uuid, name, ...form, targetNode: form.targetNode || undefined })
      dirty = false
      toasts.push({ kind: 'info', title: form.enabled ? `${name} is protected` : 'HA policy saved', detail: form.enabled ? 'Its definition is copied to every member within a few seconds.' : undefined })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not saved', detail: errorText(e) })
    } finally {
      saving = false
    }
  }

  async function savePool(id: string) {
    try {
      await ent.assignRespool(uuid, id)
      pool = id
      toasts.push({ kind: 'info', title: id ? `In pool ${pools.find((p) => p.id === id)?.name}` : 'Out of all pools' })
    } catch (e) {
      toasts.push({ kind: 'error', title: 'Not moved', detail: errorText(e) })
      pool = pools.find((p) => p.machines.includes(uuid.toLowerCase()))?.id ?? ''
    }
  }
</script>

<section class="ui-panel card">
  <h2><ShieldCheck size={16} /> High availability and resource pool</h2>
  {#if !ha}
    <p class="muted">High availability is not available on this host.</p>
  {:else}
    <label class="ui-switch"><input type="checkbox" bind:checked={form.enabled} onchange={() => (dirty = true)} disabled={!admin} /> Restart this machine on another host if its host dies</label>
    {#if !ha.clustered}<p class="hint">This host is not in a cluster yet: until it is, protection only means it starts again after this host reboots.</p>{/if}
    {#if form.enabled}
      <div class="grid">
        <label class="ui-field"><span class="label">Priority</span>
          <select class="ui-input" bind:value={form.priority} onchange={() => (dirty = true)} disabled={!admin}>
            {#each PRIORITIES as p (p)}<option value={p}>{p}</option>{/each}
          </select><span class="hint">Critical machines restart first.</span></label>
        <label class="ui-field"><span class="label">Preferred host</span>
          <select class="ui-input" bind:value={form.targetNode} onchange={() => (dirty = true)} disabled={!admin}>
            <option value="">The one with most free memory</option>
            {#each ha.nodes as n (n.id)}<option value={n.id}>{n.name}</option>{/each}
          </select></label>
        <label class="ui-field"><span class="label">Wait before starting (s)</span>
          <input class="ui-input" type="number" min="0" max="3600" bind:value={form.restartDelaySec} oninput={() => (dirty = true)} disabled={!admin} /></label>
        <label class="ui-field"><span class="label">Hosts to try</span>
          <input class="ui-input" type="number" min="1" max="16" bind:value={form.maxRetries} oninput={() => (dirty = true)} disabled={!admin} /></label>
      </div>
      <p class="hint">Its disks must be on storage every host reaches (NFS, Ceph, iSCSI). HA turns autostart off for it and starts it itself.
        {#if machine?.known}Definition copied {new Date(machine.updated ?? '').toLocaleTimeString()}, owner {machine.ownerName}.{/if}</p>
    {/if}
    {#if admin}<div><button class="ui-btn sm primary" disabled={!dirty || saving} onclick={saveHA}>{saving ? 'Saving…' : 'Save HA policy'}</button></div>{/if}
  {/if}

  <label class="ui-field pool">
    <span class="label">Resource pool</span>
    <select class="ui-input" value={pool} disabled={!admin || pools.length === 0} onchange={(e) => savePool((e.currentTarget as HTMLSelectElement).value)}>
      <option value="">{pools.length ? 'None (not limited by a pool)' : 'No pools on this host'}</option>
      {#each pools as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
    </select>
    <span class="hint">Moving it in is refused if it does not fit the pool's limits.</span>
  </label>
</section>

<style>
  .card { display: flex; flex-direction: column; gap: 10px; padding: 16px; }
  h2 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 15px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 10px; }
  .hint { color: var(--text-2); font-size: 12px; margin: 0; }
  .muted { color: var(--text-2); }
  .pool { margin-top: 6px; max-width: 360px; }
</style>
