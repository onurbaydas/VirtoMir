<script lang="ts">
  import QuotasCard from '../components/QuotasCard.svelte'
  import SigninCard from '../components/SigninCard.svelte'
  import IntegrationsCard from '../components/IntegrationsCard.svelte'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash from '@lucide/svelte/icons/trash'
  import Copy from '@lucide/svelte/icons/copy'
  import ShieldUser from '@lucide/svelte/icons/shield-user'
  import { api, errorText, type ApiToken, type Role } from '../api'
  import { copy } from '../machine'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import Dialog from '../components/Dialog.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'

  let tokens = $state<ApiToken[] | null>(null)
  let error = $state('')
  let showAll = $state(false)

  async function load() {
    try {
      tokens = await api.tokens(showAll)
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void showAll
    void load()
  })

  const ROLES: { key: Role; label: string; can: string }[] = [
    { key: 'viewer', label: 'Viewer', can: 'Sees machines, metrics and XML. Changes nothing.' },
    { key: 'operator', label: 'Operator', can: 'Also starts and stops machines, opens consoles, changes CDs, takes and reverts snapshots.' },
    { key: 'admin', label: 'Admin', can: 'Also creates, deletes and reconfigures machines, storage and networks.' },
  ]
  let allowedRoles = $derived(ROLES.filter((r) => session.can(r.key)))
  let me = $derived(ROLES.find((r) => r.key === session.role))

  // ---- New token -----------------------------------------------------------------------
  let dlg = $state(false)
  let name = $state('')
  let role = $state<Role>('viewer')
  let ttl = $state(90)
  let busy = $state(false)
  let formErr = $state('')
  let secret = $state('')

  function openNew() {
    name = ''
    role = 'viewer'
    ttl = 90
    formErr = ''
    secret = ''
    dlg = true
  }
  async function create(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    formErr = ''
    try {
      const res = await api.createToken(name, role, ttl)
      secret = res.secret
      await load()
    } catch (err) {
      formErr = errorText(err)
    } finally {
      busy = false
    }
  }

  const date = (s?: string) => (s ? new Date(s).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '—')
  const ago = (s?: string) => {
    if (!s) return 'never'
    const m = Math.round((Date.now() - Date.parse(s)) / 60000)
    return m < 1 ? 'just now' : m < 60 ? `${m} min ago` : m < 1440 ? `${Math.round(m / 60)} h ago` : date(s)
  }
</script>

<svelte:head><title>Account · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Account</h1>
    <p class="lead">Your role and the API tokens your scripts use.</p>
  </div>
</header>

<section class="ui-panel card who">
  <span class="avatar"><ShieldUser size={22} /></span>
  <div>
    <div class="name">{session.info?.user ?? 'Development mode (no sign-in)'}</div>
    <div class="role">{me?.label} · {me?.can}</div>
  </div>
</section>

<QuotasCard />
{#if session.can('admin')}<SigninCard />{/if}
<IntegrationsCard />

<section class="ui-panel card">
  <header class="head">
    <div>
      <h2>API tokens</h2>
      <p class="hint">
        For scripts, CI and tools: send <span class="mono">Authorization: Bearer &lt;token&gt;</span>. A token never has more rights than you,
        and loses them when you do.
      </p>
    </div>
    <div class="head-tools">
      {#if session.can('admin')}
        <label class="ui-switch small"><input type="checkbox" bind:checked={showAll} /> Everyone's</label>
      {/if}
      <button class="ui-btn primary" onclick={openNew} disabled={session.info?.mode === 'none'} title={session.info?.mode === 'none' ? 'Tokens need sign-in to be enabled' : ''}>
        <Plus size={15} /> New token
      </button>
    </div>
  </header>

  {#if error}
    <div class="ui-error-box">{error}</div>
  {:else if tokens === null}
    <p class="muted">Loading…</p>
  {:else if tokens.length === 0}
    <p class="empty"><KeyRound size={20} /> No tokens yet.</p>
  {:else}
    <div class="table" role="table" aria-label="API tokens">
      <div class="row head-row" role="row">
        <span role="columnheader">Name</span>
        <span role="columnheader">Role</span>
        {#if showAll}<span role="columnheader">Owner</span>{/if}
        <span role="columnheader">Last used</span>
        <span role="columnheader">Expires</span>
        <span role="columnheader" class="sr">Actions</span>
      </div>
      {#each tokens as t (t.id)}
        <div class="row" class:all={showAll} role="row">
          <span role="cell" class="tname"><KeyRound size={14} /> {t.name}</span>
          <span role="cell"><span class="badge {t.role}">{t.role}</span></span>
          {#if showAll}<span role="cell">{t.owner}</span>{/if}
          <span role="cell" class="muted">{ago(t.lastUsed)}</span>
          <span role="cell" class="muted" class:soon={!!t.expires && Date.parse(t.expires) - Date.now() < 7 * 864e5}>{t.expires ? date(t.expires) : 'never'}</span>
          <span role="cell" class="act">
            <ConfirmButton
              label="Revoke"
              confirm="Revoke now?"
              icon={Trash}
              failure="Could not revoke {t.name}"
              run={async () => {
                await api.revokeToken(t.id)
                toasts.push({ kind: 'info', title: `Token ${t.name} revoked` })
                await load()
              }}
            />
          </span>
        </div>
      {/each}
    </div>
  {/if}
</section>

<Dialog open={dlg} title={secret ? 'Your new token' : 'New API token'} onclose={() => (dlg = false)} width={520}>
  {#if secret}
    <div class="secret">
      <p>Copy it now — VirtoMir only keeps a hash and cannot show it again.</p>
      <div class="secret-box mono">{secret}</div>
      <button class="ui-btn" onclick={() => copy(secret, 'Token')}><Copy size={14} /> Copy token</button>
      <pre class="mono example">curl -H "Authorization: Bearer {secret.slice(0, 12)}…" {location.origin}/api/v1/domains</pre>
    </div>
  {:else}
    <form id="tok-form" class="form" onsubmit={create}>
      <label class="ui-field">
        <span class="label">Name</span>
        <input class="ui-input" bind:value={name} placeholder="terraform, backup-script, grafana…" required maxlength="64" />
      </label>
      <div class="roles" role="radiogroup" aria-label="Token role">
        {#each allowedRoles as r (r.key)}
          <label class="role-opt" class:on={role === r.key}>
            <input type="radio" name="role" value={r.key} bind:group={role} />
            <strong>{r.label}</strong>
            <span>{r.can}</span>
          </label>
        {/each}
      </div>
      <label class="ui-field">
        <span class="label">Expires</span>
        <select class="ui-input" bind:value={ttl}>
          <option value={7}>in 7 days</option>
          <option value={30}>in 30 days</option>
          <option value={90}>in 90 days</option>
          <option value={365}>in a year</option>
          <option value={0}>never</option>
        </select>
      </label>
      {#if formErr}<div class="ui-error-box">{formErr}</div>{/if}
    </form>
  {/if}
  {#snippet footer()}
    {#if secret}
      <button class="ui-btn primary" onclick={() => (dlg = false)}>Done</button>
    {:else}
      <button class="ui-btn ghost" onclick={() => (dlg = false)}>Cancel</button>
      <button class="ui-btn primary" form="tok-form" disabled={busy}>{busy ? 'Creating…' : 'Create token'}</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .lead,
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 13px;
  }
  .card {
    padding: 16px 18px;
    margin-bottom: 16px;
  }
  .who {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .avatar {
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent);
  }
  .name {
    font-size: 16px;
    font-weight: 600;
  }
  .role {
    color: var(--text-2);
    font-size: 13px;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  .head h2 {
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 600;
  }
  .head-tools {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .small {
    font-size: 12.5px;
    color: var(--text-2);
  }
  .table {
    display: grid;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(160px, 1.5fr) 100px 120px 120px 140px;
    gap: 12px;
    align-items: center;
    min-height: 46px;
    border-top: 1px solid var(--border);
    font-size: 13px;
  }
  .row.all,
  .head-row:has(+ .row.all) {
    grid-template-columns: minmax(160px, 1.5fr) 100px 110px 120px 120px 140px;
  }
  .head-row {
    min-height: 0;
    padding: 8px 0;
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .tname {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 500;
  }
  .badge {
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11.5px;
  }
  .badge.admin {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .badge.operator {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .act {
    display: flex;
    justify-content: flex-end;
  }
  .soon {
    color: var(--warn);
  }
  .muted {
    color: var(--text-3);
  }
  .empty {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 8px 0 4px;
    color: var(--text-3);
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .roles {
    display: grid;
    gap: 8px;
  }
  .role-opt {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    cursor: pointer;
  }
  .role-opt input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .role-opt.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .role-opt:focus-within {
    outline: 2px solid var(--accent);
  }
  .role-opt span {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .secret {
    display: flex;
    flex-direction: column;
    gap: 12px;
    align-items: flex-start;
  }
  .secret p {
    margin: 0;
    color: var(--warn);
    font-size: 13px;
  }
  .secret-box {
    width: 100%;
    padding: 10px 12px;
    border: 1px dashed var(--accent);
    border-radius: var(--radius);
    background: var(--bg);
    font-size: 12.5px;
    overflow-wrap: anywhere;
    user-select: all;
  }
  .example {
    width: 100%;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11.5px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  @media (max-width: 760px) {
    .row,
    .row.all {
      grid-template-columns: 1fr auto;
    }
    .row > :nth-child(n + 3):not(.act) {
      display: none;
    }
    .head-row {
      display: none;
    }
  }
</style>
