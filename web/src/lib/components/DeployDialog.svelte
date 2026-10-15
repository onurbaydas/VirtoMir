<script lang="ts">
  // New machine from a template: name, linked or full copy, optional first-boot settings.
  import Link from '@lucide/svelte/icons/link'
  import Copy from '@lucide/svelte/icons/copy'
  import Rocket from '@lucide/svelte/icons/rocket'
  import { api, errorText } from '../api'
  import { deploy } from '../deploy.svelte'
  import { track } from '../jobs'
  import { live } from '../live.svelte'
  import { machineHref, router } from '../router.svelte'
  import Dialog from './Dialog.svelte'

  let name = $state('')
  let linked = $state(true)
  let firstBoot = $state(false)
  let user = $state('admin')
  let password = $state('')
  let sshKeys = $state('')
  let start = $state(true)
  let busy = $state(false)
  let error = $state('')

  let t = $derived(deploy.template)

  $effect(() => {
    if (!t) return
    name = uniqueName(t.name.replace(/[-_.]?(template|tpl|base|golden)$/i, '') || t.name)
    linked = true
    firstBoot = false
    password = ''
    sshKeys = ''
    start = true
    busy = false
    error = ''
  })

  function uniqueName(base: string) {
    const taken = new Set((live.snapshot?.domains ?? []).map((d) => d.name))
    for (let i = 1; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`
  }

  let keys = $derived(sshKeys.split('\n').map((k) => k.trim()).filter(Boolean))
  let nameValid = $derived(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(name))
  let nameTaken = $derived((live.snapshot?.domains ?? []).some((d) => d.name === name))
  let ciProblem = $derived(
    !firstBoot
      ? ''
      : !/^[a-z_][a-z0-9_-]{0,31}$/.test(user)
        ? 'User name: lowercase letters, digits, "_" or "-".'
        : !password && keys.length === 0
          ? 'Set a password or an SSH key to be able to sign in.'
          : '',
  )
  let ok = $derived(nameValid && !nameTaken && !ciProblem)

  const close = () => (deploy.template = null)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!t || !ok) return
    busy = true
    error = ''
    try {
      const job = await api.deploy(t.uuid, {
        name,
        linked,
        start,
        cloudInit: firstBoot ? { hostname: name, user, password: password || undefined, sshKeys: keys } : undefined,
      })
      const made = name
      close()
      track(job, { success: `${made} is ready`, onSuccess: (j) => j.result && router.navigate(machineHref(j.result)) })
    } catch (err) {
      error = errorText(err)
      busy = false
    }
  }
</script>

<Dialog open={t !== null} title="New machine from {t?.name ?? ''}" subtitle="Same hardware and system as the template, with its own identity" onclose={close}>
  <form id="deploy-form" class="form" onsubmit={submit}>
    <label class="ui-field">
      <span class="label">Name</span>
      <input class="ui-input" bind:value={name} required aria-invalid={!nameValid || nameTaken} />
      {#if nameTaken}<span class="hint warn">A machine with this name already exists.</span>{:else if !nameValid}<span class="hint warn">Letters, digits, '.', '_', '+' or '-'.</span>{/if}
    </label>

    <div class="modes" role="radiogroup" aria-label="Disk">
      <label class="mode" class:on={linked}>
        <input type="radio" bind:group={linked} value={true} />
        <span class="m-head"><Link size={15} /> Linked clone</span>
        <span class="m-text">Ready in seconds and takes almost no space: only what the machine changes is stored. Needs the template to stay.</span>
      </label>
      <label class="mode" class:on={!linked}>
        <input type="radio" bind:group={linked} value={false} />
        <span class="m-head"><Copy size={15} /> Full copy</span>
        <span class="m-text">An independent machine with its own copy of every disk. Takes as long and as much space as the disks.</span>
      </label>
    </div>

    <label class="check"><input type="checkbox" bind:checked={firstBoot} /> Set host name and sign-in on first boot <span class="muted">(cloud-init)</span></label>
    {#if firstBoot}
      <div class="ci">
        <div class="ci-row">
          <label class="ui-field">
            <span class="label">User</span>
            <input class="ui-input" bind:value={user} autocomplete="off" />
          </label>
          <label class="ui-field">
            <span class="label">Password</span>
            <input class="ui-input" type="password" bind:value={password} autocomplete="new-password" />
          </label>
        </div>
        <label class="ui-field">
          <span class="label">SSH public keys <span class="muted">(one per line)</span></span>
          <textarea class="ui-input mono" rows="2" bind:value={sshKeys} placeholder="ssh-ed25519 AAAA…"></textarea>
        </label>
        {#if ciProblem}<span class="hint warn">{ciProblem}</span>{/if}
        <span class="hint">The host name becomes <b>{name || '…'}</b>. Works when the template's system has cloud-init (cloud images do).</span>
      </div>
    {/if}

    <label class="check"><input type="checkbox" bind:checked={start} /> Start it right away</label>
    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={close}>Cancel</button>
    <button class="ui-btn primary" form="deploy-form" disabled={!ok || busy}><Rocket size={14} /> Create machine</button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .modes {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .mode {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 0;
    cursor: pointer;
  }
  .mode.on {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 7%, transparent);
  }
  .mode input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .m-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-weight: 600;
    font-size: 13.5px;
  }
  .m-text {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .ci {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding-left: 24px;
  }
  .ci-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .hint,
  .muted {
    color: var(--text-3);
    font-size: 12px;
  }
  .warn {
    color: var(--warn);
  }
  @media (max-width: 560px) {
    .modes,
    .ci-row {
      grid-template-columns: 1fr;
    }
  }
</style>
