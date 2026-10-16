<script lang="ts">
  // Two-step sign-in for one's own account (authenticator app + recovery codes), and for
  // admins: who has it, and a reset for someone who lost their phone.
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import ShieldOff from '@lucide/svelte/icons/shield-off'
  import Copy from '@lucide/svelte/icons/copy'
  import { api, errorText, type TwoFactorStatus } from '../api'
  import { copy } from '../machine'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from './Dialog.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  let status = $state<TwoFactorStatus | null>(null)
  let users = $state<string[]>([])
  let error = $state('')

  async function load() {
    try {
      status = await api.twoFactor()
      if (session.can('admin') && status.available) users = await api.twoFactorUsers()
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void load()
  })

  // ---- Setup ---------------------------------------------------------------------------
  let dlg = $state(false)
  let setup = $state<{ secret: string; uri: string; qr: string } | null>(null)
  let code = $state('')
  let recovery = $state<string[]>([])
  let busy = $state(false)
  let formErr = $state('')
  let disabling = $state(false)

  async function start() {
    dlg = true
    disabling = false
    setup = null
    recovery = []
    code = ''
    formErr = ''
    try {
      setup = await api.twoFactorSetup()
    } catch (e) {
      formErr = errorText(e)
    }
  }

  function startDisable() {
    dlg = true
    disabling = true
    code = ''
    formErr = ''
    recovery = []
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    formErr = ''
    try {
      if (disabling) {
        await api.twoFactorDisable(code.trim())
        toasts.push({ kind: 'info', title: 'Two-step sign-in is off' })
        dlg = false
      } else {
        recovery = (await api.twoFactorConfirm(code.trim())).recovery
        toasts.push({ kind: 'info', title: 'Two-step sign-in is on' })
      }
      await load()
    } catch (err) {
      formErr = errorText(err)
    } finally {
      busy = false
    }
  }

  const date = (s?: string) => (s ? new Date(s).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '')
</script>

{#if status?.available}
  <section class="ui-panel card">
    <header class="head">
      <div>
        <h2><ShieldCheck size={16} /> Two-step sign-in</h2>
        <p class="hint">
          {#if status.enabled}
            On since {date(status.since)} · {status.recoveryLeft} recovery codes left. Signing in asks for a code from your authenticator app.
          {:else}
            Off. With it on, signing in needs your password <em>and</em> a code from an app like Google Authenticator, Aegis or 1Password.
          {/if}
        </p>
      </div>
      {#if status.enabled}
        <button class="ui-btn" onclick={startDisable}><ShieldOff size={15} /> Turn off</button>
      {:else}
        <button class="ui-btn primary" onclick={start}><ShieldCheck size={15} /> Turn on</button>
      {/if}
    </header>
    {#if error}<div class="ui-error-box">{error}</div>{/if}

    {#if session.can('admin') && users.length > 0}
      <div class="users">
        <span class="label">Accounts with two-step sign-in on this host</span>
        {#each users as u (u)}
          <div class="user">
            <span class="mono">{u}</span>
            {#if u !== session.info?.user}
              <ConfirmButton
                label="Reset"
                confirm="Turn off for {u}?"
                failure="Could not reset {u}"
                run={async () => {
                  await api.twoFactorReset(u)
                  toasts.push({ kind: 'info', title: `Two-step sign-in turned off for ${u}` })
                  await load()
                }}
              />
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </section>
{/if}

<Dialog open={dlg} title={disabling ? 'Turn off two-step sign-in' : recovery.length ? 'Save your recovery codes' : 'Turn on two-step sign-in'} onclose={() => (dlg = false)} width={480}>
  {#if recovery.length}
    <div class="rec">
      <p class="warn">Each code signs you in once if your phone is gone. Keep them somewhere safe — they are not shown again.</p>
      <div class="codes mono">
        {#each recovery as r (r)}<span>{r}</span>{/each}
      </div>
      <button class="ui-btn" onclick={() => copy(recovery.join('\n'), 'Recovery codes')}><Copy size={14} /> Copy all</button>
    </div>
  {:else}
    <form id="tf-form" class="form" onsubmit={submit}>
      {#if !disabling}
        {#if setup}
          <ol class="steps">
            <li>Scan this with your authenticator app:</li>
          </ol>
          <img class="qr" src={setup.qr} alt="QR code for the authenticator app" width="200" height="200" />
          <p class="hint">Can't scan? Enter this key: <span class="mono key">{setup.secret.replace(/(.{4})/g, '$1 ').trim()}</span></p>
          <ol class="steps" start="2"><li>Enter the 6-digit code it shows:</li></ol>
        {:else if !formErr}
          <p class="hint">Preparing…</p>
        {/if}
      {:else}
        <p class="hint">Enter a current code from your authenticator app (or a recovery code) to confirm it's you.</p>
      {/if}
      {#if disabling || setup}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="ui-input mono code" bind:value={code} autocomplete="one-time-code" inputmode="numeric" maxlength="11" required autofocus aria-label="Code" />
      {/if}
      {#if formErr}<div class="ui-error-box">{formErr}</div>{/if}
    </form>
  {/if}
  {#snippet footer()}
    {#if recovery.length}
      <button class="ui-btn primary" onclick={() => (dlg = false)}>I saved them</button>
    {:else}
      <button class="ui-btn ghost" onclick={() => (dlg = false)}>Cancel</button>
      <button class="ui-btn {disabling ? 'danger' : 'primary'}" form="tf-form" disabled={busy || code.trim().length < 6}>
        {busy ? 'Checking…' : disabling ? 'Turn off' : 'Turn on'}
      </button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .card {
    padding: 16px 18px;
    margin-bottom: 16px;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .head h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 600;
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .users {
    display: grid;
    gap: 4px;
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }
  .label {
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
  }
  .user {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 34px;
    font-size: 13px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .steps {
    margin: 0;
    padding-left: 18px;
    font-size: 13px;
  }
  .qr {
    align-self: center;
    border-radius: var(--radius-sm);
    background: #fff;
    padding: 8px;
  }
  .key {
    color: var(--text);
    user-select: all;
  }
  .code {
    font-size: 18px;
    text-align: center;
  }
  .rec {
    display: flex;
    flex-direction: column;
    gap: 12px;
    align-items: flex-start;
  }
  .warn {
    margin: 0;
    color: var(--warn);
    font-size: 13px;
  }
  .codes {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px 24px;
    width: 100%;
    padding: 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font-size: 14px;
  }
</style>
