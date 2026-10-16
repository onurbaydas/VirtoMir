<script lang="ts">
  // The machine being designed as code: the exact request this page sends to VirtoMir
  // (curl, Ansible), and an equivalent virt-install command for hosts without VirtoMir.
  // Passwords are never written into the code.
  import { toasts } from '../toast.svelte'
  import Copy from '@lucide/svelte/icons/copy'
  import Check from '@lucide/svelte/icons/check'
  import type { CreateSpec } from '../api'

  let { spec }: { spec: CreateSpec } = $props()

  type Format = 'curl' | 'ansible' | 'virt-install'
  let format = $state<Format>('curl')
  let copied = $state(false)

  // The spec as sent, with a cloud-init password replaced by a placeholder.
  let safe = $derived.by(() => {
    const s: CreateSpec = JSON.parse(JSON.stringify(spec))
    if (s.cloudInit?.password) s.cloudInit.password = '<password>'
    return s
  })
  let body = $derived(JSON.stringify(safe, null, 2))
  const indent = (t: string, n: number) => t.split('\n').map((l, i) => (i ? ' '.repeat(n) + l : l)).join('\n')

  let generatedCode = $derived.by(() => {
    const s = safe
    switch (format) {
      case 'curl':
        return `# VirtoMir API (token: Account → API tokens)
curl -k -X POST '${location.origin}/api/v1/domains' \\
  -H "Authorization: Bearer $VIRTOMIR_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -d @- <<'EOF'
${body}
EOF`
      case 'ansible':
        return `# Ansible task against the VirtoMir API
- name: Create machine ${s.name}
  ansible.builtin.uri:
    url: "${location.origin}/api/v1/domains"
    method: POST
    headers:
      Authorization: "Bearer {{ virtomir_token }}"
    body_format: json
    body: ${indent(body, 6)}
    status_code: 201
    validate_certs: false`
      case 'virt-install': {
        const net = s.network?.network ? `network=${s.network.network},model=virtio` : s.network?.bridge ? `bridge=${s.network.bridge},model=virtio` : 'none'
        const disk = s.diskPath ? `path=${s.diskPath}` : s.baseImage ? `path=${s.baseImage},size=${s.diskGiB} # copy the image first` : s.diskGiB ? `pool=${s.pool},size=${s.diskGiB},format=qcow2,bus=virtio` : 'none'
        const lines = [
          `virt-install`,
          `--name ${s.name || 'new-machine'}`,
          `--vcpus ${s.vcpus}`,
          `--memory ${s.memoryMiB}`,
          `--osinfo ${s.os === 'windows' ? 'win11' : s.os === 'linux' ? 'linux2022' : 'generic'}`,
          `--machine q35`,
          s.firmware === 'uefi' ? `--boot uefi${s.secureBoot ? ',firmware.feature0.name=secure-boot,firmware.feature0.enabled=yes' : ''}` : `--boot hd`,
          ...(s.tpm ? ['--tpm emulator,model=tpm-crb'] : []),
          `--disk ${disk}`,
          ...(s.iso ? [`--cdrom ${s.iso}`] : []),
          `--network ${net}`,
          `--graphics vnc,listen=127.0.0.1`,
          ...(s.start ? [] : ['--noreboot --print-xml > machine.xml  # define only']),
          `--noautoconsole`,
        ]
        return `# Equivalent on a host without VirtoMir (cloud-init not included)\n${lines.join(' \\\n  ')}`
      }
    }
  })

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(generatedCode)
      copied = true
      toasts.push({ kind: 'info', title: 'Copied' })
      setTimeout(() => (copied = false), 2000)
    } catch {
      toasts.push({ kind: 'warn', title: 'Clipboard copy failed' })
    }
  }
</script>

<div class="iac-panel">
  <div class="panel-head">
    <div class="title">
      <span class="dot"></span>
      <span>AS CODE</span>
    </div>
    <div class="formats">
      <button class:active={format === 'curl'} onclick={() => (format = 'curl')}>API (CURL)</button>
      <button class:active={format === 'ansible'} onclick={() => (format = 'ansible')}>ANSIBLE</button>
      <button class:active={format === 'virt-install'} onclick={() => (format = 'virt-install')}>VIRT-INSTALL</button>
    </div>
  </div>

  <div class="code-container">
    <pre><code>{generatedCode}</code></pre>
    <button class="copy-btn" onclick={copyCode} title="Copy code">
      {#if copied}
        <Check size={13} />
        <span>COPIED</span>
      {:else}
        <Copy size={13} />
        <span>COPY</span>
      {/if}
    </button>
  </div>
</div>

<style>
  .iac-panel {
    border: var(--frame) solid var(--border-strong);
    background: var(--surface);
    box-shadow: var(--shadow-hard);
    display: flex;
    flex-direction: column;
    margin-top: 16px;
  }
  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 12px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 8px;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 700;
    color: var(--accent);
  }
  .dot {
    width: 6px;
    height: 6px;
    background: var(--accent);
  }
  .formats {
    display: flex;
    gap: 2px;
  }
  .formats button {
    padding: 2px 8px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-2);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
    cursor: pointer;
  }
  .formats button.active {
    background: var(--accent);
    color: var(--accent-contrast);
    border-color: var(--accent);
  }
  .code-container {
    position: relative;
    background: #0d0e10;
    color: #4fd27b;
    padding: 12px;
    max-height: 220px;
    overflow-y: auto;
  }
  pre {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.45;
    white-space: pre-wrap;
    user-select: text;
  }
  .copy-btn {
    position: absolute;
    top: 8px;
    right: 8px;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 8px;
    border: 1px solid #333;
    background: #1a1c20;
    color: #eee;
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
    cursor: pointer;
  }
  .copy-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
</style>
