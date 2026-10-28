<script lang="ts">
  // Storage VirtoMir finds by itself on this host's networks (iSCSI targets, NFS exports, SMB
  // shares): one click makes it a pool. Only a server that asks for a password asks the user.
  import { onMount } from 'svelte'
  import Network from '@lucide/svelte/icons/network'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Lock from '@lucide/svelte/icons/lock'
  import { api, errorText, type FoundStorage, type StorageLogin } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatBytes } from '../format'
  import { t } from '../i18n.svelte'
  import { toasts } from '../toast.svelte'

  let { onconnected }: { onconnected: (pool: string) => void } = $props()

  let found = $state<FoundStorage[] | null>(null)
  let scanning = $state(false)
  let scanError = $state('')
  let busy = $state('')
  let everywhere = $state(true)
  // Sign-in fields, shown for a server that asked for them.
  let logins = $state<Record<string, StorageLogin>>({})

  const key = (f: FoundStorage) => `${f.kind}|${f.server}|${f.name}`
  const serverKey = (f: FoundStorage) => `${f.kind}|${f.server}|`
  const login = (f: FoundStorage) => logins[key(f)] ?? logins[serverKey(f)]
  const kindLabel: Record<FoundStorage['kind'], string> = { iscsi: 'iSCSI', nfs: 'NFS', smb: 'SMB', fc: 'FC' }

  async function scan() {
    scanning = true
    scanError = ''
    try {
      found = await api.discoverStorage()
      for (const f of found) if (f.needsAuth && !f.name) logins[key(f)] ??= { username: '', password: '' }
    } catch (e) {
      scanError = errorText(e)
    } finally {
      scanning = false
    }
  }
  onMount(() => void scan())

  // A server that lists its shares only after a sign-in: ask it again with the user's login.
  async function listShares(f: FoundStorage) {
    busy = key(f)
    try {
      const more = await api.discoverStorage({ server: f.server!, ...logins[key(f)] })
      if (more.some((m) => m.kind === f.kind && m.needsAuth && !m.name)) throw new Error(t('The server refused this user name and password.'))
      logins[serverKey(f)] = logins[key(f)]
      found = [...(found ?? []).filter((x) => x.kind !== f.kind || x.server !== f.server), ...more.filter((m) => m.kind === f.kind)]
    } catch (e) {
      toasts.push({ kind: 'error', title: t('Could not list the shares'), detail: errorText(e) })
    } finally {
      busy = ''
    }
  }

  async function use(f: FoundStorage) {
    busy = key(f)
    try {
      const res = await api.connectStorage({ kind: f.kind, server: f.server, host: f.host, name: f.name, everywhere, ...login(f) })
      const failed = (res.results ?? []).filter((r) => !r.ok)
      if (res.results && failed.length === res.results.length) throw new Error(failed.map((r) => `${r.host}: ${r.error}`).join(' · '))
      toasts.push({
        kind: failed.length ? 'warn' : 'info',
        title: t('{pool} is ready', { pool: res.pool }),
        detail: failed.map((r) => `${r.host}: ${r.error}`).join(' · ') || undefined,
      })
      f.pool = res.pool
      onconnected(res.pool)
    } catch (e) {
      const msg = errorText(e)
      if (msg.includes('user name and password')) logins[key(f)] = login(f) ?? { username: '', password: '' }
      toasts.push({ kind: 'error', title: t('Not connected'), detail: msg })
    } finally {
      busy = ''
    }
  }
</script>

{#if scanning && !found}
  <section class="ui-panel finder">
    <p class="hint"><Network size={14} /> {t('Looking for storage on your network…')}</p>
  </section>
{:else if found?.length || scanError}
  <section class="ui-panel finder">
    <header>
      <h2><Network size={16} /> {t('Found on your network')}</h2>
      <button class="ui-btn sm ghost" onclick={scan} disabled={scanning} title={t('Look again')}><RefreshCw size={14} /> {scanning ? t('Looking…') : t('Look again')}</button>
    </header>
    {#if scanError}<div class="ui-error-box">{scanError}</div>{/if}
    <ul>
      {#each found ?? [] as f (key(f))}
        <li>
          <span class="kind mono">{kindLabel[f.kind]}</span>
          <span class="what">
            <strong class="mono">{f.name || t('Shares')}</strong>
            <span class="where">{f.host ? `${f.host} · ${f.server}` : f.server}{f.sizeBytes ? ` · ${formatBytes(f.sizeBytes)}` : ''}{f.portals && f.portals.length > 1 ? ` · ${t('{n} paths', { n: f.portals.length })}` : ''}</span>
            {#if f.error}<span class="err">{f.error}</span>{/if}
          </span>
          {#if f.pool}
            <a class="ui-btn sm ghost" href={`/storage#${encodeURIComponent(f.pool)}`}>{t('In use as {pool}', { pool: f.pool })}</a>
          {:else if !f.error}
            {@const k = key(f)}
            <span class="act">
              {#if logins[k]}
                <Lock size={14} />
                <input class="ui-input" placeholder={t('User name')} autocomplete="off" bind:value={logins[k].username} aria-label={t('User name')} />
                <input class="ui-input" type="password" placeholder={t('Password')} autocomplete="off" bind:value={logins[k].password} aria-label={t('Password')} />
              {/if}
              {#if f.needsAuth && !f.name}
                <button class="ui-btn sm" disabled={busy !== ''} onclick={() => listShares(f)}>{busy === k ? t('Signing in…') : t('Show shares')}</button>
              {:else}
                <button class="ui-btn sm primary" disabled={busy !== ''} onclick={() => use(f)}>{busy === k ? t('Connecting…') : t('Use')}</button>
              {/if}
            </span>
          {/if}
        </li>
      {/each}
    </ul>
    {#if cluster.clustered}
      <label class="check"><input type="checkbox" bind:checked={everywhere} /> {t('Connect it on every host of the cluster')}</label>
    {/if}
  </section>
{/if}

<style>
  .finder {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px 16px;
    margin-bottom: 16px;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .kind {
    min-width: 44px;
    font-size: 11.5px;
    font-weight: 600;
    color: var(--text-2);
  }
  .what {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  .what strong {
    overflow-wrap: anywhere;
  }
  .where,
  .hint {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    color: var(--text-2);
    font-size: 12.5px;
  }
  .err {
    color: var(--danger);
    font-size: 12px;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .act {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .act .ui-input {
    width: 140px;
  }
  .check {
    display: flex;
    gap: 8px;
    font-size: 13px;
    color: var(--text-2);
  }
</style>
