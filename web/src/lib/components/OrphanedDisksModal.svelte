<script lang="ts">
  import Dialog from './Dialog.svelte'
  import Search from '@lucide/svelte/icons/search'
  import Trash from '@lucide/svelte/icons/trash'
  import Plus from '@lucide/svelte/icons/plus'
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import CheckCircle from '@lucide/svelte/icons/check-circle'
  import { toasts } from '../toast.svelte'

  interface Props {
    open: boolean
    onclose: () => void
  }
  let { open, onclose }: Props = $props()

  interface OrphanedDisk {
    id: string
    path: string
    pool: string
    sizeGiB: number
    format: string
    lastModified: string
    possibleOrigin: string
  }

  let scanning = $state(false)
  let scanned = $state(false)
  let orphans = $state<OrphanedDisk[]>([
    {
      id: 'o1',
      path: '/var/lib/libvirt/images/old-ubuntu-build-disk.qcow2',
      pool: 'default',
      sizeGiB: 24.5,
      format: 'QCOW2',
      lastModified: '2026-09-12 14:22',
      possibleOrigin: 'Deleted VM: ubuntu-ci-builder',
    },
    {
      id: 'o2',
      path: '/var/lib/libvirt/images/test-k8s-worker-stale.raw',
      pool: 'default',
      sizeGiB: 40.0,
      format: 'RAW',
      lastModified: '2026-08-30 09:15',
      possibleOrigin: 'Unreferenced volume',
    },
    {
      id: 'o3',
      path: '/mnt/nvme-pool/orphan-data-scratch.qcow2',
      pool: 'nvme-fast',
      sizeGiB: 120.0,
      format: 'QCOW2',
      lastModified: '2026-10-02 18:40',
      possibleOrigin: 'Manual qemu-img creation',
    },
  ])

  function scanPools() {
    scanning = true
    setTimeout(() => {
      scanning = false
      scanned = true
      toasts.push({
        kind: 'info',
        title: 'Storage scan complete',
        detail: `Found ${orphans.length} unreferenced disk volumes across active pools.`,
      })
    }, 800)
  }

  function purgeDisk(o: OrphanedDisk) {
    orphans = orphans.filter((x) => x.id !== o.id)
    toasts.push({
      kind: 'info',
      title: 'Orphaned volume purged',
      detail: `Permanently removed ${o.path} (${o.sizeGiB} GiB freed).`,
    })
  }

  function attachDisk(o: OrphanedDisk) {
    toasts.push({
      kind: 'info',
      title: 'Disk ready for attachment',
      detail: `Select a machine on its Hardware tab and choose Add Storage → Existing volume.`,
    })
  }
</script>

<Dialog {open} title="" {onclose} width={680}>
  <div class="orphaned-dialog">
    <div class="orphaned-header mono">
      <div class="title-wrap">
        <HardDrive size={16} />
        <span>ORPHANED & UNREFERENCED DISK SCANNER</span>
      </div>
      <button class="scan-btn" disabled={scanning} onclick={scanPools}>
        <Search size={12} />
        <span>{scanning ? 'SCANNING POOLS…' : 'SCAN ALL POOLS'}</span>
      </button>
    </div>

    <div class="orphaned-body mono">
      <p class="lead-text">
        Scans all active storage pools for virtual disk images (.qcow2, .raw, .img) that are not attached to any domain XML definition in the cluster.
      </p>

      <div class="table-wrap">
        <table class="orphan-table">
          <thead>
            <tr>
              <th>IMAGE PATH & POOL</th>
              <th>FORMAT</th>
              <th>SIZE</th>
              <th>LAST MODIFIED</th>
              <th>DIAGNOSTIC ORIGIN</th>
              <th>ACTIONS</th>
            </tr>
          </thead>
          <tbody>
            {#each orphans as o (o.id)}
              <tr>
                <td>
                  <div class="path-cell">
                    <strong>{o.path.split('/').pop()}</strong>
                    <span class="full-path">{o.path} [pool: {o.pool}]</span>
                  </div>
                </td>
                <td><span class="fmt-badge">{o.format}</span></td>
                <td><strong>{o.sizeGiB} GiB</strong></td>
                <td>{o.lastModified}</td>
                <td><span class="origin-text">{o.possibleOrigin}</span></td>
                <td>
                  <div class="act-row">
                    <button class="act-btn" onclick={() => attachDisk(o)} title="Attach to a virtual machine">
                      <Plus size={11} /> ATTACH
                    </button>
                    <button class="act-btn purge" onclick={() => purgeDisk(o)} title="Permanently delete file">
                      <Trash size={11} /> PURGE
                    </button>
                  </div>
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="6" class="empty-text">
                  <CheckCircle size={16} class="ok-icon" />
                  <span>No orphaned virtual disks detected. All pool volumes are accounted for.</span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <div class="orphaned-footer mono">
      <div class="summary-stats">
        <span>TOTAL RECLAIMABLE: <strong>{orphans.reduce((n, o) => n + o.sizeGiB, 0)} GiB</strong></span>
      </div>
      <button class="ui-btn" onclick={onclose}>CLOSE</button>
    </div>
  </div>
</Dialog>

<style>
  .orphaned-dialog {
    display: flex;
    flex-direction: column;
    margin: -16px -20px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
  }
  .orphaned-header {
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
  .scan-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 10px;
    background: var(--metal);
    border: 1px solid var(--border-strong);
    color: var(--text);
    font-family: inherit;
    font-size: 11px;
    font-weight: 700;
    cursor: pointer;
    box-shadow: 1px 1px 0 var(--ink);
  }
  .scan-btn:hover:not(:disabled) {
    background: var(--metal-hover);
  }
  .orphaned-body {
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    font-size: 11px;
  }
  .lead-text {
    margin: 0;
    color: var(--text-2);
    line-height: 1.4;
  }
  .table-wrap {
    max-height: 280px;
    overflow-y: auto;
    border: 1px solid var(--border-strong);
  }
  .orphan-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11px;
    text-align: left;
  }
  .orphan-table th {
    background: var(--surface);
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-strong);
    color: var(--text-3);
    font-weight: 700;
  }
  .orphan-table td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  .path-cell {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .full-path {
    font-size: 9.5px;
    color: var(--text-3);
  }
  .fmt-badge {
    padding: 1px 5px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 9.5px;
    font-weight: 700;
  }
  .origin-text {
    color: var(--warn);
    font-size: 10.5px;
  }
  .act-row {
    display: flex;
    gap: 4px;
  }
  .act-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding: 0 6px;
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    font-family: inherit;
    font-size: 10px;
    cursor: pointer;
  }
  .act-btn:hover {
    border-color: var(--accent);
    color: var(--accent);
  }
  .act-btn.purge:hover {
    border-color: var(--danger);
    color: var(--danger);
  }
  .empty-text {
    text-align: center;
    padding: 30px;
    color: var(--text-2);
  }
  .orphaned-footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 20px;
    background: var(--surface);
    border-top: 1px solid var(--border-strong);
  }
  .summary-stats {
    font-size: 11px;
    color: var(--text-2);
  }
  .summary-stats strong {
    color: var(--accent);
  }
</style>
