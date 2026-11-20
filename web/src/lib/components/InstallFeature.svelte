<script lang="ts">
  // What a feature is missing, and one button that installs it: VirtoMir runs the package
  // manager itself and follows each install as a task. In a cluster it installs on every
  // member that lacks it (everywhere), so nobody opens host after host. Nobody types apt or dnf.
  // With installedHere the open host has it already: in a cluster the members that still lack it
  // are named (nothing is shown when none does).
  import Download from '@lucide/svelte/icons/download'
  import { api, errorText, type Job, type SetupFeature } from '../api'
  import { cluster } from '../cluster.svelte'
  import { t } from '../i18n.svelte'
  import { track } from '../jobs'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let {
    feature,
    missing,
    host,
    everywhere = true,
    installedHere = false,
    oninstalled,
  }: {
    feature: SetupFeature
    /** One sentence: what is missing and what it is needed for. */
    missing: string
    /** The member the page is about (undefined: the one being shown). */
    host?: string
    /** Install on every cluster member that lacks it, not only on this one. */
    everywhere?: boolean
    /** The open host has it; only the other members that lack it matter. */
    installedHere?: boolean
    oninstalled?: () => void
  } = $props()

  let running = $state(0)
  let wide = $derived(everywhere && cluster.clustered)

  // The members to install on: this one, or every reachable member that lacks the feature.
  async function targets(): Promise<{ id: string | undefined; name: string }[]> {
    if (!wide) return [{ id: host, name: cluster.current?.name ?? t('this host') }]
    const members = cluster.members.filter((m) => m.reachable)
    const states = await Promise.all(members.map((m) => api.hostSetup(m.id).catch(() => null)))
    return members.filter((_, i) => !states[i]?.features[feature]?.installed).map((m) => ({ id: m.id, name: m.name }))
  }

  // installedHere: the members that still lack it (looked up once the cluster is known).
  let elsewhere = $state<{ id: string | undefined; name: string }[]>([])
  $effect(() => {
    if (!installedHere || !wide) {
      elsewhere = []
      return
    }
    targets()
      .then((list) => (elsewhere = list))
      .catch(() => (elsewhere = []))
  })

  async function install() {
    let list: { id: string | undefined; name: string }[]
    try {
      list = await targets()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Not installed'), detail: errorText(e) })
      return
    }
    if (!list.length) {
      elsewhere = []
      oninstalled?.()
      return
    }
    running = list.length
    const finished = () => {
      running--
      // Look again either way: the page shows what is still missing (failures have their own toast).
      if (running === 0) {
        if (installedHere) targets().then((l) => (elsewhere = l)).catch(() => {})
        oninstalled?.()
      }
    }
    for (const m of list) {
      let job: Job
      try {
        job = await api.installFeature(feature, m.id)
      } catch (e) {
        toasts.push({ kind: 'error', title: t('Not installed on {host}', { host: m.name }), detail: errorText(e) })
        finished()
        continue
      }
      track(job, {
        success: wide ? t('Installed on {host}', { host: m.name }) : t('Installed: {what}', { what: job.title.replace(/^Install /, '') }),
        host: m.id,
        onSuccess: finished,
        onFailure: finished,
      })
    }
  }
</script>

{#if !installedHere || elsewhere.length}
  <div class="install">
    {#if installedHere}
      <p>{t('Not installed on {hosts} yet; those hosts need it too.', { hosts: elsewhere.map((m) => m.name).join(', ') })}</p>
    {:else}
      <p>{missing}</p>
    {/if}
    {#if session.can('admin')}
      <button class="ui-btn sm primary" onclick={install} disabled={running > 0}>
        <Download size={14} />
        {running > 0 ? t('Installing…') : installedHere ? t('Install on those hosts') : wide ? t('Install on every host') : t('Install it')}
      </button>
    {:else}
      <p class="hint">{t('An admin can install it with one click.')}</p>
    {/if}
  </div>
{/if}

<style>
  .install {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--warn) 45%, transparent);
    background: color-mix(in srgb, var(--warn) 8%, var(--surface));
  }
  .install p {
    margin: 0;
    flex: 1 1 260px;
  }
  .hint {
    color: var(--text-2);
  }
</style>
