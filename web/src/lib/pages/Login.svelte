<script lang="ts">
  import { t } from '../i18n.svelte'
  import Composition from '../components/Composition.svelte'
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

<svelte:head><title>{t('Sign in · VirtoMir')}</title></svelte:head>

<div class="wrap">
  <div class="art"><Composition /><div class="claim">{t('Every')}<br />{t('setting.')}<br />{t('Nothing')}<br />{t('hidden.')}</div></div>
  <form class="card" onsubmit={submit}>
    <div class="brand">
      <img src={import.meta.env.BASE_URL + 'favicon.svg'} alt="" width="40" height="40" />
      <div>
        <div class="wordmark">VirtoMir</div>
        <div class="tagline">{t('Sign in with your account on this host{v}', { v: methods.ldap ? t(' or in the directory') : '' })}</div>
      </div>
    </div>

    {#if pending}
      <div class="code-step">
        <ShieldCheck size={22} />
        <div>
          <strong>{t('Two-step sign-in')}</strong>
          <p>{t('Enter the 6-digit code from your authenticator app for')} <span class="mono">{user}</span>{t(', or one of your recovery codes.')}</p>
        </div>
      </div>
      <label class="ui-field">
        <span class="label">{t('Code')}</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="ui-input mono code" bind:value={code} autocomplete="one-time-code" inputmode="numeric" spellcheck="false" maxlength="11" required autofocus />
      </label>
    {:else}
      <label class="ui-field">
        <span class="label">{t('User name')}</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input class="ui-input" bind:value={user} autocomplete="username" autocapitalize="off" spellcheck="false" required autofocus />
      </label>
      <label class="ui-field">
        <span class="label">{t('Password')}</span>
        <input class="ui-input" type="password" bind:value={password} autocomplete="current-password" required />
      </label>
    {/if}

    {#if error}<div class="ui-error-box" role="alert">{error}</div>{/if}

    <button class="ui-btn primary submit" disabled={busy || (pending ? code.trim().length < 6 : !user || !password)}>
      <LogIn size={15} />
      {busy ? t('Signing in…') : pending ? t('Verify') : t('Sign in')}
    </button>
    {#if pending}
      <button type="button" class="ui-btn ghost" onclick={back}>{t('Use another account')}</button>
    {:else if methods.oidc}
      <div class="or"><span>{t('or')}</span></div>
      <a class="ui-btn sso" href="/api/v1/auth/oidc/start"><KeyRound size={15} /> {methods.oidcLabel || t('Sign in with single sign-on')}</a>
    {/if}
    <p class="note">{methods.ldap || methods.oidc ? t('Root, administrators of this host (groups sudo, wheel or libvirt) and directory users whose groups have a role can sign in.') : t('Root and administrators of this host (groups sudo, wheel or libvirt) can sign in.')}</p>
  </form>
</div>

<style>
  .wrap {
    display: grid;
    grid-template-columns: minmax(0, 1.15fr) minmax(360px, 1fr);
    min-height: 100vh;
    background: var(--bg);
  }
  .art {
    position: relative;
    min-height: 260px;
    border-right: var(--frame) solid var(--ink);
    overflow: hidden;
  }
  .claim {
    position: absolute;
    left: 36px;
    top: 30px;
    color: #f4f1e8;
    font-family: var(--font-display);
    font-size: clamp(34px, 4.6vw, 64px);
    font-stretch: 62%;
    font-weight: 900;
    line-height: 0.88;
    text-shadow: 4px 4px 0 #000;
  }
  .card {
    display: flex;
    flex-direction: column;
    align-self: center;
    justify-self: center;
    gap: 14px;
    width: min(380px, calc(100% - 32px));
    margin: 32px 0;
    padding: 0 22px 20px;
    background: var(--surface);
    border: var(--frame) solid var(--ink);
    box-shadow: var(--bevel), 8px 8px 0 var(--ink);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 0 -22px 6px;
    padding: 12px 22px;
    border-bottom: var(--frame) solid var(--ink);
    background: #000;
    color: #f4f1e8;
  }
  .brand img {
    border: 2px solid #f4f1e8;
  }
  .wordmark {
    font-family: var(--font-display);
    font-size: 30px;
    font-stretch: 62%;
    font-weight: 900;
    line-height: 1;
  }
  .tagline {
    font-size: 12.5px;
    opacity: 0.8;
  }
  @media (max-width: 820px) {
    .wrap {
      grid-template-columns: 1fr;
    }
    .art {
      height: 220px;
      border-right: 0;
      border-bottom: var(--frame) solid var(--ink);
    }
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
    height: 2px;
    background: var(--ink);
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
