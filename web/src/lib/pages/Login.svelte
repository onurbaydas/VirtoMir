<script lang="ts">
  import LogIn from '@lucide/svelte/icons/log-in'
  import { errorText } from '../api'
  import { session } from '../session.svelte'

  let user = $state('')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await session.login(user.trim(), password)
    } catch (err) {
      error = errorText(err)
      password = ''
    } finally {
      busy = false
    }
  }
</script>

<svelte:head><title>Sign in · VirtoMir</title></svelte:head>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="brand">
      <img src="/favicon.svg" alt="" width="40" height="40" />
      <div>
        <div class="wordmark">VirtoMir</div>
        <div class="tagline">Sign in with your account on this host</div>
      </div>
    </div>

    <label class="ui-field">
      <span class="label">User name</span>
      <!-- svelte-ignore a11y_autofocus -->
      <input class="ui-input" bind:value={user} autocomplete="username" autocapitalize="off" spellcheck="false" required autofocus />
    </label>
    <label class="ui-field">
      <span class="label">Password</span>
      <input class="ui-input" type="password" bind:value={password} autocomplete="current-password" required />
    </label>

    {#if error}<div class="ui-error-box" role="alert">{error}</div>{/if}

    <button class="ui-btn primary submit" disabled={busy || !user || !password}>
      <LogIn size={15} />
      {busy ? 'Signing in…' : 'Sign in'}
    </button>
    <p class="note">Root and members of the <span class="mono">libvirt</span> group can sign in.</p>
  </form>
</div>

<style>
  .wrap {
    display: grid;
    place-items: center;
    min-height: 100vh;
    padding: 24px 16px;
    background:
      radial-gradient(900px 500px at 15% -10%, var(--accent-soft), transparent 60%),
      radial-gradient(700px 420px at 110% 110%, color-mix(in srgb, var(--accent-2) 12%, transparent), transparent 60%),
      var(--bg);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    width: min(380px, 100%);
    padding: 26px 24px 20px;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-pop);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 8px;
  }
  .wordmark {
    font-size: 19px;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .tagline {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .submit {
    height: 38px;
    margin-top: 4px;
  }
  .note {
    margin: 2px 0 0;
    color: var(--text-3);
    font-size: 12px;
    text-align: center;
  }
</style>
