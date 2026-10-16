<script lang="ts">
  // Every saved version of the machine's definition (ADR-0021): who changed what, when —
  // including changes made outside VirtoMir — and one click to go back to any of them.
  import History from '@lucide/svelte/icons/history'
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw'
  import { api, errorText, type ConfigVersion } from '../api'
  import { diffLines, hunks, type DiffLine } from '../diff'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  let { uuid, active, version, onchange }: { uuid: string; active: boolean; version: unknown; onchange: () => void } = $props()

  let versions = $state<ConfigVersion[] | null>(null)
  let error = $state('')
  let selected = $state<string | null>(null)
  let diff = $state<(DiffLine | null)[] | null>(null)
  let diffErr = $state('')

  async function load() {
    try {
      versions = await api.configVersions(uuid)
      error = ''
      if (!selected || !versions.some((v) => v.id === selected)) selected = versions[0]?.id ?? null
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void version // reload when the machine changed
    void load()
  })

  // What the selected change did: the version before it → this one.
  $effect(() => {
    const id = selected
    const list = versions
    diff = null
    diffErr = ''
    if (!id || !list) return
    const i = list.findIndex((v) => v.id === id)
    const prev = list[i + 1]
    void (async () => {
      try {
        const [after, before] = await Promise.all([api.configVersion(uuid, id), prev ? api.configVersion(uuid, prev.id) : Promise.resolve('')])
        if (selected === id) diff = hunks(diffLines(before, after))
      } catch (e) {
        diffErr = errorText(e)
      }
    })()
  })

  async function restore(v: ConfigVersion) {
    const r = await api.revertConfig(uuid, v.id)
    toasts.push({
      kind: 'info',
      title: 'Earlier version restored',
      detail: r.appliesOnNextBoot ? 'The machine is running: it uses the restored definition from its next start.' : undefined,
    })
    onchange()
    await load()
  }

  const when = (t: string) => new Date(t).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
  const who = (v: ConfigVersion) =>
    v.source === 'first seen' ? 'First version VirtoMir saw' : v.source === 'outside' ? 'Changed outside VirtoMir (virsh or another tool)' : `Changed by ${v.user}`
  let sel = $derived(versions?.find((v) => v.id === selected))
  let isCurrent = $derived(!!versions && versions[0]?.id === selected)
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if versions === null}
  <p class="muted">Loading…</p>
{:else if versions.length === 0}
  <div class="empty ui-panel">
    <History size={28} strokeWidth={1.5} />
    <strong>No versions yet</strong>
    <span>VirtoMir saves the definition whenever it changes; the first one appears within a minute.</span>
  </div>
{:else}
  <div class="layout">
    <ol class="ui-panel list" aria-label="Versions">
      {#each versions as v, i (v.id)}
        <li>
          <button class:on={selected === v.id} onclick={() => (selected = v.id)}>
            <span class="t">{when(v.time)} {#if i === 0}<span class="cur">current</span>{/if}</span>
            <span class="w" class:outside={v.source === 'outside'}>{who(v)}</span>
          </button>
        </li>
      {/each}
    </ol>

    <section class="ui-panel detail">
      {#if sel}
        <header>
          <div>
            <strong>{who(sel)}</strong>
            <span class="muted">{when(sel.time)}</span>
          </div>
          {#if !isCurrent}
            <ConfirmButton
              label="Restore this version"
              confirm="Replace the current definition?"
              icon={RotateCcw}
              size="md"
              disabled={!session.can('admin')}
              title={session.why('admin') || (active ? 'Takes effect at the next start' : 'Makes this the definition again')}
              failure="Not restored"
              run={() => restore(sel!)}
            />
          {/if}
        </header>
        <div class="diff mono" aria-label="What this change did">
          {#if diffErr}
            <p class="err">{diffErr}</p>
          {:else if !diff}
            <p class="muted">Comparing…</p>
          {:else if diff.every((l) => l === null || l.kind === 'same')}
            <p class="muted">No visible difference.</p>
          {:else}
            {#each diff as l, i (i)}
              {#if l === null}<div class="gap">⋯</div>{:else}<div class="dl {l.kind}"><span class="sign">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>{l.text}</div>{/if}
            {/each}
          {/if}
        </div>
      {/if}
    </section>
  </div>
{/if}

<style>
  .layout {
    display: grid;
    grid-template-columns: minmax(240px, 320px) 1fr;
    gap: 14px;
    align-items: start;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 6px;
    max-height: 70vh;
    overflow-y: auto;
  }
  .list button {
    display: flex;
    flex-direction: column;
    gap: 2px;
    width: 100%;
    padding: 8px 10px;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .list button:hover {
    background: var(--surface-hover);
  }
  .list button.on {
    background: var(--accent-soft);
  }
  .t {
    font-size: 13px;
    font-weight: 600;
  }
  .w {
    font-size: 12px;
    color: var(--text-3);
  }
  .w.outside {
    color: var(--warn);
  }
  .cur {
    margin-left: 6px;
    padding: 0 7px;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 11px;
  }
  .detail header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
  }
  .detail header div {
    display: flex;
    flex-direction: column;
  }
  .diff {
    font-size: 12px;
    line-height: 1.6;
    max-height: 70vh;
    overflow: auto;
  }
  .diff p {
    padding: 0 14px;
  }
  .dl {
    padding: 0 14px;
    white-space: pre;
    color: var(--text-3);
  }
  .dl.add {
    background: color-mix(in srgb, var(--ok) 12%, transparent);
    color: var(--text);
  }
  .dl.del {
    background: var(--danger-soft);
    color: var(--text-2);
    text-decoration: line-through;
    text-decoration-color: color-mix(in srgb, var(--danger) 50%, transparent);
  }
  .sign {
    display: inline-block;
    width: 16px;
    color: var(--text-3);
  }
  .gap {
    padding: 0 14px;
    color: var(--text-3);
    background: var(--surface-2);
  }
  .muted {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .err {
    color: var(--danger);
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 28px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
  @media (max-width: 800px) {
    .layout {
      grid-template-columns: 1fr;
    }
  }
</style>
