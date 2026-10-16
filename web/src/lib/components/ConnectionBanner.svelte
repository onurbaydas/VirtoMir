<script lang="ts">
  import WifiOff from '@lucide/svelte/icons/wifi-off'
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert'
  import { live } from '../live.svelte'
  import { ago } from '../format'

  // Re-render the "x s ago" text every second while a problem is shown.
  let now = $state(Date.now())
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })

  // Only complain after a short grace period: brief blips are not worth a banner.
  let streamDownSince = $state<number | null>(null)
  $effect(() => {
    if (live.streamOpen) streamDownSince = null
    else streamDownSince ??= Date.now()
  })

  let problem = $derived.by(() => {
    if (!live.streamOpen && live.lastUpdate && streamDownSince && now - streamDownSince > 3000) {
      return {
        kind: 'down',
        title: 'Lost connection to VirtoMir',
        detail: `Retrying automatically. Last update ${ago(now - live.lastUpdate)}.`,
      }
    }
    if (live.snapshot && !live.snapshot.connected) {
      return {
        kind: 'degraded',
        title: 'VirtoMir cannot reach libvirt on this host',
        detail: live.snapshot.error,
      }
    }
    return null
  })
</script>

{#if problem}
  <div class="banner {problem.kind}" role="status">
    {#if problem.kind === 'down'}<WifiOff size={17} />{:else}<TriangleAlert size={17} />{/if}
    <div>
      <strong>{problem.title}</strong>
      {#if problem.detail}<span class:mono={problem.kind === 'degraded'}>{problem.detail}</span>{/if}
    </div>
    <span class="spinner" aria-hidden="true"></span>
  </div>
{/if}

<style>
  .banner {
    --c: var(--warn);
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 11px 16px;
    margin-bottom: 20px;
    border: 1px solid color-mix(in srgb, var(--c) 40%, transparent);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--c) 10%, var(--surface));
    color: var(--c);
    animation: slide var(--dur) var(--ease-out);
  }
  .banner.down {
    --c: var(--danger);
  }
  .banner div {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  strong {
    color: var(--text);
    font-weight: 600;
  }
  span {
    color: var(--text-2);
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  .spinner {
    width: 14px;
    height: 14px;
    border: 2px solid currentColor;
    border-right-color: transparent;
    border-radius: var(--radius-sm);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @keyframes slide {
    from {
      opacity: 0;
      transform: translateY(-6px);
    }
  }
</style>
