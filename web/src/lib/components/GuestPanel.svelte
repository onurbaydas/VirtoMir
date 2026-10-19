<script lang="ts">
  // What the guest agent inside a running machine reports: OS, host name, users and how
  // full its file systems are. Loaded on its own so a slow agent never delays the page.
  import { t } from '../i18n.svelte'
  import { api, errorText, type GuestInfo, type Prop } from '../api'
  import { formatBytes } from '../format'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import PropList from './PropList.svelte'

  let { uuid, running }: { uuid: string; running: boolean } = $props()

  let info = $state<GuestInfo | null>(null)
  let error = $state('')
  let pwOpen = $state(false)
  let pwUser = $state('')
  let pwValue = $state('')
  let busy = $state(false)

  $effect(() => {
    info = null
    error = ''
    if (!running) return
    const id = uuid
    api
      .guestInfo(id)
      .then((g) => {
        if (id !== uuid) return
        info = g
        pwUser = g.users[0] ?? (g.os.toLowerCase().includes('windows') ? 'Administrator' : 'root')
      })
      .catch((e) => (error = errorText(e)))
  })

  let rows = $derived<Prop[]>(
    info?.connected
      ? [
          { label: t('Operating system'), value: info.os || '—' },
          { label: t('Kernel'), value: info.kernel || '—' },
          { label: t('Host name'), value: info.hostname || '—' },
          { label: t('Time zone'), value: info.timezone || '—' },
          { label: t('Signed in'), value: info.users.length ? info.users.join(', ') : t('nobody') },
        ]
      : [],
  )

  async function setPassword(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    try {
      await api.setGuestPassword(uuid, pwUser, pwValue)
      toasts.push({ kind: 'info', title: t('Password of {user} changed', { user: pwUser }) })
      pwOpen = false
      pwValue = ''
    } catch (err) {
      toasts.push({ kind: 'error', title: t('Password not changed'), detail: errorText(err) })
    } finally {
      busy = false
    }
  }
</script>

<h2>{t('Inside the machine')}</h2>
{#if !running}
  <p class="hint">{t('Start the machine to see what its guest agent reports.')}</p>
{:else if error}
  <div class="ui-error-box">{error}</div>
{:else if !info}
  <p class="hint">{t('Asking the guest agent…')}</p>
{:else if !info.connected}
  <p class="hint">{t('No guest agent answers. Install qemu-guest-agent in the machine (virtio-win guest tools on Windows) to see its OS, users and disk usage here.')}</p>
{:else}
  <PropList props={rows} />
  {#if info.filesystems.length}
    <ul class="fs">
      {#each info.filesystems as f (f.mountpoint)}
        {@const pct = f.totalBytes ? Math.round((f.usedBytes / f.totalBytes) * 100) : 0}
        <li>
          <span class="mono">{f.mountpoint}</span>
          <span class="bar" class:full={pct >= 90}><span style="width: {pct}%"></span></span>
          <span class="num">{formatBytes(f.usedBytes)} / {formatBytes(f.totalBytes)} · {pct} %</span>
        </li>
      {/each}
    </ul>
  {/if}
  {#if pwOpen}
    <form class="pw" onsubmit={setPassword}>
      <input class="ui-input" bind:value={pwUser} placeholder={t('User')} autocomplete="off" />
      <input class="ui-input" type="password" bind:value={pwValue} placeholder={t('New password')} autocomplete="new-password" />
      <button class="ui-btn sm primary" disabled={busy || !pwUser || !pwValue}>{t('Set')}</button>
      <button class="ui-btn sm ghost" type="button" onclick={() => (pwOpen = false)}>{t('Cancel')}</button>
    </form>
  {:else}
    <button class="ui-btn sm" disabled={!session.can('admin')} title={session.why('admin')} onclick={() => (pwOpen = true)}>{t('Set a password…')}</button>
  {/if}
{/if}

<style>
  .fs {
    list-style: none;
    margin: 12px 0;
    padding: 0;
    display: grid;
    gap: 6px;
  }
  .fs li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(60px, 2fr) auto;
    align-items: center;
    gap: 10px;
    font-size: 0.85em;
  }
  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .bar.full span {
    background: var(--danger);
  }
  .pw {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 8px;
  }
  .pw input {
    flex: 1 1 140px;
  }
</style>
