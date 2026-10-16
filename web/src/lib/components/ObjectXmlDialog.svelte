<script lang="ts">
  import { t } from '../i18n.svelte'
  // Raw XML of a network or storage pool (P4): everything the forms do not offer. The
  // browser checks well-formedness while typing; libvirt validates on save. Changes show as
  // a line diff before they are written.
  import Dialog from './Dialog.svelte'
  import Save from '@lucide/svelte/icons/save'
  import { errorText } from '../api'
  import { diffLines, hunks } from '../diff'
  import { toasts } from '../toast.svelte'

  interface Props {
    open: boolean
    title: string
    /** Object kind in messages: "network", "storage pool". */
    kind: string
    load: () => Promise<string>
    save: (xml: string) => Promise<{ restartNeeded: boolean }>
    onclose: (saved: boolean) => void
  }
  let { open, title, kind, load, save, onclose }: Props = $props()

  let original = $state<string | null>(null)
  let text = $state('')
  let error = $state('')
  let saving = $state(false)
  let review = $state(false)

  $effect(() => {
    if (!open) return
    original = null
    error = ''
    review = false
    load()
      .then((x) => {
        original = x
        text = x
      })
      .catch((e) => (error = errorText(e)))
  })

  let dirty = $derived(original !== null && text !== original)
  let parseError = $derived.by(() => {
    if (!dirty) return ''
    const doc = new DOMParser().parseFromString(text, 'application/xml')
    const err = doc.querySelector('parsererror')
    return err ? (err.textContent ?? 'not well-formed').split('\n')[0] : ''
  })
  let diff = $derived(review && original !== null ? hunks(diffLines(original, text)) : [])

  async function submit() {
    saving = true
    error = ''
    try {
      const r = await save(text)
      toasts.push({
        kind: r.restartNeeded ? 'warn' : 'info',
        title: `${title} saved`,
        detail: r.restartNeeded ? t('The {kind} is running: stop and start it to use the new definition.', { kind: kind }) : undefined,
      })
      onclose(true)
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = false
    }
  }
</script>

<Dialog {open} {title} subtitle={t('Definition XML · validated by libvirt on save')} width={820} onclose={() => onclose(false)}>
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if original === null && !error}
    <p class="muted">{t('Loading…')}</p>
  {:else if review}
    <div class="diff mono">
      {#each diff as l, i (i)}
        {#if l === null}<div class="gap">⋯</div>{:else}<div class="dl {l.kind}"><span>{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>{l.text}</div>{/if}
      {/each}
    </div>
  {:else}
    <textarea class="ui-input mono xml" bind:value={text} spellcheck="false" aria-invalid={!!parseError}></textarea>
    {#if parseError}<p class="err">{parseError}</p>{/if}
  {/if}
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={() => onclose(false)}>{t('Cancel')}</button>
    {#if review}
      <button class="ui-btn" onclick={() => (review = false)}>{t('Edit')}</button>
      <button class="ui-btn primary" disabled={saving} onclick={submit}><Save size={14} /> {saving ? 'Saving…' : 'Save'}</button>
    {:else}
      <button class="ui-btn" disabled={!dirty} onclick={() => (text = original ?? '')}>{t('Undo changes')}</button>
      <button class="ui-btn primary" disabled={!dirty || !!parseError} onclick={() => (review = true)}>{t('Review changes')}</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .xml {
    height: 52vh;
    padding: 10px;
    font-size: 12.5px;
    line-height: 1.5;
    white-space: pre;
  }
  .err {
    margin: 6px 0 0;
    color: var(--danger);
    font-size: 12.5px;
    font-weight: 700;
  }
  .diff {
    max-height: 52vh;
    overflow: auto;
    padding: 8px;
    border: var(--frame) solid var(--ink);
    background: var(--bg-elev);
    font-size: 12.5px;
    white-space: pre;
  }
  .dl span {
    display: inline-block;
    width: 16px;
  }
  .dl.add {
    background: color-mix(in srgb, var(--ok) 22%, transparent);
  }
  .dl.del {
    background: color-mix(in srgb, var(--danger) 22%, transparent);
  }
  .gap {
    color: var(--text-3);
  }
  .muted {
    color: var(--text-3);
  }
</style>
