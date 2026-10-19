<script lang="ts">
  import { t } from '../i18n.svelte'
  // GPU pools (ADR-0071): the cluster's GPUs grouped by how machines get them — a whole
  // card by passthrough, a vGPU slice of one type, or one MIG profile — so a new machine
  // asks for "2 of l40s" and lands on the member that has them free.
  import Plus from '@lucide/svelte/icons/plus'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Trash from '@lucide/svelte/icons/trash'
  import { errorText } from '../api'
  import { ent, type ClusterGPU, type GPUKind, type GPUPool, type GPUPoolView } from '../enterprise'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'

  let pools = $state<GPUPoolView[]>([])
  let gpus = $state<ClusterGPU[]>([])
  let error = $state('')
  let loaded = $state(false)
  let admin = $derived(session.can('admin'))

  async function load() {
    try {
      ;[pools, gpus] = await Promise.all([ent.gpuPools(), ent.clusterGPUs()])
      error = ''
    } catch (e) {
      error = errorText(e)
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    void load()
  })

  const KIND: Record<GPUKind, string> = { whole: t('Whole card (passthrough)'), mdev: t('vGPU slice'), mig: t('MIG instance') }

  // Profiles the cluster's cards offer, for the form's picker.
  let mdevTypes = $derived([...new Map(gpus.flatMap((g) => g.mdevTypes ?? []).map((m) => [m.id, m])).values()])
  let migProfiles = $derived([...new Map(gpus.flatMap((g) => g.migProfiles ?? []).map((m) => [m.id, m])).values()])
  let models = $derived([...new Set(gpus.filter((g) => g.name).map((g) => g.name!))])
  // Emulated display adapters are not worth listing.
  let real = $derived(gpus.filter((g) => !['Virtio', 'QEMU', 'VMware', 'Red Hat', 'Microsoft', 'Cirrus', 'VirtualBox'].includes(g.vendor)))

  let editing = $state<(GPUPool & { isNew: boolean }) | null>(null)
  let saving = $state(false)
  function openNew() {
    editing = { isNew: true, id: '', name: '', model: '', kind: 'whole', profile: '', perHost: 0 }
  }
  function openEdit(p: GPUPoolView) {
    editing = { isNew: false, id: p.id, name: p.name, description: p.description, model: p.model, kind: p.kind, profile: p.profile ?? '', perHost: p.perHost ?? 0 }
  }
  async function save() {
    if (!editing) return
    saving = true
    const { isNew, ...p } = editing
    const body: GPUPool = { ...p, profile: p.kind === 'whole' ? undefined : p.profile, perHost: p.perHost || 0 }
    try {
      if (isNew) await ent.createGPUPool(body)
      else await ent.saveGPUPool(p.id, body)
      toasts.push({ kind: 'info', title: isNew ? t('GPU pool created') : t('GPU pool saved') })
      editing = null
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }
  async function remove(p: GPUPoolView) {
    await ent.deleteGPUPool(p.id)
    toasts.push({ kind: 'info', title: t('{name} deleted', { name: p.name }) })
    await load()
  }
  const pct = (a: number, b: number) => (b > 0 ? Math.round((a / b) * 100) : 0)
</script>

<header class="head">
  <p class="lead">{t('Group the GPUs by how machines get them. A new machine asks for GPUs of a pool and is placed on the member that has them free.')}</p>
  {#if admin}<button class="ui-btn primary" onclick={openNew}><Plus size={14} /> {t('New GPU pool')}</button>{/if}
</header>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !loaded}
  <p class="muted">{t('Loading…')}</p>
{:else}
  {#if pools.length === 0}
    <section class="ui-panel empty"><p class="muted">{t('No GPU pools yet.')}</p></section>
  {/if}
  {#each pools as p (p.id)}
    <section class="ui-panel pool">
      <header>
        <div>
          <h3>{p.name} <span class="tag">{KIND[p.kind]}{p.profile ? ` · ${p.profile}` : ''}</span></h3>
          <p class="muted">{p.model || t('any model')}{p.perHost ? ` · ${t('at most {n} per machine', { n: p.perHost })}` : ''}{p.description ? ` · ${p.description}` : ''}</p>
        </div>
        <div class="right">
          <span class="big mono">{t('{free} of {total} free', { free: p.free, total: p.total })}</span>
          {#if admin}
            <button class="ui-btn sm ghost" onclick={() => openEdit(p)} aria-label={t('Edit')}><Pencil size={14} /></button>
            <ConfirmButton label={t('Delete')} icon={Trash} confirm={t('Delete this pool')} run={() => remove(p)} failure={t('Not deleted')} />
          {/if}
        </div>
      </header>
      {#if p.kind === 'mig'}
        <p class="hint">{t('MIG instances reach a machine through an NVIDIA vGPU type: to give them to machines, make a vGPU slice pool with the MIG-backed type.')}</p>
      {/if}
      {#each p.hosts ?? [] as h (h.hostId)}
        <div class="host">
          <span class="name">{h.hostName}</span>
          <span class="bar"><span style="width: {pct(h.used, h.total)}%"></span></span>
          <span class="mono small">{t('{used} used · {free} free{reserved}', { used: h.used, free: h.free, reserved: h.reserved ? ` · ${h.reserved} reserved` : '' })}</span>
        </div>
      {:else}
        <p class="muted small">{t('No member has GPUs of this pool.')}</p>
      {/each}
    </section>
  {/each}

  <h3 class="sub">{t('GPUs in the cluster')}</h3>
  {#if real.length === 0}
    <p class="muted">{t('No member reports a GPU.')}</p>
  {:else}
    <table class="t">
      <thead><tr><th>{t('Host')}</th><th>{t('GPU')}</th><th>{t('Can be')}</th><th>{t('Pools')}</th><th>{t('Used by')}</th></tr></thead>
      <tbody>
        {#each real as g (g.hostId + g.address)}
          <tr>
            <td>{g.hostName}</td>
            <td>{g.name || g.vendor} <span class="mono small muted">{g.address}</span></td>
            <td>
              {#if g.profile}{KIND[g.profile]}{/if}
              {#if g.profile === 'whole' && !g.passThroughReady}<span class="warn small">{t('no IOMMU group')}</span>{/if}
            </td>
            <td>{g.pools.length ? g.pools.join(', ') : '—'}</td>
            <td>{g.usedBy?.length ? g.usedBy.join(', ') : '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<Dialog open={!!editing} title={editing?.isNew ? t('New GPU pool') : t('Edit {name}', { name: editing?.name ?? '' })} width={520} onclose={() => (editing = null)}>
  {#if editing}
    <form id="gpool" class="form" onsubmit={(e) => (e.preventDefault(), save())}>
      <label class="ui-field">
        <span class="label">{t('ID')}</span>
        <input class="ui-input mono" bind:value={editing.id} disabled={!editing.isNew} placeholder="l40s" required pattern="[a-z0-9][a-z0-9-]{'{1,38}'}[a-z0-9]" />
      </label>
      <label class="ui-field">
        <span class="label">{t('Name')}</span>
        <input class="ui-input" bind:value={editing.name} required />
      </label>
      <fieldset class="kinds">
        {#each Object.entries(KIND) as [k, label] (k)}
          <label class="ui-switch"><input type="radio" bind:group={editing.kind} value={k} /> {label}</label>
        {/each}
      </fieldset>
      <label class="ui-field">
        <span class="label">{t('Model')}</span>
        <input class="ui-input" bind:value={editing.model} list="gpu-models" placeholder="L40S" required={editing.kind === 'whole'} />
        <datalist id="gpu-models">{#each models as m (m)}<option value={m}></option>{/each}</datalist>
        <span class="hint">{t('Part of the card name, e.g. L40S or A100. Empty: any card that offers the profile.')}</span>
      </label>
      {#if editing.kind !== 'whole'}
        <label class="ui-field">
          <span class="label">{editing.kind === 'mdev' ? t('vGPU type') : t('MIG profile')}</span>
          <input class="ui-input mono" bind:value={editing.profile} list="gpu-profiles" required placeholder={editing.kind === 'mdev' ? 'nvidia-1155' : '1g.10gb'} />
          <datalist id="gpu-profiles">
            {#if editing.kind === 'mdev'}
              {#each mdevTypes as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
            {:else}
              {#each migProfiles as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
            {/if}
          </datalist>
        </label>
      {/if}
      <label class="ui-field">
        <span class="label">{t('At most per machine')}</span>
        <input class="ui-input" type="number" min="0" bind:value={editing.perHost} />
        <span class="hint">{t('0 = no cap.')}</span>
      </label>
      <label class="ui-field">
        <span class="label">{t('Description')}</span>
        <input class="ui-input" bind:value={editing.description} />
      </label>
    </form>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (editing = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" form="gpool" disabled={saving}>{t('Save')}</button>
  {/snippet}
</Dialog>

<style>
  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 16px;
    margin-bottom: 16px;
  }
  .lead,
  .muted {
    color: var(--text-3);
    margin: 0;
  }
  .pool,
  .empty {
    padding: 14px 16px;
    margin-bottom: 12px;
  }
  .pool header {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 10px;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .big {
    font-size: 13px;
    font-weight: 600;
  }
  h3 {
    margin: 0 0 4px;
    font-size: 15px;
  }
  .sub {
    margin: 22px 0 10px;
  }
  .tag {
    margin-left: 6px;
    padding: 1px 7px;
    border: var(--frame) solid var(--ink);
    font: 600 11px var(--font-mono);
    color: var(--text-2);
  }
  .host {
    display: grid;
    grid-template-columns: 120px 1fr auto;
    gap: 12px;
    align-items: center;
    padding: 4px 0;
  }
  .bar {
    height: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--info);
  }
  .small {
    font-size: 12px;
  }
  .warn {
    color: var(--warn);
    margin-left: 6px;
  }
  .hint {
    color: var(--text-3);
    font-size: 12px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .kinds {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .t {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .t th {
    text-align: left;
    font: 500 12px var(--font-sans);
    color: var(--text-2);
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .t td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  @media (max-width: 720px) {
    .head,
    .pool header {
      flex-direction: column;
    }
    .host {
      grid-template-columns: 1fr;
      gap: 4px;
    }
  }
</style>
