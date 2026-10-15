<script lang="ts">
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
      toasts.push({ kind: 'error', title: 'No join code', detail: errorText(e) })
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
      toasts.push({ kind: 'info', title: 'Joined the cluster', detail: 'The other hosts are now in the host switcher.' })
      await cluster.refresh()
    } catch (e) {
      joinError = errorText(e)
    } finally {
      joining = false
    }
  }

  async function remove(id: string, name: string, isSelf: boolean) {
    await api.removeMember(id)
    toasts.push({ kind: 'info', title: isSelf ? 'Left the cluster' : `${name} removed`, detail: 'Its machines keep running; it is just not shown here any more.' })
    if (!isSelf && cluster.current?.id === id) cluster.open(self?.id ?? '')
    await cluster.refresh()
  }
</script>

<svelte:head><title>Cluster · VirtoMir</title></svelte:head>

<header class="page-head">
  <div>
    <h1>Cluster</h1>
    <p class="lead">Manage several hosts from one place. Every host keeps running VirtoMir on its own — if one is down, the others carry on.</p>
  </div>
</header>

{#if info === null}
  <p class="muted">Loading…</p>
{:else if !info.enabled}
  <div class="ui-panel card notice">
    <Unplug size={18} />
    <div>
      <strong>This host runs without HTTPS</strong>
      <p>Hosts recognize each other by their certificates, so joining needs HTTPS. Start virtomird with <span class="mono">-tls auto</span> (the default) or your own certificate.</p>
    </div>
  </div>
{:else}
  {#if cluster.clustered}
    <ClusterOverview />
    <h2 class="sub">Members</h2>
    <section class="ui-panel members" aria-label="Hosts">
      {#each cluster.members as m (m.id)}
        <div class="member" class:current={cluster.current?.id === m.id}>
          <span class="dot" class:ok={m.reachable} title={m.reachable ? 'Reachable' : 'Not reachable'}></span>
          <div class="m-main">
            <div class="m-name">
              {m.name}
              {#if m.self}<span class="pill">this host</span>{/if}
              {#if cluster.current?.id === m.id}<span class="pill accent">shown</span>{/if}
            </div>
            <div class="m-sub mono">{m.address}</div>
            {#if m.error}<div class="m-err">{m.error}</div>{/if}
          </div>
          <div class="m-ver mono" class:warn={m.version && self?.version && m.version !== self.version} title={m.version && self?.version && m.version !== self.version ? 'Runs another VirtoMir version than this host' : 'VirtoMir version'}>
            {m.version ?? '—'}
          </div>
          <div class="m-actions">
            <button class="ui-btn sm" disabled={!m.reachable || cluster.current?.id === m.id} onclick={() => cluster.open(m.id)}>Open</button>
            {#if admin}
              <ConfirmButton
                label={m.self ? 'Leave' : 'Remove'}
                confirm={m.self ? 'Leave the cluster' : `Remove ${m.name}`}
                title={m.self ? 'This host stops being part of the cluster' : 'Its machines keep running; it is only disconnected from the others'}
                failure="Not removed"
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
      <strong>This host is on its own</strong>
      <span>Add another host to manage both from here, or join this one to an existing cluster.</span>
    </div>
  {/if}

  {#if admin}
    <div class="cards">
      <section class="ui-panel card">
        <h2><KeyRound size={16} /> Add a host</h2>
        <ol class="steps">
          <li>Install VirtoMir on the new host.</li>
          <li>Create a join code here.</li>
          <li>On the new host, open <strong>Cluster</strong> and paste the code under <strong>Join a cluster</strong>.</li>
        </ol>
        {#if invite && left > 0}
          <div class="code">
            <code class="mono">{invite.code}</code>
            <button class="ui-btn sm" onclick={() => copy(invite!.code, 'Join code')}><Copy size={14} /> Copy</button>
          </div>
          <p class="hint">Works once, for {leftText} more. The new host will connect to <span class="mono">{invite.address}</span>.</p>
          {#if loopback}
            <p class="warn-text">You opened VirtoMir through a local address, so the code points there. Open VirtoMir through an address the new host can reach and create the code again.</p>
          {/if}
        {:else}
          <button class="ui-btn primary" class:busy={inviting} disabled={inviting} onclick={createInvite}>Create join code</button>
          {#if invite}<p class="hint">The code expired. Create a new one.</p>{/if}
        {/if}
      </section>

      {#if !cluster.clustered}
        <section class="ui-panel card">
          <h2><LogIn size={16} /> Join a cluster</h2>
          <p class="hint">Paste a code created on any host of the cluster. This host's machines stay where they are.</p>
          <textarea class="ui-input mono" rows="3" placeholder="vmj1-…" bind:value={code} aria-label="Join code" spellcheck="false"></textarea>
          {#if joinError}<div class="ui-error-box">{joinError}</div>{/if}
          <button class="ui-btn primary" class:busy={joining} disabled={joining || !code.trim().startsWith('vmj1-')} onclick={join}>Join</button>
        </section>
      {/if}
    </div>
  {:else}
    <p class="muted">Adding and removing hosts needs the admin role.</p>
  {/if}
{/if}

<style>
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
    border-radius: 0;
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
    border-radius: 0;
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
    border-radius: 0;
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
