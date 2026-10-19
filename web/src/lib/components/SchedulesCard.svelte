<script lang="ts">
  import { t } from '../i18n.svelte'
  // Automatic snapshots and backups of one machine: one schedule of each kind, edited in place.
  import CalendarClock from '@lucide/svelte/icons/calendar-clock'
  import Play from '@lucide/svelte/icons/play'
  import { api, errorText, type ScheduleInfo } from '../api'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let { uuid }: { uuid: string } = $props()

  type Kind = 'snapshot' | 'backup'
  let list = $state<ScheduleInfo[] | null>(null)
  let error = $state('')
  let drafts = $state<Record<Kind, ScheduleInfo>>({
    snapshot: blank('snapshot'),
    backup: blank('backup'),
  })
  let saved = $state<Record<Kind, string>>({ snapshot: '', backup: '' })
  let busy = $state<Kind | null>(null)

  function blank(kind: Kind): ScheduleInfo {
    return kind === 'snapshot'
      ? { id: '', kind, domain: uuid, enabled: false, every: 'daily', at: '03:00', weekday: 0, hours: 6, keep: 7 }
      : { id: '', kind, domain: uuid, enabled: false, every: 'weekly', at: '02:00', weekday: 0, hours: 6, keep: 4, keepDaily: 7, keepWeekly: 4, keepMonthly: 12, keepYearly: 1, fullEvery: 7 }
  }

  async function load() {
    try {
      const l = await api.schedules(uuid)
      list = l
      for (const k of ['snapshot', 'backup'] as Kind[]) {
        const s = l.find((x) => x.kind === k)
        drafts[k] = s ? { ...blank(k), ...s } : blank(k)
        saved[k] = JSON.stringify(strip(drafts[k]))
      }
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  const strip = (s: ScheduleInfo) => ({ ...s, lastRun: undefined, lastError: undefined, lastResult: undefined, nextRun: undefined })
  const dirty = (k: Kind) => JSON.stringify(strip(drafts[k])) !== saved[k]

  async function save(k: Kind) {
    busy = k
    try {
      await api.saveSchedule(strip($state.snapshot(drafts[k])))
      const title = k === 'snapshot'
        ? drafts[k].enabled ? t('Automatic snapshots are on') : t('Automatic snapshots are off')
        : drafts[k].enabled ? t('Automatic backups are on') : t('Automatic backups are off')
      toasts.push({ kind: 'info', title })
      await load()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Schedule not saved'), detail: errorText(e) })
    } finally {
      busy = null
    }
  }

  async function runNow(k: Kind) {
    if (!drafts[k].id) return
    try {
      await api.runSchedule(drafts[k].id)
      toasts.push({ kind: 'info', title: `${k === 'snapshot' ? 'Snapshot' : 'Backup'} started`, detail: t('Retention runs right after it.') })
      setTimeout(load, 3000)
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not start'), detail: errorText(e) })
    }
  }

  const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
  const when = (s?: string) => (s ? new Date(s).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : '')
  const LABEL: Record<Kind, { title: string; what: string }> = {
    snapshot: { title: t('Automatic snapshots'), what: t('Quick points to return to, stored inside the disk image. Not a backup: they live and die with the disk.') },
    backup: { title: t('Automatic backups'), what: t('Full copies of the disks and definition in the backup location — taken live, without stopping the machine.') },
  }
</script>

<section class="ui-panel card">
  <h2><CalendarClock size={16} /> {t('Automatic protection')}</h2>
  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if !list}
    <p class="muted">{t('Loading…')}</p>
  {:else}
    <div class="grid">
      {#each ['snapshot', 'backup'] as Kind[] as k (k)}
        {@const d = drafts[k]}
        <div class="sched" class:on={d.enabled}>
          <div class="head">
            <label class="ui-switch"><input type="checkbox" bind:checked={d.enabled} disabled={!session.can('admin')} /> <strong>{LABEL[k].title}</strong></label>
          </div>
          <p class="what">{LABEL[k].what}</p>
          <div class="row" inert={!session.can('admin')}>
            <select class="ui-input" bind:value={d.every} aria-label={t('How often')}>
              <option value="hours">{t('every few hours')}</option>
              <option value="daily">{t('every day')}</option>
              <option value="weekly">{t('every week')}</option>
            </select>
            {#if d.every === 'hours'}
              <select class="ui-input" bind:value={d.hours} aria-label={t('Hours')}>
                {#each [1, 2, 3, 4, 6, 8, 12] as h (h)}<option value={h}>every {h} h</option>{/each}
              </select>
            {:else}
              {#if d.every === 'weekly'}
                <select class="ui-input" bind:value={d.weekday} aria-label={t('Weekday')}>
                  {#each DAYS as day, i (day)}<option value={i}>on {day}</option>{/each}
                </select>
              {/if}
              <input class="ui-input time" type="time" bind:value={d.at} aria-label={t('At')} />
            {/if}
            <span class="keep">{t('keep the last')} <input class="ui-input num" type="number" min="1" max="365" bind:value={d.keep} aria-label={t('Keep')} /></span>
          </div>
          {#if k === 'backup'}
            <div class="gfs-row" inert={!session.can('admin')}>
              <span class="gfs-title">{t('GFS Retention:')}</span>
              <label class="gfs-item">{t('Daily')} <input class="ui-input gfs-num" type="number" min="0" max="365" bind:value={d.keepDaily} placeholder="0" /></label>
              <label class="gfs-item">{t('Weekly')} <input class="ui-input gfs-num" type="number" min="0" max="52" bind:value={d.keepWeekly} placeholder="0" /></label>
              <label class="gfs-item">{t('Monthly')} <input class="ui-input gfs-num" type="number" min="0" max="120" bind:value={d.keepMonthly} placeholder="0" /></label>
              <label class="gfs-item">{t('Yearly')} <input class="ui-input gfs-num" type="number" min="0" max="10" bind:value={d.keepYearly} placeholder="0" /></label>
            </div>
            <div class="gfs-row" inert={!session.can('admin')}>
              <label class="gfs-item" title={t('Between full backups only what changed is copied (running machines with qcow2 disks). 0 = every backup is full.')}>
                {t('Full backup every')} <input class="ui-input gfs-num" type="number" min="0" max="100" bind:value={d.fullEvery} placeholder="0" /> {t('backups')}
              </label>
            </div>
          {/if}
          <div class="status">
            {#if d.lastError}
              <span class="err">{t('Last run failed: {lastError}', { lastError: d.lastError })}</span>
            {:else if d.lastResult}
              <span class="muted">Last: {when(d.lastRun)} — {d.lastResult}</span>
            {/if}
            {#if d.enabled && d.nextRun}<span class="muted">Next: {when(d.nextRun)}</span>{/if}
          </div>
          <div class="actions">
            {#if d.id}
              <button class="ui-btn sm ghost" onclick={() => runNow(k)} disabled={!session.can('admin')} title={session.why('admin')}><Play size={13} /> {t('Run now')}</button>
            {/if}
            <button class="ui-btn sm primary" disabled={!dirty(k) || busy === k || !session.can('admin')} onclick={() => save(k)}>{busy === k ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
      {/each}
    </div>
    <p class="note">{t('Retention only removes what the schedule made itself — snapshots named')} <span class="mono">{t('auto-…')}</span> {t('and automatic backups. Your own are never touched.')}</p>
  {/if}
</section>

<style>
  .card {
    padding: 16px 18px;
    margin-bottom: 16px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 12px;
    font-size: 14.5px;
    font-weight: 600;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .sched {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .sched.on {
    border-color: color-mix(in srgb, var(--accent) 45%, transparent);
  }
  .what {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .row select,
  .row .time {
    width: auto;
    height: 32px;
    font-size: 12.5px;
  }
  .keep {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .num {
    width: 60px;
    height: 32px;
  }
  .gfs-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    font-size: 11.5px;
  }
  .gfs-title {
    font-weight: 600;
    color: var(--text-2);
    margin-right: 4px;
  }
  .gfs-item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--text-3);
  }
  .gfs-num {
    width: 48px;
    height: 26px;
    padding: 2px 6px;
    font-size: 12px;
  }
  .status {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--text-3);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
  }
  .note {
    margin: 10px 0 0;
    color: var(--text-3);
    font-size: 12px;
  }
  @media (max-width: 900px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
</style>
