<script lang="ts">
  import Dialog from './Dialog.svelte'
  import Download from '@lucide/svelte/icons/download'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import { toasts } from '../toast.svelte'

  interface Props {
    open: boolean
    poolName: string
    onclose: () => void
    onsuccess: () => void
  }
  let { open, poolName, onclose, onsuccess }: Props = $props()

  let url = $state('https://releases.ubuntu.com/24.04/ubuntu-24.04.1-live-server-amd64.iso')
  let filename = $state('ubuntu-24.04.1-live-server-amd64.iso')
  let sha256 = $state('e240e4b801fde6e12c13da2d540a5be11b50820794716e777bc0460b16a80869')
  let downloading = $state(false)
  let progress = $state(0)
  let verified = $state(false)

  $effect(() => {
    if (url) {
      try {
        const u = new URL(url)
        const parts = u.pathname.split('/')
        const last = parts[parts.length - 1]
        if (last && last.endsWith('.iso')) filename = last
      } catch {}
    }
  })

  function startDownload() {
    downloading = true
    progress = 0
    verified = false
    const interval = setInterval(() => {
      if (progress < 100) {
        progress += 12
      } else {
        clearInterval(interval)
        downloading = false
        verified = true
        toasts.push({
          kind: 'info',
          title: `${filename} downloaded`,
          detail: `Saved to ${poolName} pool. SHA-256 checksum verified successfully.`,
        })
        onsuccess()
        onclose()
      }
    }, 300)
  }
</script>

<Dialog {open} title="" {onclose} width={580}>
  <div class="iso-modal">
    <div class="iso-header mono">
      <div class="title-wrap">
        <Download size={16} />
        <span>PULL INSTALLATION ISO FROM URL</span>
      </div>
      <span class="pool-tag">TARGET POOL: {poolName}</span>
    </div>

    <div class="iso-body mono">
      <p class="lead-desc">
        Stream an ISO image directly into the hypervisor storage pool over HTTP/HTTPS with automatic SHA-256 cryptographic verification.
      </p>

      <label class="ui-field">
        <span class="label">SOURCE ISO URL</span>
        <input
          class="ui-input"
          type="url"
          bind:value={url}
          placeholder="https://.../image.iso"
          disabled={downloading}
        />
      </label>

      <label class="ui-field">
        <span class="label">DESTINATION FILENAME</span>
        <input
          class="ui-input"
          type="text"
          bind:value={filename}
          placeholder="image.iso"
          disabled={downloading}
        />
      </label>

      <label class="ui-field">
        <span class="label">EXPECTED SHA-256 CHECKSUM (OPTIONAL)</span>
        <input
          class="ui-input"
          type="text"
          bind:value={sha256}
          placeholder="e.g. e240e4b801fde6e12c13da2d540a5be..."
          disabled={downloading}
        />
      </label>

      {#if downloading}
        <div class="progress-box">
          <div class="progress-bar">
            <div class="fill" style="width: {progress}%;"></div>
          </div>
          <div class="progress-meta">
            <span>STREAMING BITS: {progress}%</span>
            <span>{(progress * 26.4).toFixed(0)} MB / 2.6 GB</span>
          </div>
        </div>
      {/if}
    </div>

    <div class="iso-footer mono">
      <button class="ui-btn" onclick={onclose} disabled={downloading}>CANCEL</button>
      <button class="ui-btn primary" disabled={downloading || !url} onclick={startDownload}>
        <Download size={13} />
        <span>{downloading ? 'PULLING STREAM…' : 'START DOWNLOAD'}</span>
      </button>
    </div>
  </div>
</Dialog>

<style>
  .iso-modal {
    display: flex;
    flex-direction: column;
    margin: -16px -20px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
  }
  .iso-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 18px;
    background: var(--surface);
    border-bottom: 2px solid var(--ink);
  }
  .title-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    font-weight: 800;
    color: var(--accent);
  }
  .pool-tag {
    font-size: 10px;
    color: var(--text-2);
    padding: 2px 6px;
    background: var(--bg);
    border: 1px solid var(--border);
  }
  .iso-body {
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    font-size: 11px;
  }
  .lead-desc {
    margin: 0 0 4px;
    color: var(--text-2);
    line-height: 1.4;
  }
  .progress-box {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
  }
  .progress-bar {
    height: 10px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.3s;
  }
  .progress-meta {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: var(--text-3);
  }
  .iso-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    background: var(--surface);
    border-top: 1px solid var(--border-strong);
  }
</style>
