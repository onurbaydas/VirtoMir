<script lang="ts">
  import LogIn from '@lucide/svelte/icons/log-in'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import { api, errorText } from '../api'
  import { session } from '../session.svelte'

  let user = $state('')
  let password = $state('')
  // A failed single sign-on comes back as ?signin_error=…
  let error = $state(new URLSearchParams(location.search).get('signin_error') ?? '')
  let methods = $state<{ ldap: boolean; oidc: boolean; oidcLabel?: string }>({ ldap: false, oidc: false })
  $effect(() => {
    api
      .authMethods()
      .then((m) => (methods = m))
      .catch(() => {})
    if (location.search.includes('signin_error')) history.replaceState(null, '', location.pathname)
  })
  let busy = $state(false)
  // Two-factor sign-in: after the password, the code from the authenticator app.
  let pending = $state('')
  let code = $state('')

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      if (pending) {
        await session.loginCode(pending, code.trim())
      } else {
        const p = await session.login(user.trim(), password)
        if (p) {
          pending = p
          code = ''
        }
      }
    } catch (err) {
      error = errorText(err)
      if (pending) {
        code = ''
        if (/expired/.test(error)) pending = ''
      } else password = ''
    } finally {
      busy = false
    }
  }

  function back() {
    pending = ''
    password = ''
    error = ''
  }
</script>

<svelte:head><title>Sign in · VirtoMir</title></svelte:head>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="brand">
      <img src="/favicon.svg" alt="" width="40" height="40" />
      <div>
        <div class="wordmark">VirtoMir</div>
        <div class="tagline">Sign in with your account on this host{methods.ldap ? ' or in the directory' : ''}</div>
      </div>
    </div>

    {#if pending}
      <div class="code-step">
        <ShieldCheck size={22} />
        <div>
          <strong>Two-step sign-in</strong>
          <p>Enter the 6-digit code from your authenticator app for <span class="mono">{user}</span>, or one of your recovery codes.</p>
        </div>
      </div>
      <label class="ui-field">
        <span class="label">Code</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="ui-input mono code" bind:value={code} autocomplete="one-time-code" inputmode="numeric" spellcheck="false" maxlength="11" required autofocus />
      </label>
    {:else}
      <label class="ui-field">
        <span class="label">User name</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="ui-input" bind:value={user} autocomplete="username" autocapitalize="off" spellcheck="false" required autofocus />
      </label>
      <label class="ui-field">
        <span class="label">Password</span>
        <input class="ui-input" type="password" bind:value={password} autocomplete="current-password" required />
      </label>
    {/if}

    {#if error}<div class="ui-error-box" role="alert">{error}</div>{/if}

    <button class="ui-btn primary submit" disabled={busy || (pending ? code.trim().length < 6 : !user || !password)}>
      <LogIn size={15} />
      {busy ? 'Signing in…' : pending ? 'Verify' : 'Sign in'}
    </button>
    {#if pending}
      <button type="button" class="ui-btn ghost" onclick={back}>Use another account</button>
    {:else if methods.oidc}
      <div class="or"><span>or</span></div>
      <a class="ui-btn sso" href="/api/v1/auth/oidc/start"><KeyRound size={15} /> {methods.oidcLabel || 'Sign in with single sign-on'}</a>
    {/if}
    <p class="note">Root and members of the <span class="mono">libvirt</span> group can sign in{methods.ldap || methods.oidc ? ', and directory users whose groups have a role' : ''}.</p>
  </form>
</div>

<style>
  .wrap {
    display: grid;
    place-items: center;
    min-height: 100vh;
    padding: 24px 16px;
    background: radial-gradient(60rem 30rem at 50% -10%, var(--accent-soft), transparent 70%), var(--bg);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    width: min(380px, 100%);
    padding: 28px 28px 22px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 0 0 10px;
  }
  .wordmark {
    font-size: 20px;
    font-weight: 650;
    line-height: 1.2;
    letter-spacing: -0.01em;
  }
  .tagline {
    color: var(--text-3);
    font-size: 13px;
  }
  .submit {
    height: 38px;
    margin-top: 4px;
  }
  .or {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-3);
    font-size: 12px;
  }
  .or::before,
  .or::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border);
  }
  .sso {
    justify-content: center;
    height: 38px;
    text-decoration: none;
  }
  .code-step {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    color: var(--accent);
  }
  .code-step strong {
    color: var(--text);
    font-size: 14px;
  }
  .code-step p {
    margin: 2px 0 0;
    color: var(--text-2);
    font-size: 12.5px;
  }
  .code {
    font-size: 18px;
    text-align: center;
  }
  .note {
    margin: 2px 0 0;
    color: var(--text-3);
    font-size: 12px;
    text-align: center;
  }
</style>
