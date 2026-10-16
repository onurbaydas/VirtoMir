<script lang="ts">
  import { t } from '../i18n.svelte'
  // Expert setting: extra QEMU arguments and environment (<qemu:commandline>). Collapsed and
  // marked dangerous: libvirt cannot see what these do and marks the machine "tainted".
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { api, errorText } from '../api'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'

  let { uuid, onsaved }: { uuid: string; onsaved: () => void } = $props()

  let open = $state(false)
  let loaded = $state(false)
  let args = $state('')
  let env = $state('')
  let saved = $state({ args: '', env: '' })
  let busy = $state(false)
  let error = $state('')
  let id = $props.id()

  $effect(() => {
    if (!open || loaded) return
    api
      .qemuArgs(uuid)
      .then((q) => {
        args = q.args.join('\n')
        env = Object.entries(q.env)
          .map(([k, v]) => `${k}=${v}`)
          .join('\n')
        saved = { args, env }
        loaded = true
      })
      .catch((e) => (error = errorText(e)))
  })

  const lines = (s: string) =>
    s
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)

  let badEnv = $derived(lines(env).filter((l) => !/^[A-Za-z_][A-Za-z0-9_]*=/.test(l)))
  let dirty = $derived(loaded && (args !== saved.args || env !== saved.env))
  let count = $derived(lines(saved.args).length + lines(saved.env).length)

  async function save() {
    busy = true
    error = ''
    try {
      const e: Record<string, string> = {}
      for (const l of lines(env)) {
        const i = l.indexOf('=')
        e[l.slice(0, i)] = l.slice(i + 1)
      }
      const r = await api.saveQEMUArgs(uuid, { args: lines(args), env: e })
      saved = { args, env }
      toasts.push({
        kind: 'warn',
        title: lines(args).length + Object.keys(e).length ? 'QEMU command line saved' : t('QEMU command line removed'),
        detail: r.appliesOnNextBoot ? 'Used from the next start of the machine.' : undefined,
      })
      onsaved()
    } catch (err) {
      error = errorText(err)
    } finally {
      busy = false
    }
  }
</script>

<section class="qemu" class:open>
  <button class="head" aria-expanded={open} aria-controls="{id}-body" onclick={() => (open = !open)}>
    <span class="icon"><TriangleAlert size={16} /></span>
    <span class="titles">
      <span class="title">{t('QEMU command line')} <span class="badge">{t('Expert')}</span></span>
      <span class="summary">{loaded ? (count ? `${count} extra ${count === 1 ? 'entry' : 'entries'}` : 'None') : t('Arguments and environment passed straight to QEMU')}</span>
    </span>
    <span class="chev"><ChevronDown size={16} /></span>
  </button>
  {#if open}
    <div class="body" id="{id}-body">
      <div class="warn">
        {t('These go to QEMU unchecked. A wrong argument stops the machine from starting, and libvirt marks it as')}
        <i>{t('tainted')}</i>{t(': support, migration and snapshots may misbehave. Prefer the Hardware and Settings tabs whenever they offer it.')}
      </div>
      {#if !loaded && !error}
        <p class="muted">{t('Loading…')}</p>
      {:else if loaded}
        <label class="ui-field">
          <span class="label">{t('Arguments, one per line')}</span>
          <textarea class="ui-input mono" rows="4" spellcheck="false" placeholder={'-device\nich9-intel-hda'} bind:value={args} readonly={!session.can('admin')}></textarea>
          <span class="hint">{t('An option and its value are separate lines, as QEMU\'s argv:')} <span class="mono">{t('-set')}</span> {t('then')} <span class="mono">{t('device.hostdev0.x-igd-opregion=on')}</span>.</span>
        </label>
        <label class="ui-field">
          <span class="label">{t('Environment, NAME=value per line')}</span>
          <textarea class="ui-input mono" rows="2" spellcheck="false" placeholder={t('QEMU_AUDIO_DRV=none')} bind:value={env} readonly={!session.can('admin')} aria-invalid={badEnv.length > 0}></textarea>
          {#if badEnv.length}<span class="error">{t('Not NAME=value: {v}', { v: badEnv.join(', ') })}</span>{/if}
        </label>
      {/if}
      {#if error}<div class="ui-error-box">{error}</div>{/if}
      {#if loaded}
        <div class="actions">
          <button class="ui-btn sm danger-soft" disabled={!dirty || busy || badEnv.length > 0 || !session.can('admin')} title={session.why('admin')} onclick={save}>
            {busy ? 'Saving…' : t('Save QEMU command line')}
          </button>
          {#if dirty}<button class="ui-btn sm ghost" onclick={() => ((args = saved.args), (env = saved.env))}>{t('Undo')}</button>{/if}
        </div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .qemu {
    background: var(--surface);
    border: var(--frame) dashed var(--border-strong);
    border-radius: var(--radius-sm);
    overflow: hidden;
    box-shadow:
      var(--bevel),
      var(--shadow-hard);
  }
  .qemu.open {
    border-style: solid;
    border-color: color-mix(in srgb, var(--warn) 45%, var(--border));
  }
  .head {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 12px 14px;
    border: 0;
    background: transparent;
    text-align: left;
    cursor: pointer;
  }
  .icon {
    flex: none;
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--warn) 14%, transparent);
    color: var(--warn);
  }
  .titles {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 13.5px;
  }
  .badge {
    padding: 1px 7px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--warn);
    font-size: 11px;
    font-weight: 600;
  }
  .summary {
    color: var(--text-3);
    font-size: 12.5px;
  }
  .chev {
    flex: none;
    display: grid;
    color: var(--text-3);
    transition: transform var(--dur) var(--ease-out);
  }
  .open .chev {
    transform: rotate(180deg);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px 14px 14px 58px;
    border-top: 1px solid var(--border);
  }
  .warn {
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    border-left: 3px solid var(--warn);
    color: var(--text-2);
    font-size: 12.5px;
  }
  textarea {
    resize: vertical;
    min-height: 0;
    height: auto;
    padding-top: 8px;
    padding-bottom: 8px;
    font-size: 12.5px;
  }
  .hint,
  .muted {
    margin: 0;
    color: var(--text-3);
    font-size: 12px;
  }
  .error {
    color: var(--warn);
    font-size: 12px;
  }
  .actions {
    display: flex;
    gap: 6px;
  }
  @media (max-width: 520px) {
    .body {
      padding-left: 14px;
    }
  }
</style>
