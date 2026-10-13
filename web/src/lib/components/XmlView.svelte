<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy'
  import Download from '@lucide/svelte/icons/download'
  import RefreshCw from '@lucide/svelte/icons/refresh-cw'
  import Pencil from '@lucide/svelte/icons/pencil'
  import XmlEditor from './XmlEditor.svelte'
  import { api, ApiError } from '../api'
  import { copy } from '../machine'
  import { highlightXML } from '../xml'

  interface Props {
    uuid: string
    name: string
    /** Running machines have a live config and a (possibly different) next-boot config. */
    active: boolean
    persistent: boolean
    /** Changes when the machine changes state, so the XML is refetched. */
    version: string
    /** Called after the definition was saved. */
    onsaved?: () => void
  }
  let { uuid, name, active, persistent, version, onsaved }: Props = $props()

  let editing = $state(false)

  let which = $state<'live' | 'next'>('live')
  let xml = $state<string | null>(null)
  let error = $state<string | null>(null)
  let loading = $state(false)

  let inactive = $derived(which === 'next' && active)

  async function load() {
    loading = true
    error = null
    try {
      xml = await api.domainXML(uuid, inactive)
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e)
    } finally {
      loading = false
    }
  }

  $effect(() => {
    void version
    void inactive
    void load()
  })

  let lines = $derived(xml ? highlightXML(xml) : [])

  function download() {
    if (!xml) return
    const url = URL.createObjectURL(new Blob([xml], { type: 'application/xml' }))
    const a = Object.assign(document.createElement('a'), {
      href: url,
      download: `${name}${inactive ? '.next-boot' : ''}.xml`,
    })
    a.click()
    URL.revokeObjectURL(url)
  }
</script>

<section class="xml">
  {#if editing}
    <XmlEditor
      {uuid}
      {active}
      onclose={(saved) => {
        editing = false
        if (saved) {
          which = active ? 'next' : 'live'
          void load()
          onsaved?.()
        }
      }}
    />
  {:else}
  <header class="bar">
    {#if active && persistent}
      <div class="seg" role="radiogroup" aria-label="Which configuration">
        <button role="radio" aria-checked={which === 'live'} class:on={which === 'live'} onclick={() => (which = 'live')}>
          Running now
        </button>
        <button role="radio" aria-checked={which === 'next'} class:on={which === 'next'} onclick={() => (which = 'next')}>
          Next boot
        </button>
      </div>
    {:else}
      <span class="what">{active ? 'Running configuration (transient machine)' : 'Configuration'}</span>
    {/if}
    <span class="spacer"></span>
    <button class="tool" onclick={() => load()} disabled={loading} title="Reload">
      <span class:spin={loading}><RefreshCw size={14} /></span>
    </button>
    <button class="tool" onclick={() => xml && copy(xml, 'XML')} disabled={!xml}><Copy size={14} /> Copy</button>
    <button class="tool" onclick={download} disabled={!xml}><Download size={14} /> Download</button>
    {#if persistent || !active}
      <button class="tool edit" onclick={() => (editing = true)}><Pencil size={14} /> Edit</button>
    {/if}
  </header>

  {#if error}
    <div class="err">Could not load the XML: {error}</div>
  {:else if xml === null}
    <div class="sk-wrap">
      {#each [70, 45, 60, 52, 38, 66] as w, i (i)}<span class="sk" style:width="{w}%"></span>{/each}
    </div>
  {:else}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (scrollable region must be keyboard-scrollable) -->
    <div class="code mono" role="region" aria-label="Domain XML" tabindex="0">
      <ol>
        {#each lines as l, i (i)}
          <li><span class="ln" aria-hidden="true">{i + 1}</span><span class="src">{@html l || ' '}</span></li>
        {/each}
      </ol>
    </div>
  {/if}
  <p class="hint">
    {persistent || !active ? 'Edit changes the saved definition, validated by libvirt.' : 'Transient machines have no saved definition to edit.'}
  </p>
  {/if}
</section>

<style>
  .xml {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .what {
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    padding-left: 4px;
  }
  .spacer {
    flex: 1;
  }
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: 9px;
    background: var(--surface-2);
  }
  .seg button {
    height: 26px;
    padding: 0 11px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 500;
    cursor: pointer;
  }
  .seg button.on {
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .tool {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
    cursor: pointer;
  }
  .tool:hover:not(:disabled) {
    color: var(--text);
    border-color: var(--border-strong);
  }
  .tool:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .tool.edit {
    border-color: color-mix(in srgb, var(--accent) 50%, transparent);
    color: var(--accent);
  }
  .tool span {
    display: grid;
  }
  .spin {
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .err {
    padding: 18px 16px;
    color: var(--danger);
    font-size: 13px;
  }
  .code {
    max-height: 70vh;
    overflow: auto;
    background: var(--bg-elev);
    font-size: 12.5px;
    line-height: 1.6;
  }
  ol {
    margin: 0;
    padding: 10px 0;
    list-style: none;
    min-width: max-content;
  }
  li {
    display: flex;
  }
  li:hover {
    background: var(--surface-hover);
  }
  .ln {
    position: sticky;
    left: 0;
    flex: none;
    width: 52px;
    padding-right: 14px;
    text-align: right;
    color: var(--text-3);
    background: var(--bg-elev);
    user-select: none;
    opacity: 0.7;
  }
  .src {
    white-space: pre;
    padding-right: 16px;
    color: var(--text-2);
  }
  .src :global(.x-t) {
    color: var(--accent);
  }
  .src :global(.x-a) {
    color: var(--accent-2);
  }
  .src :global(.x-v) {
    color: var(--warn);
  }
  .src :global(.x-p) {
    color: var(--text-3);
  }
  .src :global(.x-c) {
    color: var(--text-3);
    font-style: italic;
  }
  .sk-wrap {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 18px 16px;
  }
  .sk {
    height: 12px;
    border-radius: 5px;
    background: var(--skeleton);
    animation: shimmer 1.4s ease-in-out infinite;
  }
  @keyframes shimmer {
    50% {
      opacity: 0.45;
    }
  }
  .hint {
    margin: 0;
    padding: 9px 14px;
    border-top: 1px solid var(--border);
    color: var(--text-3);
    font-size: 12px;
  }
</style>
