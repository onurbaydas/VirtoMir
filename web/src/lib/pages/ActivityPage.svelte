<script lang="ts">
  import History from '@lucide/svelte/icons/clock-arrow-up'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleX from '@lucide/svelte/icons/circle-x'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import { api, errorText, type ActivityEntry } from '../api'
  import { describe } from '../activity'
  import { live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import { session } from '../session.svelte'

  let entries = $state<ActivityEntry[] | null>(null)
  let error = $state('')
  let failedOnly = $state(false)
  let user = $state('')

  async function load() {
    try {
      entries = await api.activity({ failed: failedOnly, user: user || undefined })
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void failedOnly
    void user
    void load()
  })

  let names = $derived(new Map((live.snapshot?.domains ?? []).map((d) => [d.uuid, d.name])))
  let users = $derived([...new Set((entries ?? []).map((e) => e.user))].sort())

  const when = (t: string) => new Date(t).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
  // Group by day so long lists stay readable.
  let days = $derived.by(() => {
    const out: { day: string; items: ActivityEntry[] }[] = []
    for (const e of entries ?? []) {
      const day = new Date(e.time).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' })
      if (out.at(-1)?.day !== day) out.push({ day, items: [] })
      out.at(-1)!.items.push(e)
    }
    return out
  })
</script>

<svelte:head><title>Activity · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Activity</h1>
    <p class="lead">Who changed what, when — every change and sign-in, including refused ones.</p>
  </div>
  <div class="tools">
    <select class="ui-input" bind:value={user} aria-label="Filter by user">
      <option value="">Everyone</option>
      {#each users as u (u)}<option value={u}>{u}</option>{/each}
    </select>
    <label class="ui-switch"><input type="checkbox" bind:checked={failedOnly} /> Failed only</label>
    <button class="ui-btn ghost" onclick={load} title="Reload"><RefreshCw size={15} /></button>
  </div>
</header>

{#if !session.can('admin')}
  <div class="ui-error-box">{session.why('admin')}</div>
{:else if error}
  <div class="ui-error-box">{error}</div>
{:else if entries === null}
  <p class="muted">Loading…</p>
{:else if entries.length === 0}
  <div class="empty ui-panel">
    <History size={28} strokeWidth={1.5} />
    <strong>Nothing yet</strong>
    <span>Changes made in VirtoMir or through its API appear here.</span>
  </div>
{:else}
  {#each days as d (d.day)}
    <section class="day">
      <h2>{d.day}</h2>
      <ol class="ui-panel list">
        {#each d.items as e, i (e.time + i)}
          {@const info = describe(e, names)}
          {@const ok = e.status > 0 && e.status < 400}
          <li class:failed={!ok}>
            <span class="icon">{#if ok}<CircleCheck size={16} />{:else}<CircleX size={16} />{/if}</span>
            <div class="what">
              <span class="sentence">
                <b>{e.user || 'someone'}</b>
                {info.before}{#if info.machine}{#if names.has(info.machine)}<a href={machineHref(info.machine)} onclick={router.link}>{info.machineName}</a>{:else}<span class="gone">{info.machineName}</span>{/if}{/if}{info.after}
                {#if e.token}<span class="tok" title="Through the API token “{e.token}”"><KeyRound size={12} /> {e.token}</span>{/if}
              </span>
              {#if e.error}<span class="err">{e.error}</span>{/if}
            </div>
            <span class="meta mono" title={e.remote}>{when(e.time).split(', ').pop()}</span>
          </li>
        {/each}
      </ol>
    </section>
  {/each}
{/if}

<style>
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .tools {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .tools select {
    width: 160px;
  }
  .day {
    margin-bottom: 18px;
  }
  h2 {
    margin: 0 0 8px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .list {
    margin: 0;
    padding: 0;
    list-style: none;
    overflow: hidden;
  }
  li {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 10px 16px;
    border-top: 1px solid var(--border);
    font-size: 13.5px;
  }
  li:first-child {
    border-top: 0;
  }
  .icon {
    display: grid;
    padding-top: 1px;
    color: var(--ok);
  }
  .failed .icon {
    color: var(--danger);
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .sentence b {
    font-weight: 600;
  }
  .sentence a {
    color: var(--accent);
    text-decoration: none;
  }
  .gone {
    color: var(--text-3);
  }
  .tok {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: 6px;
    padding: 0 7px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-3);
    font-size: 11.5px;
  }
  .err {
    color: var(--danger);
    font-size: 12.5px;
  }
  .meta {
    flex: none;
    color: var(--text-3);
    font-size: 12px;
  }
  .muted {
    color: var(--text-3);
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 56px 16px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
</style>
