<script lang="ts" module>
  import FolderSymlink from '@lucide/svelte/icons/folder-symlink'
  import ClipboardCopy from '@lucide/svelte/icons/clipboard-copy'
  import Gpu from '@lucide/svelte/icons/gpu'
  import Timer from '@lucide/svelte/icons/timer'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Dices from '@lucide/svelte/icons/dices'
  import Layers from '@lucide/svelte/icons/layers'
  import type { ExtraDeviceKind } from '../api'

  /** What each kind is for, in plain words. "once": a machine has at most one. */
  const KINDS: { kind: ExtraDeviceKind; title: string; text: string; icon: typeof Timer; once?: boolean }[] = [
    { kind: 'filesystem', title: 'Shared folder', text: 'A folder of this host, mounted inside the guest (virtiofs).', icon: FolderSymlink },
    { kind: 'clipboard', title: 'Clipboard sharing', text: 'Copy and paste between the console and the guest.', icon: ClipboardCopy, once: true },
    { kind: 'video', title: 'Video card and monitors', text: 'Change the display adapter or give the guest up to 4 monitors.', icon: Gpu },
    { kind: 'mdev', title: 'vGPU slice', text: 'A share of a host graphics card split into mediated devices.', icon: Layers },
    { kind: 'watchdog', title: 'Watchdog', text: 'Resets or stops the machine when the guest hangs (or changes what an existing one does).', icon: Timer },
    { kind: 'tpm', title: 'TPM 2.0', text: 'Trusted Platform Module, needed by Windows 11 and disk encryption.', icon: ShieldCheck, once: true },
    { kind: 'rng', title: 'Random number generator', text: 'Gives the guest entropy from the host, so it boots and makes keys faster.', icon: Dices, once: true },
  ]
</script>

<script lang="ts">
  import { api, errorText, type DomainDetail, type HostMDev } from '../api'
  import { toasts } from '../toast.svelte'
  import { router } from '../router.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    detail: DomainDetail
    active: boolean
    /** Kind chosen when it opens (e.g. "video" from the video card's Change button). */
    start?: ExtraDeviceKind | null
    onclose: () => void
    onchange: () => void
  }
  let { open, detail, active, start = null, onclose, onchange }: Props = $props()

  let kind = $state<ExtraDeviceKind | null>(null)
  let busy = $state(false)
  let error = $state('')

  let source = $state('')
  let tag = $state('')
  let readOnly = $state(false)
  let videoModel = $state('virtio')
  let heads = $state(1)
  let wdModel = $state('i6300esb')
  let wdAction = $state('reset')
  let tpmModel = $state('tpm-crb')
  let mdevs = $state<HostMDev[] | null>(null)
  let mdevPick = $state('')

  /** Kinds of which the machine already has its one. */
  let present = $derived(new Set(detail.devices.map((d) => d.kind)))
  let usedTags = $derived(new Set(detail.devices.filter((d) => d.kind === 'filesystem').map((d) => d.target)))

  $effect(() => {
    if (!open) return
    kind = start
    error = ''
    busy = false
    source = ''
    tag = ''
    tagTouched = false
    readOnly = false
    const v = detail.devices.find((d) => d.kind === 'video')
    videoModel = v?.summary?.toLowerCase().match(/virtio|qxl|vga|bochs|ramfb|cirrus/)?.[0] ?? 'virtio'
    heads = 1
    mdevs = null
    mdevPick = ''
  })

  $effect(() => {
    if (kind !== 'mdev' || mdevs) return
    api
      .hostDevices()
      .then((h) => (mdevs = h.mdevs ?? []))
      .catch((e) => (error = errorText(e)))
  })

  // A tag from the folder name, until the user types their own.
  let tagTouched = $state(false)
  $effect(() => {
    if (tagTouched) return
    const base = source.split('/').filter(Boolean).pop() ?? ''
    tag = base.replace(/[^A-Za-z0-9_.-]/g, '-').slice(0, 36)
  })

  let sourceOK = $derived(/^\/./.test(source) && !/(^|\/)\.\.?(\/|$)/.test(source) && !source.endsWith('/'))
  let tagOK = $derived(/^[A-Za-z0-9_.-]{1,36}$/.test(tag) && !usedTags.has(tag))
  let multiHeadOK = $derived(heads <= 1 || videoModel === 'virtio' || videoModel === 'qxl')

  let ready = $derived.by(() => {
    switch (kind) {
      case null:
        return false
      case 'filesystem':
        return sourceOK && tagOK
      case 'video':
        return multiHeadOK
      case 'mdev':
        return !!mdevPick
      default:
        return true
    }
  })

  const add = async () => {
    if (!kind) return
    busy = true
    error = ''
    try {
      const spec =
        kind === 'filesystem'
          ? { kind, source, tag, readOnly }
          : kind === 'video'
            ? { kind, model: videoModel, heads }
            : kind === 'watchdog'
              ? { kind, model: wdModel, action: wdAction }
              : kind === 'tpm'
                ? { kind, model: tpmModel }
                : kind === 'mdev'
                  ? { kind, uuid: mdevPick }
                  : { kind }
      const r = await api.addDevice(detail.uuid, spec)
      const title = KINDS.find((k) => k.kind === kind)!.title
      toasts.push({
        kind: r.liveError ? 'warn' : 'info',
        title: `${title} added`,
        detail: r.liveError
          ? `Saved for the next start; it could not be added live: ${r.liveError}`
          : r.appliesOnNextBoot
            ? 'The machine gets it from its next start (shut it down and start it again; a restart from inside the guest is not enough).'
            : kind === 'filesystem'
              ? `Inside the guest: mount -t virtiofs ${tag} /mnt`
              : active
                ? 'Added to the running machine.'
                : undefined,
      })
      onchange()
      onclose()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Dialog {open} title={start === 'video' ? 'Video card and monitors' : 'Add a device'} subtitle="Hardware for {detail.name}" {onclose} width={600}>
  <form id="add-device" class="form" onsubmit={(e) => (e.preventDefault(), add())}>
    <div class="kinds" role="radiogroup" aria-label="Device">
      {#each KINDS as k (k.kind)}
        {@const has = k.once && present.has(k.kind)}
        <label class="kind" class:on={kind === k.kind} class:off={has}>
          <input type="radio" name="kind" value={k.kind} bind:group={kind} disabled={has} />
          <span class="icon"><k.icon size={16} /></span>
          <span class="kind-text">
            <b>{k.title}</b>
            <span>{has ? 'This machine has one already.' : k.text}</span>
          </span>
        </label>
      {/each}
    </div>

    {#if kind === 'filesystem'}
      <label class="ui-field">
        <span class="label">Folder on the host</span>
        <input class="ui-input mono" placeholder="/srv/share" bind:value={source} aria-invalid={source !== '' && !sourceOK} />
        <span class="hint">An absolute path. The folder must exist; the guest sees the files as the host's QEMU user can.</span>
      </label>
      <div class="two">
        <label class="ui-field">
          <span class="label">Mount tag</span>
          <input class="ui-input mono" bind:value={tag} oninput={() => (tagTouched = true)} aria-invalid={tag !== '' && !tagOK} />
          {#if usedTags.has(tag)}<span class="error">Already used by another shared folder.</span>{/if}
        </label>
        <label class="ui-switch ro"><input type="checkbox" bind:checked={readOnly} /> Read-only for the guest</label>
      </div>
      <p class="prep">
        <span class="prep-title">Inside the guest (Linux)</span>
        <code class="mono">mount -t virtiofs {tag || '<tag>'} /mnt</code>
        <span>Windows needs the VirtIO-FS driver and service from the virtio-win ISO.</span>
      </p>
      {#if active}<p class="hint">The machine's memory has to be shared for this; if it is not yet, the folder appears after the next start.</p>{/if}
    {:else if kind === 'clipboard'}
      <p class="text">Adds a <span class="mono">qemu-vdagent</span> channel. The guest needs <span class="mono">spice-vdagent</span> (Linux) or the SPICE guest tools (Windows); the mouse then follows the pointer exactly too. Takes effect from the next start.</p>
    {:else if kind === 'video'}
      <div class="two">
        <label class="ui-field">
          <span class="label">Model</span>
          <select class="ui-input" bind:value={videoModel}>
            <option value="virtio">VirtIO (best for Linux; Windows needs the driver)</option>
            <option value="qxl">QXL (Windows with SPICE tools)</option>
            <option value="vga">Standard VGA (works everywhere)</option>
            <option value="bochs">Bochs (UEFI, no driver needed)</option>
            <option value="ramfb">ramfb (minimal, for passthrough GPUs)</option>
            <option value="cirrus">Cirrus (old guests)</option>
          </select>
        </label>
        <label class="ui-field">
          <span class="label">Monitors</span>
          <select class="ui-input" bind:value={heads}>
            {#each [1, 2, 3, 4] as n (n)}<option value={n}>{n}</option>{/each}
          </select>
          {#if !multiHeadOK}<span class="error">Several monitors need VirtIO or QXL.</span>{/if}
        </label>
      </div>
      <p class="hint">Replaces the current video card; takes effect from the next start.</p>
    {:else if kind === 'mdev'}
      {#if !mdevs}
        <p class="text">Looking at the host's mediated devices…</p>
      {:else}
        <div class="hd-list" role="radiogroup" aria-label="vGPU slice">
          {#each mdevs as m (m.uuid)}
            {@const taken = m.usedBy.length > 0}
            <label class="hd" class:off={taken}>
              <input type="radio" name="mdev" value={m.uuid} bind:group={mdevPick} disabled={taken} />
              <span class="hd-text">
                <span><b>{m.typeName || m.type}</b> <span class="muted">on {m.parent}</span></span>
                <span class="hd-meta mono">{m.uuid}{m.active ? '' : ' · not running'}</span>
                {#if taken}<span class="hd-why">Given to {m.usedBy.join(', ')}</span>{/if}
              </span>
            </label>
          {:else}
            <p class="text">This host has no vGPU slices yet. Create one on the <a href={router.scoped('/hardware')} onclick={(e) => (router.link(e), onclose())}>Hardware</a> page (the card's driver must support mediated devices).</p>
          {/each}
        </div>
      {/if}
    {:else if kind === 'watchdog'}
      <div class="two">
        <label class="ui-field">
          <span class="label">When the guest stops answering</span>
          <select class="ui-input" bind:value={wdAction}>
            <option value="reset">Reset the machine</option>
            <option value="poweroff">Power it off</option>
            <option value="shutdown">Ask the guest to shut down</option>
            <option value="pause">Pause it</option>
            <option value="dump">Save a memory dump</option>
            <option value="inject-nmi">Send an NMI</option>
            <option value="none">Do nothing (log only)</option>
          </select>
        </label>
        <label class="ui-field">
          <span class="label">Model</span>
          <select class="ui-input" bind:value={wdModel}>
            <option value="i6300esb">i6300ESB (PCI, hot-pluggable)</option>
            <option value="itco">iTCO (built into q35)</option>
          </select>
        </label>
      </div>
      <p class="hint">
        The action applies to every watchdog of the machine (q35 machines always have the built-in iTCO). The guest must run a watchdog daemon (e.g. <span class="mono">systemd</span> with RuntimeWatchdogSec) that keeps it from firing.
      </p>
    {:else if kind === 'tpm'}
      <label class="ui-field">
        <span class="label">Interface</span>
        <select class="ui-input" bind:value={tpmModel}>
          <option value="tpm-crb">CRB (recommended, TPM 2.0)</option>
          <option value="tpm-tis">TIS (older guests)</option>
        </select>
      </label>
      <p class="hint">An emulated TPM 2.0 (swtpm); its state is kept with the machine. Takes effect from the next start.</p>
    {:else if kind === 'rng'}
      <p class="text">A VirtIO random number generator fed from the host's <span class="mono">/dev/urandom</span>.</p>
    {/if}

    {#if error}<div class="ui-error-box">{error}</div>{/if}
  </form>
  {#snippet footer()}
    <button class="ui-btn ghost" onclick={onclose}>Cancel</button>
    <button class="ui-btn primary" form="add-device" disabled={busy || !ready}>{busy ? 'Adding…' : 'Add device'}</button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .kinds {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
  }
  .kind {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition:
      border-color var(--dur-fast),
      background var(--dur-fast);
  }
  .kind:hover:not(.off) {
    border-color: var(--border-strong);
  }
  .kind.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .kind.off {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .kind:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .kind input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .icon {
    flex: none;
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .kind.on .icon {
    color: var(--accent);
  }
  .kind-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: var(--text-3);
  }
  .kind-text b {
    color: var(--text);
    font-size: 13px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
    align-items: end;
  }
  .ro {
    padding-bottom: 8px;
  }
  .text {
    margin: 0;
    color: var(--text-2);
    font-size: 13.5px;
  }
  .text a {
    color: var(--accent);
  }
  .hint {
    margin: 0;
    color: var(--text-3);
    font-size: 12px;
  }
  .error {
    color: var(--warn);
    font-size: 12px;
  }
  .muted {
    color: var(--text-3);
  }
  .prep {
    display: grid;
    gap: 4px;
    margin: 0;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 12.5px;
  }
  .prep-title {
    color: var(--text);
    font-weight: 600;
  }
  .prep code {
    user-select: all;
  }
  .hd-list {
    display: grid;
    gap: 6px;
    max-height: 260px;
    overflow: auto;
  }
  .hd {
    display: flex;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    font-size: 13px;
    cursor: pointer;
  }
  .hd.off {
    opacity: 0.65;
    cursor: not-allowed;
  }
  .hd input {
    margin-top: 3px;
  }
  .hd-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .hd-meta {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .hd-why {
    font-size: 12px;
    color: var(--warn);
  }
  @media (max-width: 560px) {
    .kinds,
    .two {
      grid-template-columns: 1fr;
    }
  }
</style>
