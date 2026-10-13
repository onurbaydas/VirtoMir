<script lang="ts" module>
  import HardDrive from '@lucide/svelte/icons/hard-drive'
  import Disc from '@lucide/svelte/icons/disc'
  import Save from '@lucide/svelte/icons/save'
  import FolderSymlink from '@lucide/svelte/icons/folder-symlink'
  import EthernetPort from '@lucide/svelte/icons/ethernet-port'
  import Monitor from '@lucide/svelte/icons/monitor'
  import Gpu from '@lucide/svelte/icons/gpu'
  import Terminal from '@lucide/svelte/icons/terminal'
  import Bot from '@lucide/svelte/icons/bot'
  import Cable from '@lucide/svelte/icons/cable'
  import Usb from '@lucide/svelte/icons/usb'
  import CircuitBoard from '@lucide/svelte/icons/circuit-board'
  import MousePointer2 from '@lucide/svelte/icons/mouse-pointer-2'
  import Keyboard from '@lucide/svelte/icons/keyboard'
  import Volume2 from '@lucide/svelte/icons/volume-2'
  import ShieldCheck from '@lucide/svelte/icons/shield-check'
  import Dices from '@lucide/svelte/icons/dices'
  import MemoryStick from '@lucide/svelte/icons/memory-stick'
  import Timer from '@lucide/svelte/icons/timer'
  import Siren from '@lucide/svelte/icons/siren'
  import Cog from '@lucide/svelte/icons/cog'
  import type { Device } from '../api'

  function iconFor(d: Device) {
    switch (d.kind) {
      case 'disk':
      case 'lun':
        return HardDrive
      case 'cdrom':
        return Disc
      case 'floppy':
        return Save
      case 'filesystem':
        return FolderSymlink
      case 'interface':
        return EthernetPort
      case 'graphics':
        return Monitor
      case 'video':
        return Gpu
      case 'console':
      case 'serial':
      case 'parallel':
        return Terminal
      case 'channel':
        return d.title.includes('agent') ? Bot : Cable
      case 'hostdev-usb':
      case 'redirdev':
      case 'hub':
        return Usb
      case 'hostdev-pci':
      case 'hostdev-mdev':
      case 'hostdev':
        return CircuitBoard
      case 'input':
        return d.title === 'Keyboard' ? Keyboard : MousePointer2
      case 'sound':
        return Volume2
      case 'tpm':
        return ShieldCheck
      case 'rng':
        return Dices
      case 'memballoon':
      case 'memory':
        return MemoryStick
      case 'watchdog':
        return Timer
      case 'panic':
        return Siren
      default:
        return Cog
    }
  }
</script>

<script lang="ts">
  import ChevronDown from '@lucide/svelte/icons/chevron-down'
  import { formatBytes } from '../format'
  import PropList from './PropList.svelte'

  import type { Snippet } from 'svelte'

  let { device: d, actions }: { device: Device; actions?: Snippet } = $props()
  let open = $state(false)

  let Icon = $derived(iconFor(d))
  let usedPct = $derived(
    d.capacityBytes && d.allocationBytes !== undefined ? Math.min(100, (d.allocationBytes / d.capacityBytes) * 100) : null,
  )
  let id = $props.id()
</script>

<article class="device" class:open>
  <button class="head" aria-expanded={open} aria-controls="{id}-body" onclick={() => (open = !open)}>
    <span class="icon"><Icon size={16} /></span>
    <span class="titles">
      <span class="title">
        {d.title}
        {#if d.bootOrder}<span class="badge" title="Boot order (boot@order)">boot #{d.bootOrder}</span>{/if}
      </span>
      {#if d.summary}<span class="summary">{d.summary}</span>{/if}
    </span>
    {#if d.capacityBytes}
      <span class="size num" title="Allocated on the host / virtual size">
        {d.allocationBytes ? `${formatBytes(d.allocationBytes)} / ` : ''}{formatBytes(d.capacityBytes)}
      </span>
    {/if}
    <span class="chev"><ChevronDown size={16} /></span>
  </button>

  {#if usedPct !== null}
    <div class="bar" aria-hidden="true"><span style:width="{usedPct}%"></span></div>
  {/if}

  {#if d.addresses?.length}
    <div class="addrs">
      {#each d.addresses as a (a)}<span class="addr mono">{a}</span>{/each}
    </div>
  {/if}

  {#if open}
    <div class="body" id="{id}-body">
      {#if d.props.length}
        <PropList props={d.props} />
      {:else}
        <p class="none">No further settings. The XML tab shows the full definition.</p>
      {/if}
      {#if actions}<div class="actions">{@render actions()}</div>{/if}
    </div>
  {/if}
</article>

<style>
  .device {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
    transition: border-color var(--dur-fast);
  }
  .device:hover,
  .device.open {
    border-color: var(--border-strong);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 12px 14px;
    border: 0;
    background: transparent;
    text-align: left;
    cursor: pointer;
  }
  .icon {
    flex: none;
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .titles {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 13.5px;
  }
  .badge {
    padding: 1px 7px;
    border-radius: 999px;
    background: var(--accent-soft);
    color: var(--accent);
    font-size: 11px;
    font-weight: 600;
  }
  .summary {
    color: var(--text-3);
    font-size: 12.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .size {
    flex: none;
    color: var(--text-2);
    font-size: 12.5px;
  }
  .chev {
    flex: none;
    display: grid;
    color: var(--text-3);
    transition: transform var(--dur) var(--ease-out);
  }
  .open .chev {
    transform: rotate(180deg);
  }
  .bar {
    height: 3px;
    margin: -4px 14px 10px 58px;
    border-radius: 2px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .addrs {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 0 14px 12px 58px;
  }
  .addr {
    padding: 2px 8px;
    border-radius: 6px;
    background: var(--surface-2);
    color: var(--text);
    font-size: 12px;
  }
  .body {
    padding: 4px 14px 10px 58px;
    border-top: 1px solid var(--border);
    animation: reveal var(--dur) var(--ease-out);
  }
  .actions {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    padding-top: 10px;
    margin-top: 6px;
    border-top: 1px solid var(--border);
  }
  .none {
    margin: 8px 0 4px;
    color: var(--text-3);
    font-size: 13px;
  }
  @keyframes reveal {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
  @media (max-width: 520px) {
    .body,
    .addrs {
      padding-left: 14px;
    }
    .bar {
      margin-left: 14px;
    }
  }
</style>
