<script lang="ts">
  // The IOMMU is off: one button puts the right parameters on the kernel command line (VirtoMir
  // picks Intel or AMD and the boot loader itself). Only the firmware switch and the reboot are
  // left, and the result says so.
  import Wrench from '@lucide/svelte/icons/wrench'
  import { api, errorText, type IOMMUResult } from '../api'
  import { t } from '../i18n.svelte'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let busy = $state(false)
  let done = $state<IOMMUResult | null>(null)

  async function enable() {
    busy = true
    try {
      done = await api.enableIOMMU()
      toasts.push({ kind: 'info', title: t('IOMMU is set up; reboot the host to finish') })
    } catch (e) {
      toasts.push({ kind: 'error', title: t('The IOMMU could not be turned on'), detail: errorText(e) })
    } finally {
      busy = false
    }
  }
</script>

{#if done}
  <p class="done">
    {done.params.length
      ? t('Added {params} to the kernel command line.', { params: done.params.join(' ') })
      : t('The kernel command line already has it.')}
    {t('Reboot the host to finish. If the IOMMU is still off after that, turn on VT-d (Intel) or AMD-Vi / SVM in the firmware settings: that switch cannot be set from Linux.')}
    {#if done.backup}<span class="hint">{t('The previous file is kept as {file}.', { file: done.backup })}</span>{/if}
  </p>
{:else if session.can('admin')}
  <button class="ui-btn sm primary" onclick={enable} disabled={busy}>
    <Wrench size={14} />
    {busy ? t('Turning it on…') : t('Turn on the IOMMU')}
  </button>
{:else}
  <span class="hint">{t('An admin can turn it on with one click.')}</span>
{/if}

<style>
  .done {
    margin: 6px 0 0;
  }
  .hint {
    display: block;
    color: var(--text-2);
    font-size: 12px;
  }
</style>
