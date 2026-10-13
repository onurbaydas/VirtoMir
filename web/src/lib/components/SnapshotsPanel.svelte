<script lang="ts">
  import { session } from '../session.svelte'
  // Snapshot tree of one machine: take, revert to and delete snapshots.
  import Camera from '@lucide/svelte/icons/camera'
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw'
  import Trash from '@lucide/svelte/icons/trash'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import { api, errorText, type SnapshotInfo } from '../api'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'
  import { router } from '../router.svelte'

  let { uuid, active, onchange }: { uuid: string; active: boolean; onchange: () => void } = $props()

  let snaps = $state<SnapshotInfo[] | null>(null)
  let error = $state('')
  let name = $state('')
  let description = $state('')
  let busy = $state(false)

  async function load() {
    try {
      snaps = await api.snapshots(uuid)
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  async function take(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    try {
      const res = await api.createSnapshot(uuid, name.trim(), description.trim())
      toasts.push({ kind: 'info', title: `Snapshot ${res.name} taken` })
      name = ''
      description = ''
      await load()
    } catch (err) {
      toasts.push({ kind: 'error', title: 'Snapshot failed', detail: errorText(err) })
    } finally {
      busy = false
    }
  }

  // Depth-first order with indentation from the parent links.
  interface Row {
    s: SnapshotInfo
    depth: number
  }
  let rows = $derived.by((): Row[] => {
    if (!snaps) return []
    const byParent = new Map<string, SnapshotInfo[]>()
    const names = new Set(snaps.map((s) => s.name))
    for (const s of snaps) {
      const p = s.parent && names.has(s.parent) ? s.parent : ''
      byParent.set(p, [...(byParent.get(p) ?? []), s])
    }
    const out: Row[] = []
    const walk = (parent: string, depth: number) => {
      for (const s of (byParent.get(parent) ?? []).sort((a, b) => a.created - b.created)) {
        out.push({ s, depth })
        walk(s.name, depth + 1)
      }
    }
    walk('', 0)
    return out
  })

  const when = (t: number) =>
    t ? new Date(t * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
  const STATE: Record<string, string> = { running: 'taken while running', paused: 'taken while paused', shutoff: 'taken while off' }
</script>

<section class="ui-panel panel">
  <form class="take" onsubmit={take}>
    <div class="fields">
      <input class="ui-input" bind:value={name} placeholder="Name (optional, e.g. before-upgrade)" pattern="[A-Za-z0-9][A-Za-z0-9._+-]*" spellcheck="false" />
      <input class="ui-input" bind:value={description} placeholder="Note (optional)" />
    </div>
    <button class="ui-btn primary" disabled={busy || !session.can('operator')} title={session.why('operator')}><Camera size={15} /> {busy ? 'Taking…' : 'Take snapshot'}</button>
  </form>
  <p class="hint">
    {active
      ? 'The machine keeps running. Its memory is saved too, so reverting brings it back exactly as it is now.'
      : 'Saves the disks as they are now.'}
    Snapshots of qcow2 disks are stored inside the image.
    <a href="#backups" onclick={(e) => (e.preventDefault(), router.navigate(location.pathname + '#backups', { replace: true }))}>Take them automatically →</a>
  </p>

  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if snaps === null}
    <p class="muted">Loading…</p>
  {:else if rows.length === 0}
    <p class="empty">No snapshots yet. Take one before risky changes, then return to it in one click.</p>
  {:else}
    <ol class="tree">
      {#each rows as { s, depth } (s.name)}
        <li class="snap" class:current={s.current} style:--d={depth}>
          <span class="node" aria-hidden="true"></span>
          <div class="info">
            <span class="name">
              {s.name}
              {#if s.current}<span class="badge" title="The machine was last reverted to or created from this snapshot">current</span>{/if}
              {#if s.memory}<span class="mem" title="Includes memory: reverting resumes the running machine"><MemoryStick size={13} /></span>{/if}
            </span>
            <span class="meta">{when(s.created)} · {STATE[s.state] ?? s.state}{s.location === 'external' ? ' · external' : ''}</span>
            {#if s.description}<span class="desc">{s.description}</span>{/if}
          </div>
          <div class="acts">
            <ConfirmButton
              label="Revert"
              confirm="Lose changes since then?"
              icon={RotateCcw}
              disabled={!session.can('operator')}
              title={session.why('operator')}
              failure="Could not revert to {s.name}"
              run={async () => {
                await api.revertSnapshot(uuid, s.name)
                toasts.push({ kind: 'info', title: `Reverted to ${s.name}` })
                await load()
                onchange()
              }}
            />
            <ConfirmButton
              label="Delete"
              confirm="Delete snapshot?"
              icon={Trash}
              disabled={!session.can('admin')}
              title={session.why('admin')}
              failure="Could not delete {s.name}"
              run={async () => {
                await api.deleteSnapshot(uuid, s.name)
                await load()
              }}
            />
          </div>
        </li>
      {/each}
    </ol>
  {/if}
</section>

<style>
  .panel {
    padding: 16px 18px;
  }
  .take {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .fields {
    flex: 1;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .hint {
    margin: 8px 0 14px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .hint a {
    color: var(--accent);
    text-decoration: none;
  }
  .muted,
  .empty {
    color: var(--text-3);
    font-size: 13px;
  }
  .empty {
    padding: 24px 0 10px;
    text-align: center;
  }
  .tree {
    margin: 0;
    padding: 0;
    list-style: none;
    border-top: 1px solid var(--border);
  }
  .snap {
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 0 10px calc(var(--d) * 26px);
    border-bottom: 1px solid var(--border);
  }
  .node {
    flex: none;
    width: 12px;
    height: 12px;
    border: 2px solid var(--border-strong);
    border-radius: 50%;
    background: var(--surface);
  }
  .current .node {
    border-color: var(--accent);
    background: var(--accent);
    box-shadow: 0 0 0 4px var(--accent-soft);
  }
  .info {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 7px;
    font-weight: 600;
    font-size: 13.5px;
  }
  .badge {
    padding: 0 7px;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 11px;
  }
  .mem {
    display: grid;
    color: var(--accent-2);
  }
  .meta {
    color: var(--text-3);
    font-size: 12px;
  }
  .desc {
    color: var(--text-2);
    font-size: 12.5px;
  }
  .acts {
    display: flex;
    gap: 4px;
  }
  @media (max-width: 760px) {
    .take {
      flex-direction: column;
      align-items: stretch;
    }
    .fields {
      grid-template-columns: 1fr;
    }
    .snap {
      flex-wrap: wrap;
    }
    .acts {
      width: 100%;
      padding-left: 24px;
    }
  }
</style>
