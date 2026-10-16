<script lang="ts">
  // Disk images no machine uses: not attached, not a backing image of a linked clone. On
  // shared pools (NFS, Ceph, iSCSI) every reachable member is asked too, so a disk of a
  // machine on another host is never offered for deletion. ISOs are listed apart: an unused
  // installer image is normal.
  import Dialog from './Dialog.svelte'
  import Trash from '@lucide/svelte/icons/trash'
  import { api, errorText, onHost, type Volume } from '../api'
  import { cluster } from '../cluster.svelte'
  import { formatBytes } from '../format'
  import { session } from '../session.svelte'
  import { toasts } from '../toast.svelte'
  import ConfirmButton from './ConfirmButton.svelte'

  let { open, onclose }: { open: boolean; onclose: () => void } = $props()

  interface Orphan { pool: string; vol: Volume; shared: boolean }
  let disks = $state<Orphan[]>([])
  let isos = $state<Orphan[]>([])
  let scanning = $state(false)
  let error = $state('')
  let unchecked = $state<string[]>([])

  const SHARED = ['netfs', 'rbd', 'iscsi', 'iscsi-direct', 'gluster']

  async function scan() {
    scanning = true
    error = ''
    unchecked = []
    try {
      const pools = (await api.pools()).filter((p) => p.active)
      const others = cluster.clustered ? cluster.members.filter((m) => !(cluster.current ? m.id === cluster.current.id : m.self)) : []
      const d: Orphan[] = []
      const i: Orphan[] = []
      for (const p of pools) {
        const vols = await api.volumes(p.name)
        const shared = SHARED.includes(p.type)
        const usedElsewhere = new Set<string>()
        if (shared) {
          for (const m of others) {
            try {
              const r = await api.get<{ volumes: Volume[] }>(onHost(m.id, `/api/v1/storage/pools/${encodeURIComponent(p.name)}/volumes`))
              for (const v of r.volumes) if (v.usedBy.length) usedElsewhere.add(v.path)
            } catch {
              if (!unchecked.includes(m.name)) unchecked.push(m.name)
            }
          }
        }
        for (const v of vols) {
          if (v.usedBy.length || usedElsewhere.has(v.path) || v.type === 'dir') continue
          ;(v.format === 'iso' || v.name.toLowerCase().endsWith('.iso') ? i : d).push({ pool: p.name, vol: v, shared })
        }
      }
      disks = d.sort((a, b) => b.vol.capacityBytes - a.vol.capacityBytes)
      isos = i
    } catch (e) {
      error = errorText(e)
    } finally {
      scanning = false
    }
  }
  $effect(() => {
    if (open) void scan()
  })

  async function remove(o: Orphan) {
    await api.deleteVolume(o.pool, o.vol.name)
    toasts.push({ kind: 'info', title: `${o.vol.name} deleted` })
    disks = disks.filter((x) => x !== o)
    isos = isos.filter((x) => x !== o)
  }
  let total = $derived(disks.reduce((n, o) => n + o.vol.allocationBytes, 0))
</script>

<Dialog {open} title="Unused disk images" subtitle={scanning ? 'Looking through every pool…' : `${disks.length} disks · ${formatBytes(total)} on disk`} width={720} {onclose}>
  {#if error}<div class="ui-error-box">{error}</div>{/if}
  {#if unchecked.length}<div class="ui-error-box">Could not ask {unchecked.join(', ')}: disks on shared pools might be theirs. Check before deleting.</div>{/if}
  {#if !scanning}
    {#if disks.length === 0}
      <p class="muted">Every disk image is used by a machine (or a linked clone).</p>
    {:else}
      <table class="t">
        <thead><tr><th>Image</th><th>Pool</th><th>Size</th><th>On disk</th><th></th></tr></thead>
        <tbody>
          {#each disks as o (o.pool + o.vol.name)}
            <tr>
              <td class="mono">{o.vol.name}{#if o.vol.backing}<div class="muted small">overlay of {o.vol.backing}</div>{/if}</td>
              <td>{o.pool}{o.shared ? ' (shared)' : ''}</td>
              <td class="mono">{formatBytes(o.vol.capacityBytes)}</td>
              <td class="mono">{formatBytes(o.vol.allocationBytes)}</td>
              <td>{#if session.can('admin')}<ConfirmButton label="Delete" confirm="Delete for good?" icon={Trash} run={() => remove(o)} />{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if isos.length}
      <h3>Installation media not in a drive ({isos.length})</h3>
      <p class="muted small">{isos.map((o) => `${o.vol.name} (${formatBytes(o.vol.allocationBytes)})`).join(' · ')}</p>
    {/if}
    <p class="muted small">Snapshots inside qcow2 images and backups are not listed here; backups have their own page.</p>
  {/if}
</Dialog>

<style>
  .t { width: 100%; border-collapse: collapse; font-size: 13px; }
  .t th { text-align: left; font: 600 11px var(--font-mono); text-transform: uppercase; color: var(--text-2); padding: 6px 8px; border-bottom: 1px solid var(--border); }
  .t td { padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
  h3 { font-size: 13px; margin: 14px 0 4px; }
  .muted { color: var(--text-2); }
  .small { font-size: 12px; }
</style>
