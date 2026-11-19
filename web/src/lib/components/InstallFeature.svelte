<script lang="ts">
  // What a feature is missing on this host, and one button that installs it: VirtoMir runs the
  // package manager itself and follows the install as a task. Nobody types apt or dnf.
  import Download from '@lucide/svelte/icons/download'
  import { api, errorText, type SetupFeature } from '../api'
  import { t } from '../i18n.svelte'
  import { track } from '../jobs'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let {
    feature,
    missing,
    host,
    oninstalled,
  }: {
    feature: SetupFeature
    /** One sentence: what is missing and what it is needed for. */
    missing: string
    host?: string
    oninstalled?: () => void
  } = $props()

  let busy = $state(false)

  async function install() {
    busy = true
    try {
      const job = await api.installFeature(feature, host)
      track(job, {
        success: t('Installed: {what}', { what: job.title.replace(/^Install /, '') }),
        host,
        onSuccess: () => {
          busy = false
          oninstalled?.()
        },
        onFailure: () => (busy = false),
      })
    } catch (e) {
      busy = false
      toasts.push({ kind: 'error', title: t('Not installed'), detail: errorText(e) })
    }
  }
</script>

<div class="install">
  <p>{missing}</p>
  {#if session.can('admin')}
    <button class="ui-btn sm primary" onclick={install} disabled={busy}>
      <Download size={14} />
      {busy ? t('Installing…') : t('Install it')}
    </button>
  {:else}
    <p class="hint">{t('An admin can install it with one click.')}</p>
  {/if}
</div>

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
