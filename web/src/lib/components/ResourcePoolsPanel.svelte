<script lang="ts">
  import { t } from '../i18n.svelte'
  // Resource pools (ADR-0032): machines grouped under hard limits that VirtoMir checks on
  // every create, resize and move. Pools nest; a child counts against its parents.
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import Pencil from '@lucide/svelte/icons/pencil'
  import { api, errorText, type Pool } from '../api'
  import { ent, gib, type PoolView, type ResourcePool } from '../enterprise'
  import { live } from '../live.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import Dialog from './Dialog.svelte'

  let pools = $state<PoolView[]>([])
  let storage = $state<Pool[]>([])
  let error = $state('')
  let loaded = $state(false)

  async function load() {
    try {
      ;[pools, storage] = await Promise.all([ent.respools(), api.pools()])
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

  // Tree order: roots by name, each followed by its children.
  let tree = $derived.by(() => {
    const out: { p: PoolView; depth: number }[] = []
    const walk = (parent: string | undefined, depth: number) => {
      for (const p of pools.filter((x) => (x.parentId ?? '') === (parent ?? '')).sort((a, b) => a.name.localeCompare(b.name))) {
        out.push({ p, depth })
        walk(p.id, depth + 1)
      }
    }
    walk(undefined, 0)
    return out
  })
  let domains = $derived(live.snapshot?.domains ?? [])
  let nameOf = (uuid: string) => domains.find((d) => d.uuid.toLowerCase() === uuid)?.name ?? uuid.slice(0, 8)
  let poolOf = $derived((uuid: string) => pools.find((p) => p.machines.includes(uuid.toLowerCase()))?.id ?? '')
  let admin = $derived(session.can('admin'))

  // ---- Create / edit --------------------------------------------------------------------
  interface Form {
    id?: string
    name: string
    parentId: string
    description: string
    cpuLimit: number
    memoryGiB: number
    storageGiB: Record<string, number>
  }
  let editing = $state<Form | null>(null)
  let saving = $state(false)

  function openNew() {
    editing = { name: '', parentId: '', description: '', cpuLimit: 0, memoryGiB: 0, storageGiB: {} }
  }
  function openEdit(p: ResourcePool) {
    const sg: Record<string, number> = {}
    for (const [k, v] of Object.entries(p.storageLimits ?? {})) sg[k] = Math.round(v / 2 ** 30)
    editing = { id: p.id, name: p.name, parentId: p.parentId ?? '', description: p.description ?? '', cpuLimit: p.cpuLimit, memoryGiB: Math.round(p.memoryLimitMiB / 1024), storageGiB: sg }
  }
  async function saveForm() {
    if (!editing) return
    saving = true
    const limits: Record<string, number> = {}
    for (const [k, v] of Object.entries(editing.storageGiB)) if (v > 0) limits[k] = v * 2 ** 30
    const body = {
      name: editing.name, parentId: editing.parentId || undefined, description: editing.description,
      cpuLimit: editing.cpuLimit || 0, memoryLimitMiB: (editing.memoryGiB || 0) * 1024, storageLimits: limits,
    }
    try {
      if (editing.id) await ent.updateRespool({ ...(pools.find((p) => p.id === editing!.id) as ResourcePool), ...body, id: editing.id })
      else await ent.createRespool(body)
      toasts.push({ kind: 'info', title: editing.id ? 'Pool saved' : t('Pool created') })
      editing = null
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not saved'), detail: errorText(e) })
    } finally {
      saving = false
    }
  }

  async function remove(p: ResourcePool) {
    await ent.deleteRespool(p.id)
    toasts.push({ kind: 'info', title: `${p.name} deleted` })
    await load()
  }

  // ---- Machines -------------------------------------------------------------------------
  let adding = $state<PoolView | null>(null)
  let pick = $state('')
  async function assign(uuid: string, pool: string) {
    try {
      await ent.assignRespool(uuid, pool)
      toasts.push({ kind: 'info', title: pool ? t('{toLowerCase} moved into the pool', { toLowerCase: nameOf(uuid.toLowerCase()) }) : t('{toLowerCase} taken out of the pool', { toLowerCase: nameOf(uuid.toLowerCase()) }) })
      adding = null
      pick = ''
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not moved'), detail: errorText(e) })
    }
  }

  function pct(used: number, limit: number) {
    return limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0
  }
</script>

<header class="head">
  <p class="lead">{t('Hard caps on what a group of machines may take together (vCPUs, memory, disk). Nested pools count against their parents. Kept per host.')}</p>
  {#if admin}<button class="ui-btn primary" onclick={openNew}><Plus size={14} /> {t('New pool')}</button>{/if}
</header>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if !loaded}
  <p class="muted">{t('Loading…')}</p>
{:else if tree.length === 0}
  <section class="ui-panel empty"><p class="muted">{t('No resource pools yet. Machines outside pools are not limited (apart from user quotas).')}</p></section>
{:else}
  {#each tree as { p, depth } (p.id)}
    <section class="ui-panel pool" style="margin-left: {depth * 24}px">
      <header>
        <div>
          <h3>{depth ? '└ ' : ''}{p.name}</h3>
          {#if p.description}<p class="muted">{p.description}</p>{/if}
        </div>
        {#if admin}
          <div class="acts">
            <button class="ui-btn sm" onclick={() => (adding = p)}><Plus size={13} /> {t('Add machine')}</button>
            <button class="ui-btn sm ghost" onclick={() => openEdit(p)}><Pencil size={13} /> {t('Edit')}</button>
            <ConfirmButton label={t('Delete')} confirm={t('Delete this pool?')} icon={Trash} run={() => remove(p)} />
          </div>
        {/if}
      </header>
      <div class="meters">
        <div class="meter">
          <span class="lbl">{t('vCPUs')}</span>
          <span class="val mono">{p.usage.vcpus}{p.cpuLimit ? ` / ${p.cpuLimit}` : ''}</span>
          {#if p.cpuLimit}<div class="bar"><i style="width: {pct(p.usage.vcpus, p.cpuLimit)}%" class:hot={pct(p.usage.vcpus, p.cpuLimit) > 90}></i></div>{:else}<span class="muted small">{t('no limit')}</span>{/if}
        </div>
        <div class="meter">
          <span class="lbl">{t('Memory')}</span>
          <span class="val mono">{(p.usage.memoryMiB / 1024).toFixed(1)}{p.memoryLimitMiB ? ` / ${(p.memoryLimitMiB / 1024).toFixed(0)}` : ''} GiB</span>
          {#if p.memoryLimitMiB}<div class="bar"><i style="width: {pct(p.usage.memoryMiB, p.memoryLimitMiB)}%" class:hot={pct(p.usage.memoryMiB, p.memoryLimitMiB) > 90}></i></div>{:else}<span class="muted small">{t('no limit')}</span>{/if}
        </div>
        {#each Object.entries(p.storageLimits ?? {}) as [sp, max] (sp)}
          <div class="meter">
            <span class="lbl">Disk on {sp}</span>
            <span class="val mono">{gib(p.usage.storageBytes[sp] ?? 0)} / {gib(max)}</span>
            <div class="bar"><i style="width: {pct(p.usage.storageBytes[sp] ?? 0, max)}%" class:hot={pct(p.usage.storageBytes[sp] ?? 0, max) > 90}></i></div>
          </div>
        {/each}
      </div>
      <div class="machines">
        {#each p.machines as uuid (uuid)}
          <span class="vm">{nameOf(uuid)}{#if admin}<button class="x" title={t('Take it out of the pool')} onclick={() => assign(uuid, '')}>×</button>{/if}</span>
        {:else}
          <span class="muted small">{t('No machines directly in this pool{v}.', { v: p.usage.machines ? ` (${p.usage.machines} in its sub-pools)` : '' })}</span>
        {/each}
      </div>
    </section>
  {/each}
{/if}

<Dialog open={!!editing} title={editing?.id ? 'Edit pool' : t('New resource pool')} onclose={() => (editing = null)} width={560}>
  {#if editing}
    <div class="form">
      <label class="ui-field"><span class="label">{t('Name')}</span><input class="ui-input" bind:value={editing.name} /></label>
      <label class="ui-field"><span class="label">{t('Inside')}</span>
        <select class="ui-input" bind:value={editing.parentId}>
          <option value="">{t('— (top level)')}</option>
          {#each pools.filter((p) => p.id !== editing?.id) as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
        <span class="hint">{t('A pool inside another must set every limit its parent has, at most as high.')}</span>
      </label>
      <label class="ui-field"><span class="label">{t('Description')}</span><input class="ui-input" bind:value={editing.description} /></label>
      <div class="two">
        <label class="ui-field"><span class="label">{t('vCPU limit (0 = none)')}</span><input class="ui-input" type="number" min="0" bind:value={editing.cpuLimit} /></label>
        <label class="ui-field"><span class="label">{t('Memory limit, GiB (0 = none)')}</span><input class="ui-input" type="number" min="0" bind:value={editing.memoryGiB} /></label>
      </div>
      <span class="label">{t('Disk limits per storage pool, GiB (empty = none)')}</span>
      <div class="two">
        {#each storage as sp (sp.name)}
          <label class="ui-field"><span class="hint">{sp.name} ({sp.type})</span>
            <input class="ui-input" type="number" min="0" placeholder={t('no limit')} bind:value={editing.storageGiB[sp.name]} /></label>
        {/each}
      </div>
    </div>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (editing = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={saving || !editing?.name.trim()} onclick={saveForm}>{saving ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<Dialog open={!!adding} title={adding ? t('Add a machine to {name}', { name: adding.name }) : ''} onclose={() => (adding = null)}>
  <label class="ui-field">
    <span class="label">{t('Machine')}</span>
    <select class="ui-input" bind:value={pick}>
      <option value="">{t('Choose…')}</option>
      {#each domains.filter((d) => !d.template) as d (d.uuid)}
        <option value={d.uuid}>{t('{name} · {vcpus} vCPU · {v} GiB{name2}', { name: d.name, vcpus: d.vcpus, v: Math.round(d.maxMemoryKiB / 1048576), name2: poolOf(d.uuid) ? ` (now in ${pools.find((p) => p.id === poolOf(d.uuid))?.name})` : '' })}</option>
      {/each}
    </select>
    <span class="hint">{t('It leaves any other pool. Refused if it does not fit the limits.')}</span>
  </label>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => (adding = null)}>{t('Cancel')}</button>
    <button class="ui-btn primary" disabled={!pick} onclick={() => adding && assign(pick, adding.id)}>{t('Add')}</button>
  {/snippet}
</Dialog>

<style>
  .head { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; margin-bottom: 12px; }
  .lead { margin: 0; color: var(--text-2); max-width: 760px; font-size: 13px; }
  .empty { padding: 16px; }
  .pool { padding: 14px 16px; margin-bottom: 10px; }
  .pool header { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; }
  .pool h3 { margin: 0; font-size: 15px; }
  .pool p { margin: 2px 0 0; font-size: 12px; }
  .acts { display: flex; gap: 6px; flex-wrap: wrap; }
  .meters { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; margin: 12px 0; }
  .meter { display: flex; flex-direction: column; gap: 3px; }
  .lbl { font: 500 12px var(--font-sans); color: var(--text-2); }
  .val { font-size: 13px; }
  .bar { height: 6px; background: var(--bg-elev); box-shadow: var(--bevel-in); }
  .bar i { display: block; height: 100%; background: var(--ok); }
  .bar i.hot { background: var(--danger); }
  .machines { display: flex; flex-wrap: wrap; gap: 6px; }
  .vm { border: 1px solid var(--border); padding: 2px 8px; font-size: 12px; display: inline-flex; gap: 6px; align-items: center; }
  .x { background: none; border: 0; color: var(--text-2); cursor: pointer; font-size: 14px; padding: 0; }
  .x:hover { color: var(--danger); }
  .form { display: flex; flex-direction: column; gap: 10px; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .hint { color: var(--text-2); font-size: 12px; }
  .label { font-size: 12px; font-weight: 600; }
  .muted { color: var(--text-2); }
  .small { font-size: 12px; }
</style>
