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
      <span>As code</span>
    </div>
    <div class="formats">
      <button class:active={format === 'curl'} onclick={() => (format = 'curl')}>API (curl)</button>
      <button class:active={format === 'ansible'} onclick={() => (format = 'ansible')}>Ansible</button>
      <button class:active={format === 'virt-install'} onclick={() => (format = 'virt-install')}>virt-install</button>
    </div>
  </div>

  <div class="code-container">
    <pre><code>{generatedCode}</code></pre>
    <button class="copy-btn" onclick={copyCode} title="Copy code">
      {#if copied}
        <Check size={13} />
        <span>Copied</span>
      {:else}
        <Copy size={13} />
        <span>Copy</span>
      {/if}
    </button>
  </div>
</div>

<style>
  .iac-panel {
    display: flex;
    flex-direction: column;
    margin-top: 16px;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--surface);
  }
  .panel-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border);
  }
  .title {
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
  }
  .dot {
    display: none;
  }
  .formats {
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: var(--radius-sm);
    background: var(--bg-elev);
  }
  .formats button {
    padding: 3px 9px;
    border: 0;
    border-radius: 4px;
    background: none;
    color: var(--text-2);
    font-size: 12px;
    cursor: pointer;
  }
  .formats button.active {
    background: var(--surface-hover);
    color: var(--text);
  }
  .code-container {
    position: relative;
    max-height: 240px;
    overflow-y: auto;
    padding: 12px;
    background: var(--bg-elev);
    color: var(--text-2);
  }
  pre {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.5;
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
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text-2);
    font-size: 12px;
    cursor: pointer;
  }
  .copy-btn:hover {
    color: var(--text);
  }
</style>
