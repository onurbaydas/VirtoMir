<script lang="ts">
  import { t } from '../i18n.svelte'
  // Cluster (ADR-0020): which hosts belong together, add one with a join code, join one,
  // remove one. Every host keeps working on its own; this only connects them.
  import Copy from '@lucide/svelte/icons/copy'
  import KeyRound from '@lucide/svelte/icons/key-round'
  import LogIn from '@lucide/svelte/icons/log-in'
  import Server from '@lucide/svelte/icons/server'
  import Unplug from '@lucide/svelte/icons/unplug'
  import { api, errorText, type Invite } from '../api'
  import { cluster } from '../cluster.svelte'
  import { copy } from '../machine'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import ClusterOverview from '../components/ClusterOverview.svelte'
  import HaClusterPanel from '../components/HaClusterPanel.svelte'
  import ResourcePoolsPanel from '../components/ResourcePoolsPanel.svelte'

  type Tab = 'members' | 'ha' | 'pools'
  let currentTab = $state<Tab>('members')

  $effect(() => {
    void cluster.refresh()
  })

  let info = $derived(cluster.info)
  let self = $derived(cluster.members.find((m) => m.self))
  let admin = $derived(session.can('admin'))

  // ---- Add a host --------------------------------------------------------------------------
  let invite = $state<Invite | null>(null)
  let inviting = $state(false)
  let now = $state(Date.now())
  $effect(() => {
    if (!invite) return
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
  let left = $derived(invite ? Math.max(0, Date.parse(invite.expires) - now) : 0)
  let leftText = $derived(`${Math.floor(left / 60000)}:${String(Math.floor((left % 60000) / 1000)).padStart(2, '0')}`)
  let loopback = $derived(!!invite && /^https:\/\/(localhost|127\.|\[::1\])/.test(invite.address))

  async function createInvite() {
    inviting = true
    try {
      invite = await api.invite()
    } catch (e) {
      toasts.push({ kind: 'error', title: t('No join code'), detail: errorText(e) })
    } finally {
      inviting = false
    }
  }

  // ---- Join a cluster ------------------------------------------------------------------------
  let code = $state('')
  let joining = $state(false)
  let joinError = $state('')

  async function join() {
    joining = true
    joinError = ''
    try {
      await api.join(code.trim())
      code = ''
      toasts.push({ kind: 'info', title: t('Joined the cluster'), detail: t('The other hosts are now in the host switcher.') })
      await cluster.refresh()
    } catch (e) {
      joinError = errorText(e)
    } finally {
      joining = false
    }
  }

  async function remove(id: string, name: string, isSelf: boolean) {
    await api.removeMember(id)
    toasts.push({ kind: 'info', title: isSelf ? 'Left the cluster' : `${name} removed`, detail: t('Its machines keep running; it is just not shown here any more.') })
    if (!isSelf && cluster.current?.id === id) cluster.open(self?.id ?? '')
    await cluster.refresh()
  }
</script>

<svelte:head><title>{t('Cluster · VirtoMir')}</title></svelte:head>

<header class="page-head">
  <div>
    <h1>{t('Cluster')}</h1>
    <p class="lead">{t('Manage several hosts from one place. Every host keeps running VirtoMir on its own — if one is down, the others carry on.')}</p>
  </div>
</header>

{#if info === null}
  <p class="muted">{t('Loading…')}</p>
{:else if !info.enabled}
  <div class="ui-panel card notice">
    <Unplug size={18} />
    <div>
      <strong>{t('This host runs without HTTPS')}</strong>
      <p>{t('Hosts recognize each other by their certificates, so joining needs HTTPS. Start virtomird with')} <span class="mono">{t('-tls auto')}</span> {t('(the default) or your own certificate.')}</p>
    </div>
  </div>
{:else}
  <div class="cluster-tabs">
    <button class="c-tab" class:active={currentTab === 'members'} onclick={() => currentTab = 'members'}>
      {t('Members')}
    </button>
    <button class="c-tab" class:active={currentTab === 'ha'} onclick={() => currentTab = 'ha'}>
      {t('High availability')}
    </button>
    <button class="c-tab" class:active={currentTab === 'pools'} onclick={() => currentTab = 'pools'}>
      {t('Resource pools')}
    </button>
  </div>

  {#if currentTab === 'ha'}
    <HaClusterPanel />
  {:else if currentTab === 'pools'}
    <ResourcePoolsPanel />
  {:else}
    {#if cluster.clustered}
      <ClusterOverview />
      <h2 class="sub">{t('Members')}</h2>
      <section class="ui-panel members" aria-label={t('Hosts')}>
      {#each cluster.members as m (m.id)}
        <div class="member" class:current={cluster.current?.id === m.id}>
          <span class="dot" class:ok={m.reachable} title={m.reachable ? 'Reachable' : t('Not reachable')}></span>
          <div class="m-main">
            <div class="m-name">
              {m.name}
              {#if m.self}<span class="pill">{t('this host')}</span>{/if}
              {#if cluster.current?.id === m.id}<span class="pill accent">{t('shown')}</span>{/if}
            </div>
            <div class="m-sub">{m.address}</div>
            {#if m.error}<div class="m-err">{m.error}</div>{/if}
          </div>
          <div class="m-ver" class:warn={m.version && self?.version && m.version !== self.version} title={m.version && self?.version && m.version !== self.version ? 'Runs another VirtoMir version than this host' : t('VirtoMir version')}>
            {m.version ?? '—'}
          </div>
          <div class="m-actions">
            <button class="ui-btn sm" disabled={!m.reachable || cluster.current?.id === m.id} onclick={() => cluster.open(m.id)}>{t('Open')}</button>
            {#if admin}
              <ConfirmButton
                label={m.self ? 'Leave' : 'Remove'}
                confirm={m.self ? 'Leave the cluster' : `Remove ${m.name}`}
                title={m.self ? 'This host stops being part of the cluster' : t('Its machines keep running; it is only disconnected from the others')}
                failure={t('Not removed')}
                run={() => remove(m.id, m.name, m.self)}
              />
            {/if}
          </div>
        </div>
      {/each}
    </section>
  {:else}
    <div class="empty ui-panel">
      <Server size={28} strokeWidth={1.5} />
      <strong>{t('This host is on its own')}</strong>
      <span>{t('Add another host to manage both from here, or join this one to an existing cluster.')}</span>
    </div>
  {/if}

  {#if admin}
    <div class="cards">
      <section class="ui-panel card">
        <h2><KeyRound size={16} /> {t('Add a host')}</h2>
        <ol class="steps">
          <li>{t('Install VirtoMir on the new host.')}</li>
          <li>{t('Create a join code here.')}</li>
          <li>{t('On the new host, open')} <strong>{t('Cluster')}</strong> {t('and paste the code under')} <strong>{t('Join a cluster')}</strong>.</li>
        </ol>
        {#if invite && left > 0}
          <div class="code">
            <code class="mono">{invite.code}</code>
            <button class="ui-btn sm" onclick={() => copy(invite!.code, t('Join code'))}><Copy size={14} /> {t('Copy')}</button>
          </div>
          <p class="hint">{t('Works once, for {leftText} more. The new host will connect to', { leftText: leftText })} <span class="mono">{invite.address}</span>.</p>
          {#if loopback}
            <p class="warn-text">{t('You opened VirtoMir through a local address, so the code points there. Open VirtoMir through an address the new host can reach and create the code again.')}</p>
          {/if}
        {:else}
          <button class="ui-btn primary" class:busy={inviting} disabled={inviting} onclick={createInvite}>{t('Create join code')}</button>
          {#if invite}<p class="hint">{t('The code expired. Create a new one.')}</p>{/if}
        {/if}
      </section>

      {#if !cluster.clustered}
        <section class="ui-panel card">
          <h2><LogIn size={16} /> {t('Join a cluster')}</h2>
          <p class="hint">{t('Paste a code created on any host of the cluster. This host\'s machines stay where they are.')}</p>
          <textarea class="ui-input" rows="3" placeholder={t('vmj1-…')} bind:value={code} aria-label={t('Join code')} spellcheck="false"></textarea>
          {#if joinError}<div class="ui-error-box">{joinError}</div>{/if}
          <button class="ui-btn primary" class:busy={joining} disabled={joining || !code.trim().startsWith('vmj1-')} onclick={join}>{t('Join')}</button>
        </section>
      {/if}
    </div>
  {:else}
    <p class="muted">{t('Adding and removing hosts needs the admin role.')}</p>
  {/if}
{/if}
{/if}

<style>
  .cluster-tabs {
    display: flex;
    gap: 3px;
    margin-bottom: 0;
    padding: 0 6px;
    overflow-x: auto;
    border-bottom: var(--frame) solid var(--ink);
  }
  .c-tab {
    flex: none;
    display: flex;
    align-items: center;
    gap: 7px;
    height: 34px;
    margin-bottom: -2px;
    padding: 0 13px;
    border: var(--frame) solid var(--ink);
    border-bottom: 0;
    background: var(--surface-2);
    box-shadow: var(--bevel);
    color: var(--text-2);
    font-family: var(--font-display);
    font-size: 15px;
    font-stretch: 80%;
    font-weight: 700;
    white-space: nowrap;
    cursor: pointer;
  }
  .c-tab:hover {
    color: var(--text);
  }
  .c-tab.active {
    position: relative;
    z-index: 1;
    height: 38px;
    margin-top: -4px;
    background: var(--invert-bg);
    box-shadow: none;
    color: var(--invert-fg);
  }
  .c-tab.active::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 2px;
    background: var(--invert-bg);
  }
  .cluster-tabs {
    margin-bottom: 22px;
  }
  .lead {
    margin: 0;
    color: var(--text-3);
    font-size: 13.5px;
  }
  .card {
    padding: 16px 18px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .card h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
  }
  .card h2 :global(svg) {
    color: var(--accent);
  }
  .card .ui-btn {
    align-self: flex-start;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 14px;
    margin-top: 14px;
  }
  .notice {
    flex-direction: row;
    align-items: flex-start;
  }
  .notice p {
    margin: 4px 0 0;
    color: var(--text-2);
  }
  .sub {
    margin: 22px 0 10px;
    font-size: 15px;
  }
  .members {
    display: flex;
    flex-direction: column;
  }
  .member {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .member:last-child {
    border-bottom: none;
  }
  .member.current {
    background: var(--accent-soft);
  }
  .dot {
    flex: none;
    width: 9px;
    height: 9px;
    border-radius: var(--radius-sm);
    background: var(--danger);
  }
  .dot.ok {
    background: var(--ok);
  }
  .m-main {
    flex: 1;
    min-width: 0;
  }
  .m-name {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
  }
  .m-sub {
    color: var(--text-3);
    font-size: 12px;
  }
  .m-err {
    color: var(--danger);
    font-size: 12px;
    margin-top: 2px;
  }
  .m-ver {
    font-size: 12px;
    color: var(--text-3);
  }
  .m-ver.warn {
    color: var(--warn);
  }
  .m-actions {
    display: flex;
    gap: 8px;
  }
  .pill {
    padding: 1px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 11px;
    font-weight: 500;
  }
  .pill.accent {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .steps {
    margin: 0;
    padding-left: 20px;
    color: var(--text-2);
    font-size: 13px;
    line-height: 1.6;
  }
  .code {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .code code {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: 12px;
  }
  textarea {
    resize: vertical;
    font-size: 12px;
  }
  .hint,
  .muted {
    margin: 0;
    color: var(--text-3);
    font-size: 12.5px;
  }
  .warn-text {
    margin: 0;
    color: var(--warn);
    font-size: 12.5px;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 28px;
    color: var(--text-3);
    text-align: center;
  }
  .empty strong {
    color: var(--text);
  }
  @media (max-width: 700px) {
    .member {
      flex-wrap: wrap;
    }
    .m-actions {
      width: 100%;
      justify-content: flex-end;
    }
  }
</style>
