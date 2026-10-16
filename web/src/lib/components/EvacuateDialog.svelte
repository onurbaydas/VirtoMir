<script lang="ts">
  import { t } from '../i18n.svelte'
  // Maintenance: live-migrate every running machine off this host, one after the other, to
  // the members with most free memory. The plan is made from real numbers (each member's
  // live summary) and machines that cannot move live (host devices) are named, not moved.
  import Dialog from './Dialog.svelte'
  import { api, errorText, type Job } from '../api'
  import { cluster } from '../cluster.svelte'
  import { live } from '../live.svelte'
  import { formatKiB } from '../format'
  import { toasts } from '../toast.svelte'

  let { open = $bindable(false) }: { open?: boolean } = $props()

  interface Step {
    uuid: string
    name: string
    memKiB: number
    target?: string
    targetName?: string
    problem?: string
    state: 'planned' | 'moving' | 'done' | 'failed' | 'skipped'
    message?: string
  }
  let steps = $state<Step[]>([])
  let planning = $state(false)
  let running = $state(false)
  let error = $state('')

  async function plan() {
    planning = true
    error = ''
    try {
      const self = cluster.current?.id
      const others = cluster.members.filter((m) => m.reachable && m.id !== self)
      const free = new Map<string, number>()
      await Promise.all(
        others.map(async (m) => {
          try {
            const s = await api.summary(m.id)
            free.set(m.id, s.host.memTotalKiB - s.host.memUsedKiB)
          } catch {
            /* unreachable now: not a target */
          }
        }),
      )
      const vms = (live.snapshot?.domains ?? []).filter((d) => d.state === 'running' || d.state === 'paused' || d.state === 'blocked')
      const out: Step[] = []
      for (const d of [...vms].sort((a, b) => b.maxMemoryKiB - a.maxMemoryKiB)) {
        const st: Step = { uuid: d.uuid, name: d.name, memKiB: d.maxMemoryKiB, state: 'planned' }
        try {
          const det = await api.domain(d.uuid)
          if (det.devices.some((x) => x.kind === 'hostdev')) st.problem = 'has a host device (PCI/USB/vGPU): stop it or remove the device first'
        } catch {
          /* move will tell */
        }
        if (!st.problem) {
          const best = [...free.entries()].filter(([, f]) => f >= d.maxMemoryKiB).sort((a, b) => b[1] - a[1])[0]
          if (best) {
            st.target = best[0]
            st.targetName = cluster.members.find((m) => m.id === best[0])?.name
            free.set(best[0], best[1] - d.maxMemoryKiB)
          } else st.problem = 'no other host has enough free memory'
        }
        if (st.problem) st.state = 'skipped'
        out.push(st)
      }
      steps = out
    } catch (e) {
      error = errorText(e)
    } finally {
      planning = false
    }
  }

  $effect(() => {
    if (open) void plan()
  })

  async function wait(job: Job): Promise<Job> {
    let j = job
    while (j.state === 'running') {
      await new Promise((r) => setTimeout(r, 1500))
      j = await api.job(job.id)
    }
    return j
  }

  async function run() {
    running = true
    for (const st of steps) {
      if (st.state !== 'planned' || !st.target) continue
      st.state = 'moving'
      try {
        const j = await wait(await api.move(st.uuid, st.target, ''))
        st.state = j.state === 'succeeded' ? 'done' : 'failed'
        st.message = j.state === 'succeeded' ? undefined : j.message
      } catch (e) {
        st.state = 'failed'
        st.message = errorText(e)
      }
    }
    running = false
    const failed = steps.filter((s) => s.state === 'failed' || s.state === 'skipped').length
    toasts.push({ kind: failed ? 'warn' : 'info', title: failed ? t('{failed} machine(s) still on this host', { failed: failed }) : t('Host is empty'), detail: failed ? undefined : t('It can be shut down for maintenance.') })
  }
</script>

<Dialog bind:open title={t('Maintenance: move all running machines away')} width={640} onclose={() => !running && (open = false)}>
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if planning}
    <p class="muted">{t('Asking every member how much memory it has free…')}</p>
  {:else if steps.length === 0}
    <p class="muted">{t('No machine runs on this host.')}</p>
  {:else}
    <p class="muted">{t('Live migration, one machine at a time, biggest first. Stopped machines stay (they are only defined here). Disks on local storage are copied during the move.')}</p>
    <table class="t">
      <thead><tr><th>{t('Machine')}</th><th>{t('Memory')}</th><th>{t('To')}</th><th></th></tr></thead>
      <tbody>
        {#each steps as s (s.uuid)}
          <tr>
            <td>{s.name}</td>
            <td class="mono">{formatKiB(s.memKiB)}</td>
            <td class:bad={!!s.problem}>{s.targetName ?? s.problem}</td>
            <td class="st {s.state}">{s.state}{#if s.message}<div class="small">{s.message}</div>{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" disabled={running} onclick={() => (open = false)}>{t('Close')}</button>
    <button class="ui-btn primary" disabled={running || planning || !steps.some((s) => s.state === 'planned')} onclick={run}>{running ? 'Moving…' : t('Start moving')}</button>
  {/snippet}
</Dialog>

<style>
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 500 12px var(--font-sans); color: var(--text-2); padding: 6px 8px; border-bottom: 1px solid var(--border); }
  .t td { padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
  .st { font: 500 12px var(--font-sans); }
  .st.done { color: var(--ok); }
  .st.failed, .bad { color: var(--danger); }
  .st.moving { color: var(--warn); }
  .muted { color: var(--text-2); }
  .small { font-size: 11px; font-weight: 400; text-transform: none; }
</style>
