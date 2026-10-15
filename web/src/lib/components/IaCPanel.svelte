<script lang="ts">
  import { toasts } from '../toast.svelte'
  import Copy from '@lucide/svelte/icons/copy'
  import Check from '@lucide/svelte/icons/check'

  let {
    name = 'vm-01',
    os = 'linux',
    vcpus = 2,
    memGiB = 2,
    diskGiB = 20,
    pool = 'default',
    firmware = 'uefi',
    secureBoot = false,
    tpm = false,
    netChoice = 'network:default',
  }: {
    name: string
    os: string
    vcpus: number
    memGiB: number
    diskGiB: number
    pool: string
    firmware: string
    secureBoot: boolean
    tpm: boolean
    netChoice: string
  } = $props()

  type Format = 'cli' | 'qm' | 'terraform' | 'ansible' | 'json'
  let format = $state<Format>('cli')
  let copied = $state(false)

  let memMiB = $derived(Math.round(memGiB * 1024))
  let netName = $derived(netChoice.startsWith('network:') ? netChoice.slice(8) : netChoice)

  let generatedCode = $derived.by(() => {
    const cleanName = name || 'new-vm'
    switch (format) {
      case 'cli':
        return `# Libvirt CLI (virt-install / virsh):
virt-install \\
  --name "${cleanName}" \\
  --vcpus ${vcpus} \\
  --memory ${memMiB} \\
  --os-variant ${os === 'linux' ? 'ubuntu24.04' : os === 'windows' ? 'win11' : 'generic'} \\
  --disk pool=${pool},size=${diskGiB},bus=virtio,cache=none \\
  --network ${netChoice.startsWith('network:') ? `network=${netName}` : `bridge=${netName}`} \\
  --boot ${firmware === 'uefi' ? 'uefi' : 'hd'} \\
  --graphics vnc,listen=127.0.0.1 \\
  --channel spicevmc,target_type=virtio,name=com.redhat.spice.0 \\
  --noautoconsole`

      case 'qm':
        return `# QEMU / Proxmox CLI:
qm create 108 \\
  --name "${cleanName}" \\
  --cores ${vcpus} --sockets 1 \\
  --cpu host \\
  --memory ${memMiB} \\
  --scsihw virtio-scsi-single \\
  --scsi0 ${pool}:${diskGiB},discard=on,ssd=1 \\
  --net0 virtio,bridge=${netName}${firmware === 'uefi' ? ' \\\n  --bios ovmf' : ''}${tpm ? ' \\\n  --tpmstate0 pool:tpm' : ''}`

      case 'terraform':
        return `# HashiCorp Terraform (libvirt provider):
resource "libvirt_volume" "${cleanName}_disk" {
  name   = "${cleanName}-disk0.qcow2"
  pool   = "${pool}"
  size   = ${diskGiB * 1024 * 1024 * 1024}
  format = "qcow2"
}

resource "libvirt_domain" "${cleanName}" {
  name   = "${cleanName}"
  memory = ${memMiB}
  vcpu   = ${vcpus}

  disk {
    volume_id = libvirt_volume.${cleanName}_disk.id
  }

  network_interface {
    network_name = "${netName}"
  }

  graphics {
    type        = "vnc"
    listen_type = "address"
  }
}`

      case 'ansible':
        return `# Ansible Playbook Task:
- name: Deploy virtual machine ${cleanName}
  community.libvirt.virt:
    name: "${cleanName}"
    state: running
    command: define
    xml: |
      <domain type='kvm'>
        <name>${cleanName}</name>
        <memory unit='MiB'>${memMiB}</memory>
        <vcpu>${vcpus}</vcpu>
        <os>
          <type arch='x86_64' machine='q35'>hvm</type>
          ${firmware === 'uefi' ? '<loader readonly="yes" type="pflash">/usr/share/OVMF/OVMF_CODE.fd</loader>' : ''}
        </os>
      </domain>`

      case 'json':
        return JSON.stringify(
          {
            name: cleanName,
            os,
            vcpus,
            memoryMiB: memMiB,
            firmware,
            secureBoot,
            tpm,
            pool,
            diskGiB,
            network: { [netChoice.startsWith('network:') ? 'network' : 'bridge']: netName },
            start: true,
          },
          null,
          2,
        )
    }
  })

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(generatedCode)
      copied = true
      toasts.push({ kind: 'info', title: 'IaC code copied to clipboard' })
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
      <span>REAL-TIME IaC & CLI CODE GENERATOR</span>
    </div>
    <div class="formats">
      <button class:active={format === 'cli'} onclick={() => (format = 'cli')}>VIRSH</button>
      <button class:active={format === 'qm'} onclick={() => (format = 'qm')}>QM</button>
      <button class:active={format === 'terraform'} onclick={() => (format = 'terraform')}>TERRAFORM</button>
      <button class:active={format === 'ansible'} onclick={() => (format = 'ansible')}>ANSIBLE</button>
      <button class:active={format === 'json'} onclick={() => (format = 'json')}>REST JSON</button>
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
