<script lang="ts">
  import { t } from '../i18n.svelte'
  // Edits the persistent (next-boot) definition. The browser checks that the XML is
  // well-formed while typing; libvirt validates it against its schema on save.
  import Save from '@lucide/svelte/icons/save'
  import Undo2 from '@lucide/svelte/icons/undo-2'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import Split from '@lucide/svelte/icons/split'
  import { api, changedElsewhere, errorText } from '../api'
  import { toasts } from '../toast.svelte'

  interface Props {
    uuid: string
    active: boolean
    onclose: (saved: boolean) => void
  }
  let { uuid, active, onclose }: Props = $props()

  let original = $state<string | null>(null)
  let text = $state('')
  let loadErr = $state('')
  let saveErr = $state('')
  let saving = $state(false)
  let showDiff = $state(false)
  let ta = $state<HTMLTextAreaElement>()
  let gutter = $state<HTMLDivElement>()

  // The version the text was loaded at: saving is refused if the machine changed since.
  let version = $state('')
  let stale = $state(false)
  function loadXML() {
    void api
      .nextBootXML(uuid)
      .then((r) => {
        original = r.xml
        text = r.xml
        version = r.version
        saveErr = ''
        stale = false
      })
      .catch((e) => (loadErr = errorText(e)))
  }
  $effect(() => {
    void uuid
    loadXML()
  })

  let dirty = $derived(original !== null && text !== original)
  let lineCount = $derived(text.split('\n').length)

  interface DiffLine {
    type: 'add' | 'del' | 'eq'
    text: string
  }
  let diffLines = $derived.by((): DiffLine[] => {
    if (!original || !dirty) return []
    const origLines = original.split('\n')
    const newLines = text.split('\n')
    const res: DiffLine[] = []
    let i = 0, j = 0
    while (i < origLines.length || j < newLines.length) {
      if (i < origLines.length && j < newLines.length && origLines[i] === newLines[j]) {
        res.push({ type: 'eq', text: origLines[i] })
        i++
        j++
      } else if (j < newLines.length && (!origLines.slice(i).includes(newLines[j]))) {
        res.push({ type: 'add', text: newLines[j] })
        j++
      } else if (i < origLines.length) {
        res.push({ type: 'del', text: origLines[i] })
        i++
      } else if (j < newLines.length) {
        res.push({ type: 'add', text: newLines[j] })
        j++
      }
    }
    return res
  })

  // Well-formedness check (cheap, instant). Schema problems come back from libvirt on save.
  let parseError = $derived.by(() => {
    if (!text.trim()) return t('The XML is empty.')
    const doc = new DOMParser().parseFromString(text, 'application/xml')
    const err = doc.querySelector('parsererror')
    if (err) return (err.textContent ?? t('Not well-formed XML')).split('\n').find((l) => l.trim())?.trim() ?? t('Not well-formed XML')
    if (doc.documentElement.nodeName !== 'domain') return 'The root element must be <domain>.'
    return ''
  })

  async function save() {
    saving = true
    saveErr = ''
    try {
      const res = await api.saveXML(uuid, text, version)
      toasts.push({
        kind: 'info',
        title: t('Configuration saved'),
        detail: res.appliesOnNextBoot ? 'The machine is running: changes take effect at its next boot.' : undefined,
      })
      onclose(true)
    } catch (e) {
      saveErr = errorText(e)
      stale = changedElsewhere(e)
    } finally {
      saving = false
    }
  }

  function cancel() {
    if (dirty && !confirm( t('Discard your changes to the XML?'))) return
    onclose(false)
  }

  function onKey(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault()
      if (dirty && !parseError && !saving) void save()
    }
    // Tab inserts two spaces instead of leaving the editor.
    if (e.key === 'Tab' && !e.shiftKey && ta) {
      e.preventDefault()
      const { selectionStart: s, selectionEnd: end } = ta
      text = text.slice(0, s) + '  ' + text.slice(end)
      requestAnimationFrame(() => ta && (ta.selectionStart = ta.selectionEnd = s + 2))
    }
  }
</script>

<div class="editor">
  <div class="note" class:warn={active}>
    <TriangleAlert size={15} />
    <span>
      {#if active}
        {t('Editing the configuration for the')} <b>{t('next boot')}</b>. The running machine keeps its current setup until it is restarted.
      {:else}
        {t('Every libvirt setting can be changed here — including ones that stop the machine from booting. A snapshot first is a good idea.')}
      {/if}
    </span>
  </div>

  {#if loadErr}
    <div class="err">{loadErr}</div>
  {:else if original === null}
    <p class="muted">{t('Loading…')}</p>
  {:else if showDiff}
    <div class="diff-area mono">
      {#each diffLines as dl}
        <div class="diff-line {dl.type}">
          <span class="d-sign">{dl.type === 'add' ? '+' : dl.type === 'del' ? '-' : ' '}</span>
          <span class="d-text">{dl.text}</span>
        </div>
      {/each}
    </div>
  {:else}
    <div class="area mono">
      <div class="gutter" bind:this={gutter} aria-hidden="true">
        {#each { length: lineCount } as _, i (i)}<span>{i + 1}</span>{/each}
      </div>
      <textarea
        bind:this={ta}
        bind:value={text}
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        aria-label={t('Domain XML')}
        aria-invalid={!!parseError}
        onscroll={() => gutter && ta && (gutter.scrollTop = ta.scrollTop)}
        onkeydown={onKey}
      ></textarea>
    </div>
  {/if}

  <footer>
    <span class="status" class:bad={!!parseError}>
      {#if parseError}{parseError}{:else if dirty}{t('Unsaved changes · Ctrl+S to save')}{:else}{t('No changes')}{/if}
    </span>
    <button class="ui-btn ghost mono" class:active={showDiff} onclick={() => (showDiff = !showDiff)} disabled={!dirty} title={t('Toggle visual diff')}>
      <Split size={14} /> {showDiff ? 'Edit' : t('Show changes')}
    </button>
    <button class="ui-btn ghost" onclick={() => (text = original ?? '')} disabled={!dirty || saving}><Undo2 size={14} /> {t('Reset')}</button>
    <button class="ui-btn ghost" onclick={cancel} disabled={saving}>{t('Cancel')}</button>
    <button class="ui-btn primary" onclick={save} disabled={!dirty || !!parseError || saving}>
      <Save size={14} /> {saving ? 'Saving…' : 'Save'}
    </button>
  </footer>
  {#if saveErr && stale}
    <div class="err" role="alert">{saveErr} <button class="ui-btn sm" onclick={() => (confirm(t('Load the current XML? Your edits are lost; copy them first if you need them.')) ? loadXML() : null)}>{t('Reload')}</button></div>
  {:else if saveErr}<div class="err" role="alert"><b>{t('libvirt refused the change:')}</b> {saveErr}</div>{/if}
</div>

<style>
  .diff-area {
    height: min(70vh, 720px);
    overflow: auto;
    background: var(--bg-elev);
    font-size: 12.5px;
    line-height: 1.6;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
  }
  .diff-line {
    white-space: pre;
    display: flex;
  }
  .diff-line.add {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .diff-line.del {
    background: color-mix(in srgb, var(--danger) 18%, transparent);
    color: var(--danger);
  }
  .d-sign {
    width: 20px;
    font-weight: 700;
    user-select: none;
  }
  .editor {
    display: flex;
    flex-direction: column;
  }
  .note {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    background: color-mix(in srgb, var(--info) 7%, var(--surface));
    color: var(--info);
    font-size: 13px;
  }
  .note.warn {
    background: color-mix(in srgb, var(--warn) 9%, var(--surface));
    color: var(--warn);
  }
  .note span {
    color: var(--text-2);
  }
  .note b {
    color: var(--text);
  }
  .area {
    display: flex;
    height: min(70vh, 720px);
    background: var(--bg-elev);
    font-size: 12.5px;
    line-height: 1.6;
  }
  .gutter {
    flex: none;
    width: 52px;
    padding: 10px 14px 10px 0;
    overflow: hidden;
    color: var(--text-3);
    text-align: right;
    user-select: none;
    opacity: 0.7;
  }
  .gutter span {
    display: block;
  }
  textarea {
    flex: 1;
    min-width: 0;
    padding: 10px 16px 10px 0;
    border: 0;
    outline: none;
    resize: none;
    background: transparent;
    color: var(--text);
    font: inherit;
    line-height: inherit;
    white-space: pre;
    overflow: auto;
    tab-size: 2;
  }
  footer {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    border-top: 1px solid var(--border);
  }
  .status {
    flex: 1;
    min-width: 0;
    color: var(--text-3);
    font-size: 12.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .status.bad {
    color: var(--danger);
  }
  .err {
    padding: 10px 14px;
    border-top: 1px solid var(--border);
    background: var(--danger-soft);
    color: var(--text);
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .muted {
    padding: 16px;
    color: var(--text-3);
  }
</style>
