<script lang="ts">
  import { t } from '../i18n.svelte'
  // What happened to this machine, in one list: what people did, how its definition changed
  // (also outside VirtoMir), when it started, stopped, crashed or was paused and why, its
  // snapshots, backups and alerts. The answer to "it worked yesterday: what changed?".
  import Archive from '@lucide/svelte/icons/archive'
  import Bell from '@lucide/svelte/icons/bell'
  import Camera from '@lucide/svelte/icons/camera'
  import CircleCheck from '@lucide/svelte/icons/circle-check'
  import CircleX from '@lucide/svelte/icons/circle-x'
  import History from '@lucide/svelte/icons/history'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import Pause from '@lucide/svelte/icons/pause'
  import Pencil from '@lucide/svelte/icons/pencil'
  import Play from '@lucide/svelte/icons/play'
  import Square from '@lucide/svelte/icons/square'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { describe } from '../activity'
  import { api, errorText, type TimelineEvent } from '../api'
  import { formatBytes } from '../format'

  let { uuid, name, version, onversion }: { uuid: string; name: string; version: unknown; onversion: (id: string) => void } = $props()

  let events = $state<TimelineEvent[] | null>(null)
  let missing = $state<string[]>([])
  let error = $state('')
  let problemsOnly = $state(false)

  async function load() {
    try {
      const r = await api.machineTimeline(uuid)
      events = r.events
      missing = r.missing ?? []
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void version // reload when the machine changed
    void load()
  })

  const failed = (e: TimelineEvent) => e.kind === 'action' && !((e.status ?? 0) > 0 && (e.status ?? 0) < 400)

  /** Something went wrong here: what people look for first. */
  function problem(e: TimelineEvent): boolean {
    switch (e.kind) {
      case 'action':
        return failed(e)
      case 'lifecycle':
        return e.what === 'crashed' || (e.what === 'stopped' && (e.detail === 'crashed' || e.detail === 'failed')) || (e.what === 'paused' && ['ioerror', 'watchdog', 'api-error', 'postcopy-failed'].includes(e.detail ?? ''))
      case 'alert':
        return true
      case 'backup':
        return !!e.checkError
    }
    return false
  }

  /** What libvirt reported, in a sentence. */
  function lifecycle(e: TimelineEvent): string {
    const why = e.detail ?? ''
    switch (e.what) {
      case 'started':
        return why === 'restored'
          ? t('Started again from its saved state')
          : why === 'from-snapshot'
            ? t('Started from a snapshot')
            : why === 'migrated'
              ? t('Arrived here by migration')
              : why === 'wakeup'
                ? t('Woke up from sleep')
                : t('Started')
      case 'stopped':
        return why === 'shutdown'
          ? t('Shut down')
          : why === 'destroyed'
            ? t('Forced off')
            : why === 'crashed'
              ? t('Stopped: it crashed')
              : why === 'failed'
                ? t('Stopped: the hypervisor failed')
                : why === 'migrated'
                  ? t('Left this host by migration')
                  : why === 'saved'
                    ? t('Stopped with its state saved')
                    : why === 'from-snapshot'
                      ? t('Stopped to go back to a snapshot')
                      : t('Stopped')
      case 'paused':
        return why === 'ioerror'
          ? t('Paused by itself: its disk could not be written (is the storage pool full?)')
          : why === 'watchdog'
            ? t('Paused by its watchdog: the guest stopped answering')
            : why === 'api-error'
              ? t('Paused: the hypervisor reported an error')
              : why === 'migrated'
                ? t('Paused for a moment to migrate')
                : t('Paused')
      case 'resumed':
        return t('Resumed')
      case 'crashed':
        return t('The guest crashed (a kernel panic or a blue screen)')
      case 'shutdown':
        return why === 'guest' ? t('A shutdown was asked for from inside the guest') : t('A shutdown was asked for by the host')
    }
    return e.what ?? ''
  }

  /** How the definition or the running configuration changed. */
  function changed(e: TimelineEvent): string {
    if (e.kind === 'running')
      return e.what === 'outside'
        ? t('A device was added or removed while it ran, outside VirtoMir (lost at the next shutdown unless it is saved too)')
        : t('The running configuration was changed')
    if (e.what === 'first seen') return t('VirtoMir first saw this machine')
    if (e.what === 'outside') return t('The definition was changed outside VirtoMir (virsh or another tool)')
    return t('The definition was changed')
  }

  let shown = $derived((events ?? []).filter((e) => !problemsOnly || problem(e)))
  let problems = $derived((events ?? []).filter(problem).length)
  let names = $derived(new Map([[uuid, name]]))

  // Grouped by day, like the activity log.
  let days = $derived.by(() => {
    const out: { day: string; items: TimelineEvent[] }[] = []
    for (const e of shown) {
      const day = new Date(e.time).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })
      if (out.at(-1)?.day !== day) out.push({ day, items: [] })
      out.at(-1)!.items.push(e)
    }
    return out
  })
  const clock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
  const SOURCE: Record<string, string> = { snapshots: t('snapshots'), backups: t('backups') }
</script>

{#if error}
  <div class="ui-error-box">{error}</div>
{:else if events === null}
  <p class="muted">{t('Loading…')}</p>
{:else}
  <div class="head">
    <p class="lead">{t('Everything that happened to this machine, newest first: what was done, what changed, when it started, stopped or crashed, its snapshots, backups and alerts.')}</p>
    {#if problems > 0}
      <label class="ui-switch"><input type="checkbox" bind:checked={problemsOnly} /> {t('Only what went wrong ({n})', { n: problems })}</label>
    {/if}
  </div>
  {#if missing.length}
    <div class="note" role="status">{t('Could not be read right now, so they are missing below: {what}.', { what: missing.map((m) => SOURCE[m] ?? m).join(', ') })}</div>
  {/if}
  {#if shown.length === 0}
    <div class="empty ui-panel">
      <History size={28} strokeWidth={1.5} />
      <strong>{t('Nothing here yet')}</strong>
      <span>{t('What happens to this machine from now on is listed here.')}</span>
    </div>
  {:else}
    {#each days as d (d.day)}
      <section class="day">
        <h2>{d.day}</h2>
        <ol class="ui-panel list">
          {#each d.items as e, i (e.time + e.kind + i)}
            <li class:bad={problem(e)}>
              <time datetime={e.time}>{clock(e.time)}</time>
              <span class="icon" class:quiet={e.kind === 'definition' || e.kind === 'running'}>
                {#if e.kind === 'action'}
                  {#if failed(e)}<CircleX size={16} />{:else}<CircleCheck size={16} />{/if}
                {:else if e.kind === 'lifecycle'}
                  {#if problem(e)}<TriangleAlert size={16} />{:else if e.what === 'started' || e.what === 'resumed'}<Play size={16} />{:else if e.what === 'paused'}<Pause size={16} />{:else}<Square size={16} />{/if}
                {:else if e.kind === 'snapshot'}<Camera size={16} />
                {:else if e.kind === 'backup'}<Archive size={16} />
                {:else if e.kind === 'alert'}<Bell size={16} />
                {:else}<Pencil size={16} />{/if}
              </span>
              <div class="what">
                <span class="sentence">
                  {#if e.kind === 'action'}
                    {@const info = describe({ time: e.time, user: e.user ?? '', method: e.method ?? '', path: e.path ?? '', status: e.status ?? 0 }, names)}
                    <b>{e.user || t('someone')}</b>
                    {info.before}{info.machineName ?? ''}{info.after}
                    {#if e.token}<span class="tok" title={t('Through the API token “{token}”', { token: e.token })}><KeyRound size={12} /> {e.token}</span>{/if}
                  {:else if e.kind === 'lifecycle'}
                    {lifecycle(e)}
                  {:else if e.kind === 'snapshot'}
                    {#if e.user}<b>{e.user}</b> {t('took snapshot {name}', { name: e.ref ?? '' })}{:else}{t('Snapshot {name} was taken', { name: e.ref ?? '' })}{/if}
                  {:else if e.kind === 'backup'}
                    {#if e.user}<b>{e.user}</b> {t('backed it up')}{:else if e.automatic}{t('Backed up by its schedule')}{:else}{t('Backed up')}{/if}
                    {#if e.sizeBytes}<span class="dim">· {formatBytes(e.sizeBytes)}</span>{/if}
                  {:else if e.kind === 'alert'}
                    {t('Alert')}: {e.detail}
                  {:else}
                    {#if e.user}<b>{e.user}</b> {e.kind === 'running' ? t('changed the running configuration') : t('changed the definition')}{:else}{changed(e)}{/if}
                  {/if}
                </span>
                {#if e.kind === 'action' && failed(e) && e.error}<span class="err">{e.error}</span>{/if}
                {#if e.kind === 'backup' && e.checkError}<span class="err">{t('Its last check found a problem: {error}', { error: e.checkError })}</span>{/if}
                {#if e.kind === 'backup' && e.detail}<span class="dim">{e.detail}</span>{/if}
                {#if e.kind === 'alert'}<span class="dim">{e.until ? t('Ended {when}', { when: when(e.until) }) : t('Still on')}</span>{/if}
              </div>
              {#if e.version && !e.version.startsWith('live-')}
                <button class="ui-btn sm ghost" onclick={() => onversion(e.version!)}>{t('See what changed')}</button>
              {/if}
            </li>
          {/each}
        </ol>
      </section>
    {/each}
    <p class="foot">{t('Starts, stops and crashes are listed from the time VirtoMir began to keep them, and not for the times it was not running.')}</p>
  {/if}
{/if}

<style>
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 12px;
  }
  .head .ui-switch {
    flex: none;
    white-space: nowrap;
  }
  .lead,
  .foot {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .foot {
    margin-top: 4px;
    font-size: 12px;
  }
  .note {
    margin-bottom: 12px;
    padding: 9px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 13px;
  }
  .day {
    margin-bottom: 16px;
  }
  h2 {
    margin: 0 0 8px;
    color: var(--text-3);
    font-size: 12px;
    font-weight: 600;
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
    padding: 9px 16px;
    border-top: 1px solid var(--border);
    font-size: 13.5px;
  }
  li:first-child {
    border-top: 0;
  }
  time {
    flex: none;
    min-width: 84px;
    white-space: nowrap;
    padding-top: 1px;
    color: var(--text-3);
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
  }
  .icon {
    display: grid;
    padding-top: 1px;
    color: var(--ok);
  }
  .icon.quiet {
    color: var(--text-3);
  }
  .bad .icon {
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
  .dim {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .err {
    color: var(--danger);
    font-size: 12.5px;
  }
  .tok {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: 6px;
    padding: 0 7px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-3);
    font-size: 11.5px;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 36px 16px;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .empty strong {
    color: var(--text);
  }
  .muted {
    color: var(--text-3);
  }
  @media (max-width: 640px) {
    .head {
      flex-direction: column;
    }
    time {
      min-width: 0;
    }
    li {
      flex-wrap: wrap;
    }
    /* The sentence keeps its width; "See what changed" goes below it. */
    .what {
      min-width: 55%;
    }
    li > :global(.ui-btn) {
      margin-left: auto;
    }
  }
</style>
