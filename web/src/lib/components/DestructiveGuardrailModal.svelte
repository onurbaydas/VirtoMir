<script lang="ts">
  import { onDestroy } from 'svelte'
  import Dialog from './Dialog.svelte'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import Trash from '@lucide/svelte/icons/trash'
  import Zap from '@lucide/svelte/icons/zap'

  interface Props {
    open: boolean
    action: 'delete' | 'force-off' | 'hard-reset'
    targetName: string
    targetUuid?: string
    onconfirm: (opts: { purgeDisks: boolean; purgeBackups: boolean }) => void
    onclose: () => void
  }

  let { open, action, targetName, targetUuid, onconfirm, onclose }: Props = $props()

  let typedName = $state('')
  let purgeDisks = $state(true)
  let purgeBackups = $state(true)
  let countdown = $state(5)
  let timer: ReturnType<typeof setInterval> | undefined

  $effect(() => {
    if (open) {
      typedName = ''
      purgeDisks = true
      purgeBackups = true
      countdown = 5
      clearInterval(timer)
      timer = setInterval(() => {
        if (countdown > 0) {
          countdown -= 1
        } else {
          clearInterval(timer)
        }
      }, 1000)
    } else {
      clearInterval(timer)
    }
  })

  onDestroy(() => clearInterval(timer))

  let nameMatches = $derived(typedName.trim() === targetName.trim())
  let canConfirm = $derived(countdown === 0 && nameMatches)

  function confirm() {
    if (!canConfirm) return
    onconfirm({ purgeDisks, purgeBackups })
  }
</script>

<Dialog {open} title="" {onclose} width={540}>
  <div class="guardrail-dialog">
    <!-- Hazard Stripes Warning Banner -->
    <div class="hazard-stripes banner-header">
      <div class="banner-content">
        <TriangleAlert size={20} />
        <span class="banner-title mono">DESTRUCTIVE ACTION SAFEGUARD</span>
      </div>
    </div>

    <div class="guardrail-body">
      <div class="action-alert">
        {#if action === 'delete'}
          <p class="lead-warn">
            You are about to <strong>PERMANENTLY DESTROY</strong> machine
            <span class="target-chip mono">{targetName}</span>.
          </p>
          <p class="sub-warn">
            This operation cannot be undone. All active memory state, guest definitions, and selected storage volumes will be completely eradicated from libvirt.
          </p>
        {:else if action === 'force-off'}
          <p class="lead-warn">
            You are about to <strong>FORCE POWER OFF (PULL PLUG)</strong> on
            <span class="target-chip mono">{targetName}</span>.
          </p>
          <p class="sub-warn">
            Immediate QEMU process termination (SIGKILL). Unflushed write buffers and database transactions in the guest will be corrupted or lost.
          </p>
        {:else}
          <p class="lead-warn">
            You are about to execute a <strong>HARD COLD RESET</strong> on
            <span class="target-chip mono">{targetName}</span>.
          </p>
          <p class="sub-warn">
            Instant CPU reset vector trigger without graceful OS ACPI signal.
          </p>
        {/if}
      </div>

      {#if targetUuid}
        <div class="uuid-bar mono">
          <span class="lbl">UUID:</span>
          <span class="val">{targetUuid}</span>
        </div>
      {/if}

      {#if action === 'delete'}
        <div class="checkbox-group mono">
          <label class="check-row">
            <input type="checkbox" bind:checked={purgeDisks} />
            <span>Permanently delete all attached virtual disks (QCOW2/RAW)</span>
          </label>
          <label class="check-row">
            <input type="checkbox" bind:checked={purgeBackups} />
            <span>Remove all scheduled snapshots and local retention backups</span>
          </label>
        </div>
      {/if}

      <div class="verification-box">
        <label for="verify-input" class="verify-label mono">
          To proceed, type exact machine name <strong>"{targetName}"</strong>:
        </label>
        <input
          id="verify-input"
          class="verify-input mono"
          type="text"
          bind:value={typedName}
          placeholder={targetName}
          autocomplete="off"
          spellcheck="false"
        />
      </div>
    </div>

    <div class="guardrail-footer">
      <button class="ui-btn" onclick={onclose}>CANCEL</button>
      <button
        class="ui-btn danger-confirm mono"
        class:locked={!canConfirm}
        disabled={!canConfirm}
        onclick={confirm}
      >
        {#if countdown > 0}
          <span class="timer-badge">UNLOCKING IN {countdown}S</span>
        {:else if !nameMatches}
          <span>TYPE NAME TO UNLOCK</span>
        {:else}
          {#if action === 'delete'}
            <Trash size={14} />
            <span>CONFIRM PERMANENT DELETION</span>
          {:else}
            <Zap size={14} />
            <span>EXECUTE FORCED ACTION</span>
          {/if}
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
    letter-spacing: 0.08em;
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
    border-radius: 0;
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
    background: var(--bg-surface, var(--surface));
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
    letter-spacing: 0.05em;
    cursor: pointer;
  }
  .danger-confirm.locked,
  .danger-confirm:disabled {
    opacity: 0.5;
    background: var(--metal);
    color: var(--text-3);
    cursor: not-allowed;
    border-color: var(--border-strong);
    box-shadow: none;
  }
  .timer-badge {
    color: var(--warn);
    font-weight: 800;
  }
</style>
