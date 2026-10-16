<script lang="ts">
  import { t } from '../i18n.svelte'
  // Deleting a machine is the one action that asks for its name (P1: friction only where
  // the loss is permanent). Force off and hard reset confirm with a second click instead.
  import Dialog from './Dialog.svelte'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import Trash from '@lucide/svelte/icons/trash'

  interface Props {
    open: boolean
    targetName: string
    targetUuid?: string
    onconfirm: (opts: { purgeDisks: boolean }) => void
    onclose: () => void
  }

  let { open, targetName, targetUuid, onconfirm, onclose }: Props = $props()

  let typedName = $state('')
  let purgeDisks = $state(false)
  $effect(() => {
    if (open) {
      typedName = ''
      purgeDisks = false
    }
  })

  let canConfirm = $derived(typedName.trim() === targetName.trim())

  function confirm() {
    if (!canConfirm) return
    onconfirm({ purgeDisks })
  }
</script>

<Dialog {open} title="" {onclose} width={540}>
  <div class="guardrail-dialog">
    <!-- Hazard Stripes Warning Banner -->
    <div class="hazard-stripes banner-header">
      <div class="banner-content">
        <TriangleAlert size={20} />
        <span class="banner-title">{t('This cannot be undone')}</span>
      </div>
    </div>

    <div class="guardrail-body">
      <div class="action-alert">
        <p class="lead-warn">
          {t('You are about to')} <strong>{t('delete')}</strong> {t('machine')}
          <span class="target-chip">{targetName}</span>.
        </p>
        <p class="sub-warn">{t('Its definition, snapshots and (if ticked) its disk images are removed. Backups stay.')}</p>
      </div>

      {#if targetUuid}
        <div class="uuid-bar">
          <span class="lbl">{t('UUID')}</span>
          <span class="val">{targetUuid}</span>
        </div>
      {/if}

      <div class="checkbox-group">
        <label class="check-row">
          <input type="checkbox" bind:checked={purgeDisks} />
          <span>{t('Also delete its own disk images (shared images, ISOs and template disks are kept)')}</span>
        </label>
      </div>

      <div class="verification-box">
        <label for="verify-input" class="verify-label">
          {t('To proceed, type exact machine name')} <strong>"{targetName}"</strong>:
        </label>
        <input
          id="verify-input"
          class="verify-input"
          type="text"
          bind:value={typedName}
          placeholder={targetName}
          autocomplete="off"
          spellcheck="false"
        />
      </div>
    </div>

    <div class="guardrail-footer">
      <button class="ui-btn" onclick={onclose}>{t('Cancel')}</button>
      <button
        class="ui-btn danger-confirm"
        disabled={!canConfirm}
        onclick={confirm}
      >
        {#if !canConfirm}
          <span>{t('Type the name to continue')}</span>
        {:else}
          <Trash size={14} />
          <span>{t('Delete for good')}</span>
        {/if}
      </button>
    </div>
  </div>
</Dialog>

<style>
  .guardrail-dialog {
    display: flex;
    flex-direction: column;
    margin: -16px -20px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
  }
  .banner-header {
    height: 38px;
    display: flex;
    align-items: center;
    padding: 0 16px;
    border-bottom: 2px solid var(--ink);
  }
  .banner-content {
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--bg);
    padding: 2px 10px;
    border: 1px solid var(--ink);
    box-shadow: 2px 2px 0 var(--ink);
    color: var(--danger);
    font-weight: 800;
  }
  .guardrail-body {
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .lead-warn {
    margin: 0 0 6px;
    font-size: 14.5px;
    color: var(--text);
  }
  .sub-warn {
    margin: 0;
    font-size: 12.5px;
    color: var(--text-2);
    line-height: 1.5;
  }
  .target-chip {
    padding: 2px 6px;
    background: color-mix(in srgb, var(--danger) 15%, var(--surface));
    border: 1px solid var(--danger);
    color: var(--danger);
    font-weight: 700;
  }
  .uuid-bar {
    display: flex;
    gap: 8px;
    font-size: 11px;
    padding: 6px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-3);
  }
  .uuid-bar .val {
    color: var(--text-2);
  }
  .checkbox-group {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 12px;
  }
  .check-row {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    user-select: none;
    color: var(--text);
  }
  .check-row input[type='checkbox'] {
    width: 16px;
    height: 16px;
    accent-color: var(--danger);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .verification-box {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .verify-label {
    font-size: 12px;
    color: var(--text-2);
  }
  .verify-input {
    width: 100%;
    height: 36px;
    padding: 0 12px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    color: var(--text);
    font-size: 13.5px;
    box-shadow: var(--bevel-in);
  }
  .verify-input:focus {
    outline: none;
    border-color: var(--danger);
    box-shadow: 0 0 0 1px var(--danger);
  }
  .guardrail-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 12px;
    padding: 14px 24px;
    background: var(--surface);
    border-top: 1px solid var(--border-strong);
  }
  .danger-confirm {
    background: var(--danger);
    color: #fff;
    border: 1px solid var(--ink);
    box-shadow: 2px 2px 0 var(--ink);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 36px;
    padding: 0 16px;
    font-weight: 700;
    font-size: 12px;
    cursor: pointer;
  }
  .danger-confirm:disabled {
    opacity: 0.5;
    background: var(--metal);
    color: var(--text-3);
    cursor: not-allowed;
    border-color: var(--border-strong);
    box-shadow: none;
  }
</style>
