<script lang="ts">
  // API reference (from GET /api/v1/openapi.json) and ready-to-paste snippets for Prometheus,
  // Terraform and Ansible. Everything here works with an API token from the card below.
  import Plug from '@lucide/svelte/icons/plug'
  import Copy from '@lucide/svelte/icons/copy'
  import ExternalLink from '@lucide/svelte/icons/external-link'
  import { api, errorText, type ApiEndpoint } from '../api'
  import { copy } from '../machine'

  let tab = $state<'api' | 'prometheus' | 'terraform' | 'ansible'>('api')
  let endpoints = $state<ApiEndpoint[] | null>(null)
  let error = $state('')
  let filter = $state('')

  async function loadSpec() {
    if (endpoints) return
    try {
      endpoints = await api.endpoints()
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    if (tab === 'api') void loadSpec()
  })

  let shown = $derived(
    (endpoints ?? []).filter((e) => {
      const q = filter.trim().toLowerCase()
      return !q || e.path.toLowerCase().includes(q) || e.summary.toLowerCase().includes(q) || e.method.toLowerCase() === q
    }),
  )

  const host = location.host
  const origin = location.origin
  let prometheus = $derived(`scrape_configs:
  - job_name: virtomir
    scheme: ${location.protocol.replace(':', '')}
    metrics_path: /api/v1/metrics
    authorization:
      credentials: <API token, viewer role is enough>
    tls_config:
      insecure_skip_verify: true   # or ca_file: the host's certificate
    static_configs:
      - targets: ['${host}']`)
  let terraform = $derived(`terraform {
  required_providers {
    virtomir = { source = "onurbaydas/virtomir" }
  }
}

provider "virtomir" {
  url      = "${origin}"
  token    = var.virtomir_token   # admin token
  insecure = true                 # self-signed certificate
}

resource "virtomir_machine" "web" {
  name      = "web-1"
  os        = "linux"
  vcpus     = 2
  memory_mb = 2048
  pool      = "images"
  disk_gb   = 20
  network   = "default"
}`)
  let ansible = $derived(`- hosts: localhost
  collections: [onurbaydas.virtomir]
  module_defaults:
    group/onurbaydas.virtomir.all:
      url: ${origin}
      token: "{{ lookup('env', 'VIRTOMIR_TOKEN') }}"
      validate_certs: false
  tasks:
    - name: A machine from a template
      machine:
        name: web-1
        template: deb13-tpl
        linked: true
        state: running`)
</script>

<section class="ui-panel card">
  <header class="head">
    <div>
      <h2><Plug size={16} /> API and integrations</h2>
      <p class="hint">Everything the UI does is in the API. Use a token from <em>API tokens</em> below.</p>
    </div>
    <a class="ui-btn ghost" href="/api/v1/openapi.json" target="_blank" rel="noopener"><ExternalLink size={14} /> openapi.json</a>
  </header>

  <div class="tabs" role="tablist">
    {#each [['api', 'Endpoints'], ['prometheus', 'Prometheus'], ['terraform', 'Terraform'], ['ansible', 'Ansible']] as [k, label] (k)}
      <button role="tab" aria-selected={tab === k} class:on={tab === k} onclick={() => (tab = k as typeof tab)}>{label}</button>
    {/each}
  </div>

  {#if tab === 'api'}
    {#if error}
      <div class="ui-error-box">{error}</div>
    {:else if !endpoints}
      <p class="muted">Loading…</p>
    {:else}
      <input class="ui-input filter" placeholder="Filter: domains, snapshot, POST…" bind:value={filter} />
      <div class="eps">
        {#each shown as e (e.method + e.path)}
          <div class="ep">
            <span class="method {e.method.toLowerCase()}">{e.method}</span>
            <span class="mono path">{e.path}</span>
            <span class="sum">{e.summary}</span>
            <span class="role">{e.role}</span>
          </div>
        {/each}
      </div>
      <p class="muted small">{shown.length} of {endpoints.length} endpoints. In a cluster, <span class="mono">/api/v1/hosts/&lt;host&gt;/…</span> reaches another member.</p>
    {/if}
  {:else}
    {@const text = tab === 'prometheus' ? prometheus : tab === 'terraform' ? terraform : ansible}
    <p class="hint">
      {#if tab === 'prometheus'}
        <span class="mono">/api/v1/metrics</span>: host, machines (CPU, per-vCPU, disks, network, memory), pools and open alerts, refreshed every second.
      {:else if tab === 'terraform'}
        The provider lives in <span class="mono">integrations/terraform-provider-virtomir</span> of the VirtoMir repository.
      {:else}
        The collection lives in <span class="mono">integrations/ansible</span> of the VirtoMir repository.
      {/if}
    </p>
    <div class="snippet">
      <pre class="mono">{text}</pre>
      <button class="ui-btn ghost copy" onclick={() => copy(text, 'Snippet')}><Copy size={14} /> Copy</button>
    </div>
  {/if}
</section>

<style>
  .card {
    padding: 16px 18px;
    margin-bottom: 16px;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  .head h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 600;
  }
  .hint {
    margin: 0 0 10px;
    color: var(--text-3);
    font-size: 13px;
  }
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    padding: 6px 12px;
    border: 0;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--text-2);
    font: inherit;
    font-size: 13px;
    cursor: pointer;
  }
  .tabs button.on {
    border-bottom-color: var(--accent);
    color: var(--text);
    font-weight: 600;
  }
  .filter {
    width: 100%;
    margin-bottom: 8px;
  }
  .eps {
    max-height: 340px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 0;
  }
  .ep {
    display: grid;
    grid-template-columns: 64px minmax(200px, 2fr) minmax(120px, 1.5fr) 70px;
    gap: 10px;
    align-items: center;
    padding: 6px 10px;
    border-top: 1px solid var(--border);
    font-size: 12.5px;
  }
  .ep:first-child {
    border-top: 0;
  }
  .method {
    padding: 1px 6px;
    border-radius: 0;
    background: var(--surface-2);
    font-size: 11px;
    font-weight: 700;
    text-align: center;
  }
  .method.get {
    color: var(--accent);
  }
  .method.post,
  .method.put {
    color: var(--ok);
  }
  .method.delete {
    color: var(--danger);
  }
  .path {
    overflow-wrap: anywhere;
  }
  .sum {
    color: var(--text-2);
  }
  .role {
    color: var(--text-3);
    font-size: 11.5px;
  }
  .snippet {
    position: relative;
  }
  .snippet pre {
    margin: 0;
    padding: 12px;
    border-radius: 0;
    background: var(--surface-2);
    font-size: 12px;
    overflow: auto;
  }
  .copy {
    position: absolute;
    top: 6px;
    right: 6px;
  }
  .muted {
    color: var(--text-3);
  }
  .small {
    margin: 8px 0 0;
    font-size: 12px;
  }
  @media (max-width: 760px) {
    .ep {
      grid-template-columns: 56px 1fr;
    }
    .sum,
    .role {
      display: none;
    }
  }
</style>
