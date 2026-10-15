<script lang="ts">
  // Admins: sign-in with LDAP and OpenID Connect (ADR-0023). Local accounts always keep
  // working, so a wrong setting here can never lock anyone out.
  import Network from '@lucide/svelte/icons/network'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import { api, errorText, type GroupRoles, type SigninConfig } from '../api'
  import { copy } from '../machine'
  import { toasts } from '../toast.svelte'

  let cfg = $state<SigninConfig | null>(null)
  let redirectUrl = $state('')
  let saved = $state('')
  let error = $state('')
  let saving = $state(false)
  let unavailable = $state('')

  $effect(() => {
    api
      .signinConfig()
      .then((r) => {
        cfg = r.config
        redirectUrl = r.redirectUrl
        saved = JSON.stringify(r.config)
      })
      .catch((e) => (unavailable = errorText(e)))
  })
  let dirty = $derived(!!cfg && JSON.stringify(cfg) !== saved)

  // Group lists are edited as comma-separated text.
  const join = (xs: string[] | undefined) => (xs ?? []).join(', ')
  const split = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean)
  function setRoles(r: GroupRoles, key: keyof GroupRoles, v: string) {
    r[key] = split(v)
  }

  async function save() {
    if (!cfg) return
    saving = true
    error = ''
    try {
      const r = await api.saveSigninConfig($state.snapshot(cfg) as SigninConfig)
      cfg = r.config
      saved = JSON.stringify(r.config)
      toasts.push({ kind: 'info', title: 'Sign-in settings saved' })
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = false
    }
  }
</script>

{#if unavailable}
  <!-- -auth none: nothing to configure -->
{:else if cfg}
  <section class="ui-panel card">
    <h2>Sign-in</h2>
    <p class="hint">Besides accounts on this host (always on), people can sign in with your directory. They appear as <span class="mono">name@ldap</span> or <span class="mono">name@sso</span>; their role comes from their groups.</p>

    <div class="block">
      <label class="ui-switch head"><input type="checkbox" bind:checked={cfg.ldap.enabled} /> <Network size={15} /> <b>LDAP / Active Directory</b></label>
      {#if cfg.ldap.enabled}
        <div class="grid2">
          <label class="ui-field"><span class="label">Server</span><input class="ui-input mono" bind:value={cfg.ldap.url} placeholder="ldaps://dc1.example.com" /></label>
          <div class="checks">
            <label class="ui-switch"><input type="checkbox" bind:checked={cfg.ldap.startTLS} /> StartTLS (for ldap://)</label>
            <label class="ui-switch"><input type="checkbox" bind:checked={cfg.ldap.insecureSkipVerify} /> Accept a self-signed certificate</label>
          </div>
          <label class="ui-field"><span class="label">Search as (bind DN)</span><input class="ui-input mono" bind:value={cfg.ldap.bindDN} placeholder="cn=virtomir,ou=services,dc=example,dc=com" /></label>
          <label class="ui-field"><span class="label">Its password</span><input class="ui-input mono" type="password" bind:value={cfg.ldap.bindPassword} autocomplete="off" /></label>
          <label class="ui-field"><span class="label">Users under</span><input class="ui-input mono" bind:value={cfg.ldap.userBase} placeholder="ou=people,dc=example,dc=com" /></label>
          <label class="ui-field"><span class="label">User filter</span><input class="ui-input mono" bind:value={cfg.ldap.userFilter} placeholder="(uid=%s) or (sAMAccountName=%s)" /></label>
          <label class="ui-field"><span class="label">Groups attribute</span><input class="ui-input mono" bind:value={cfg.ldap.groupAttr} placeholder="memberOf" /></label>
        </div>
        {@render roles(cfg.ldap.roles)}
      {/if}
    </div>

    <div class="block">
      <label class="ui-switch head"><input type="checkbox" bind:checked={cfg.oidc.enabled} /> <KeyRound size={15} /> <b>Single sign-on (OpenID Connect)</b></label>
      {#if cfg.oidc.enabled}
        <p class="hint">Register VirtoMir at your provider (Keycloak, Authentik, Entra ID, Google…) with this redirect URL:
          <button class="link mono" onclick={() => copy(redirectUrl, 'Redirect URL')}>{redirectUrl}</button></p>
        <div class="grid2">
          <label class="ui-field"><span class="label">Issuer</span><input class="ui-input mono" bind:value={cfg.oidc.issuer} placeholder="https://idp.example.com/realms/main" /></label>
          <label class="ui-field"><span class="label">Button text</span><input class="ui-input" bind:value={cfg.oidc.label} /></label>
          <label class="ui-field"><span class="label">Client ID</span><input class="ui-input mono" bind:value={cfg.oidc.clientId} placeholder="virtomir" /></label>
          <label class="ui-field"><span class="label">Client secret</span><input class="ui-input mono" type="password" bind:value={cfg.oidc.clientSecret} autocomplete="off" /></label>
          <label class="ui-field"><span class="label">User name claim</span><input class="ui-input mono" bind:value={cfg.oidc.usernameClaim} placeholder="preferred_username" /></label>
          <label class="ui-field"><span class="label">Groups claim</span><input class="ui-input mono" bind:value={cfg.oidc.groupsClaim} placeholder="groups" /></label>
        </div>
        {@render roles(cfg.oidc.roles)}
      {/if}
    </div>

    {#if error}<div class="ui-error-box">{error}</div>{/if}
    <div class="actions"><button class="ui-btn primary" disabled={!dirty || saving} onclick={save}>{saving ? 'Saving…' : 'Save'}</button></div>
  </section>
{/if}

{#snippet roles(r: GroupRoles)}
  <div class="grid3">
    {#each [['admin', 'Admins'], ['operator', 'Operators'], ['viewer', 'Viewers']] as [key, label] (key)}
      <label class="ui-field">
        <span class="label">{label} — groups</span>
        <input class="ui-input mono" value={join(r[key as keyof GroupRoles])} oninput={(e) => setRoles(r, key as keyof GroupRoles, e.currentTarget.value)} placeholder="vm-{key}s" />
      </label>
    {/each}
  </div>
{/snippet}

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 16px 18px;
    margin-bottom: 14px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .block {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .grid3 {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px;
  }
  .checks {
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    gap: 6px;
  }
  .link {
    padding: 0;
    border: none;
    background: none;
    color: var(--accent);
    cursor: pointer;
    font-size: 12px;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
  }
  @media (max-width: 760px) {
    .grid2,
    .grid3 {
      grid-template-columns: 1fr;
    }
  }
</style>
