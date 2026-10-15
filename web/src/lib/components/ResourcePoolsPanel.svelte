<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../api'
  import { toasts } from '../toast.svelte'
  import { live } from '../live.svelte'
  import Layers from '@lucide/svelte/icons/layers'
  import Plus from '@lucide/svelte/icons/plus'
  import Trash2 from '@lucide/svelte/icons/trash-2'
  import Edit3 from '@lucide/svelte/icons/edit-3'
  import Box from '@lucide/svelte/icons/box'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Cpu from '@lucide/svelte/icons/cpu'
  import Database from '@lucide/svelte/icons/database'
  import Dialog from './Dialog.svelte'

  interface ResourcePool {
    id: string
    name: string
    parentId?: string
    description?: string
    cpuShares: number
    cpuLimit: number
    cpuReservation: number
    memoryLimitMiB: number
    memoryReservationMiB: number
    storagePoolQuotas: Record<string, number>
    iopsLimit: number
    assignedVms: string[]
    owners: string[]
  }

  interface PoolUsage {
    poolId: string
    vmCount: number
    usedVcpus: number
    usedMemoryMiB: number
    usedStorageBytes: Record<string, number>
  }

  let pools = $state<ResourcePool[]>([])
  let usages = $state<PoolUsage[]>([])
  let loading = $state(true)

  let createModalOpen = $state(false)
  let assignModalOpen = $state(false)
  let selectedPool = $state<ResourcePool | null>(null)

  let newPool = $state({
    name: '',
    parentId: '',
    description: '',
    cpuShares: 1000,
    cpuLimit: 0,
    cpuReservation: 0,
    memoryLimitGiB: 0,
    memoryReservationGiB: 0,
    storageQuotaGiB: 0,
  })

  let vmToAssign = $state('')

  async function loadData() {
    loading = true
    try {
      const [p, u] = await Promise.all([
        api.get<ResourcePool[]>('/api/v1/respools'),
        api.get<PoolUsage[]>('/api/v1/respools/usage'),
      ])
      pools = p
      usages = u
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to load Resource Pools', detail: e.message })
    } finally {
      loading = false
    }
  }

  onMount(loadData)

  async function handleCreatePool() {
    if (!newPool.name.trim()) return
    try {
      await api.post('/api/v1/respools', {
        name: newPool.name.trim(),
        parentId: newPool.parentId || undefined,
        description: newPool.description,
        cpuShares: newPool.cpuShares,
        cpuLimit: newPool.cpuLimit,
        cpuReservation: newPool.cpuReservation,
        memoryLimitMiB: newPool.memoryLimitGiB * 1024,
        memoryReservationMiB: newPool.memoryReservationGiB * 1024,
        storagePoolQuotas: newPool.storageQuotaGiB > 0 ? { default: newPool.storageQuotaGiB * 1024 * 1024 * 1024 } : {},
      })
      createModalOpen = false
      newPool.name = ''
      newPool.description = ''
      toasts.push({ kind: 'info', title: 'Resource Pool Created' })
      await loadData()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to create pool', detail: e.message })
    }
  }

  async function handleDeletePool(id: string) {
    if (!confirm('Are you sure you want to delete this resource pool?')) return
    try {
      await api.delete(`/api/v1/respools/${id}`)
      toasts.push({ kind: 'info', title: 'Resource Pool Deleted' })
      await loadData()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Failed to delete pool', detail: e.message })
    }
  }

  async function handleAssignVM() {
    if (!selectedPool || !vmToAssign) return
    try {
      await api.post(`/api/v1/respools/${selectedPool.id}/assign`, { vmUuid: vmToAssign })
      assignModalOpen = false
      vmToAssign = ''
      toasts.push({ kind: 'info', title: 'VM Assigned to Pool', detail: selectedPool.name })
      await loadData()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Assignment failed', detail: e.message })
    }
  }

  async function handleUnassignVM(poolId: string, vmUuid: string) {
    try {
      await api.post(`/api/v1/respools/${poolId}/assign`, { vmUuid, unassign: true })
      toasts.push({ kind: 'info', title: 'VM Removed from Pool' })
      await loadData()
    } catch (e: any) {
      toasts.push({ kind: 'error', title: 'Unassign failed', detail: e.message })
    }
  }

  function getUsage(poolId: string): PoolUsage {
    return usages.find(u => u.poolId === poolId) ?? {
      poolId,
      vmCount: 0,
      usedVcpus: 0,
      usedMemoryMiB: 0,
      usedStorageBytes: {},
    }
  }

  let domains = $derived(live.snapshot?.domains ?? [])
</script>

<div class="pools-deck">
  <div class="deck-actions">
    <div>
      <h2 class="title mono">HIERARCHICAL RESOURCE POOLS & MULTI-TENANCY</h2>
      <div class="sub">Organize guest VMs into bounded compute, memory, and storage resource quotas.</div>
    </div>
    <button class="ui-btn primary mono" onclick={() => createModalOpen = true}>
      <Plus size={14} /> NEW RESOURCE POOL
    </button>
  </div>

  {#if !pools.length}
    <div class="ui-panel empty-deck">
      <Layers size={36} />
      <h3>No Resource Pools Configured</h3>
      <p>Resource pools allow allocating hierarchical compute shares and storage caps across departments or tenants.</p>
      <button class="ui-btn primary mono" onclick={() => createModalOpen = true}>
        <Plus size={14} /> CREATE FIRST POOL
      </button>
    </div>
  {:else}
    <div class="pools-grid">
      {#each pools as p (p.id)}
        {@const u = getUsage(p.id)}
        {@const vcpuPct = p.cpuLimit > 0 ? Math.min(100, (u.usedVcpus / p.cpuLimit) * 100) : 0}
        {@const memLimitGiB = p.memoryLimitMiB / 1024}
        {@const usedMemGiB = u.usedMemoryMiB / 1024}
        {@const memPct = memLimitGiB > 0 ? Math.min(100, (usedMemGiB / memLimitGiB) * 100) : 0}

        <div class="ui-panel pool-card">
          <header class="p-header">
            <div>
              <div class="p-title mono">
                <Layers size={16} />
                <span>{p.name}</span>
                {#if p.parentId}
                  <span class="sub-tag mono">SUB-POOL</span>
                {/if}
              </div>
              <div class="p-desc">{p.description || 'No description provided.'}</div>
            </div>
            <div class="p-acts">
              <button
                class="ui-btn small mono"
                onclick={() => { selectedPool = p; assignModalOpen = true }}
                title="Assign VM to this pool"
              >
                + ASSIGN VM
              </button>
              <button
                class="ui-btn danger small icon-only"
                onclick={() => handleDeletePool(p.id)}
                title="Delete Resource Pool"
              >
                <Trash2 size={13} />
              </button>
            </div>
          </header>

          <div class="meters">
            <!-- CPU Meter -->
            <div class="meter-box">
              <div class="m-head mono">
                <span class="m-label"><Cpu size={12} /> VCPUS</span>
                <span class="m-val">{u.usedVcpus} / {p.cpuLimit > 0 ? p.cpuLimit : '∞'}</span>
              </div>
              <div class="meter-bar">
                <div class="meter-fill" style="width: {p.cpuLimit > 0 ? vcpuPct : 10}%"></div>
              </div>
              <div class="m-foot mono">Shares: {p.cpuShares} · Min: {p.cpuReservation}</div>
            </div>

            <!-- Memory Meter -->
            <div class="meter-box">
              <div class="m-head mono">
                <span class="m-label"><Database size={12} /> MEMORY</span>
                <span class="m-val">{usedMemGiB.toFixed(1)} / {memLimitGiB > 0 ? memLimitGiB.toFixed(0) : '∞'} GiB</span>
              </div>
              <div class="meter-bar">
                <div class="meter-fill warn" style="width: {memLimitGiB > 0 ? memPct : 10}%"></div>
              </div>
              <div class="m-foot mono">Min Reserved: {(p.memoryReservationMiB/1024).toFixed(1)} GiB</div>
            </div>
          </div>

          <!-- Assigned VMs List -->
          <div class="assigned-vms">
            <div class="av-head mono">
              <span>ASSIGNED WORKLOADS ({p.assignedVms.length})</span>
            </div>
            {#if !p.assignedVms.length}
              <div class="empty-vms mono">No virtual machines currently assigned to this pool.</div>
            {:else}
              <div class="vms-list">
                {#each p.assignedVms as vmUuid}
                  {@const vm = domains.find(d => d.uuid === vmUuid)}
                  <div class="vm-chip mono">
                    <Box size={12} />
                    <span class="vm-name">{vm?.name ?? vmUuid.slice(0, 8)}</span>
                    <button class="remove-btn" onclick={() => handleUnassignVM(p.id, vmUuid)} title="Remove from pool">×</button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Create Pool Modal -->
<Dialog title="NEW RESOURCE POOL" bind:open={createModalOpen}>
  <form class="create-form" onsubmit={(e) => { e.preventDefault(); handleCreatePool() }}>
    <div class="field">
      <label class="mono" for="pool-name">POOL NAME</label>
      <input id="pool-name" type="text" bind:value={newPool.name} placeholder="e.g. Production-Cluster" required />
    </div>

    <div class="field">
      <label class="mono" for="parent-pool">PARENT POOL (OPTIONAL HIERARCHY)</label>
      <select id="parent-pool" bind:value={newPool.parentId}>
        <option value="">None (Top-Level Pool)</option>
        {#each pools as p}
          <option value={p.id}>{p.name}</option>
        {/each}
      </select>
    </div>

    <div class="field">
      <label class="mono" for="pool-desc">DESCRIPTION</label>
      <input id="pool-desc" type="text" bind:value={newPool.description} placeholder="Workload isolation notes..." />
    </div>

    <div class="grid-2">
      <div class="field">
        <label class="mono" for="cpu-limit">VCPU HARD LIMIT (0 = UNLIMITED)</label>
        <input id="cpu-limit" type="number" min="0" bind:value={newPool.cpuLimit} />
      </div>
      <div class="field">
        <label class="mono" for="cpu-res">VCPU RESERVATION</label>
        <input id="cpu-res" type="number" min="0" bind:value={newPool.cpuReservation} />
      </div>
    </div>

    <div class="grid-2">
      <div class="field">
        <label class="mono" for="mem-limit">MEMORY LIMIT (GIB, 0 = UNLIMITED)</label>
        <input id="mem-limit" type="number" min="0" bind:value={newPool.memoryLimitGiB} />
      </div>
      <div class="field">
        <label class="mono" for="mem-res">MEMORY RESERVATION (GIB)</label>
        <input id="mem-res" type="number" min="0" bind:value={newPool.memoryReservationGiB} />
      </div>
    </div>

    <div class="field">
      <label class="mono" for="storage-quota">STORAGE QUOTA ON DEFAULT POOL (GIB, 0 = UNLIMITED)</label>
      <input id="storage-quota" type="number" min="0" bind:value={newPool.storageQuotaGiB} />
    </div>

    <div class="form-actions">
      <button type="button" class="ui-btn mono" onclick={() => createModalOpen = false}>CANCEL</button>
      <button type="submit" class="ui-btn primary mono">CREATE RESOURCE POOL</button>
    </div>
  </form>
</Dialog>

<!-- Assign VM Modal -->
<Dialog title="ASSIGN VM TO RESOURCE POOL" bind:open={assignModalOpen}>
  <form class="create-form" onsubmit={(e) => { e.preventDefault(); handleAssignVM() }}>
    <div class="field">
      <label class="mono" for="assign-vm">SELECT VIRTUAL MACHINE</label>
      <select id="assign-vm" bind:value={vmToAssign} required>
        <option value="">-- Choose Virtual Machine --</option>
        {#each domains as d}
          <option value={d.uuid}>{d.name} ({d.vcpus} vCPUs, {(d.memoryKiB/1024/1024).toFixed(1)} GiB RAM)</option>
        {/each}
      </select>
    </div>
    <div class="form-actions">
      <button type="button" class="ui-btn mono" onclick={() => assignModalOpen = false}>CANCEL</button>
      <button type="submit" class="ui-btn primary mono">ASSIGN TO POOL</button>
    </div>
  </form>
</Dialog>

<style>
  .pools-deck {
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  .deck-actions {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .title {
    margin: 0;
    font-size: 16px;
    font-weight: 700;
    letter-spacing: 0.05em;
  }
  .sub {
    font-size: 12px;
    color: var(--text-dim);
    margin-top: 2px;
  }
  .empty-deck {
    text-align: center;
    padding: 48px;
    color: var(--text-dim);
  }
  .empty-deck h3 {
    margin: 12px 0 6px;
    color: var(--text);
  }
  .pools-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
    gap: 16px;
  }
  .pool-card {
    padding: 16px;
    background: var(--surface-1);
    border: 1px solid var(--border-strong);
    box-shadow: 2px 2px 0 0 var(--border-strong);
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .p-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
  }
  .p-title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
    font-weight: 700;
  }
  .sub-tag {
    font-size: 9px;
    padding: 1px 4px;
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .p-desc {
    font-size: 11px;
    color: var(--text-dim);
    margin-top: 3px;
  }
  .p-acts {
    display: flex;
    gap: 6px;
  }
  .meters {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .meter-box {
    background: var(--surface-2);
    border: 1px solid var(--border);
    padding: 8px 10px;
  }
  .m-head {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    margin-bottom: 4px;
  }
  .m-label {
    display: flex;
    align-items: center;
    gap: 4px;
    color: var(--text-dim);
  }
  .meter-bar {
    height: 6px;
    background: var(--surface-3);
    overflow: hidden;
  }
  .meter-fill {
    height: 100%;
    background: var(--ok);
  }
  .meter-fill.warn {
    background: var(--accent);
  }
  .m-foot {
    font-size: 9px;
    color: var(--text-dim);
    margin-top: 4px;
  }
  .assigned-vms {
    border-top: 1px solid var(--border);
    padding-top: 10px;
  }
  .av-head {
    font-size: 10px;
    color: var(--text-dim);
    margin-bottom: 8px;
  }
  .empty-vms {
    font-size: 11px;
    color: var(--text-dim);
    font-style: italic;
  }
  .vms-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .vm-chip {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    font-size: 11px;
  }
  .remove-btn {
    background: transparent;
    border: none;
    color: var(--text-dim);
    cursor: pointer;
    font-size: 12px;
    padding: 0;
  }
  .remove-btn:hover {
    color: var(--danger);
  }
  .create-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .field label {
    font-size: 11px;
    color: var(--text-dim);
  }
  .field input, .field select {
    padding: 8px 10px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--text);
    font-family: inherit;
    font-size: 13px;
  }
  .grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 8px;
  }
</style>
