<script lang="ts">
  import type { DomainState } from '../api'

  let { state, progress }: { state: DomainState; progress?: string } = $props()

  const LOOK: Record<DomainState, { label: string; tone: string }> = {
    running: { label: 'Running', tone: 'ok' },
    blocked: { label: 'Running', tone: 'ok' }, // blocked on I/O: still "on" to the user
    paused: { label: 'Paused', tone: 'warn' },
    'shutting-down': { label: 'Shutting down', tone: 'info' },
    shutoff: { label: 'Off', tone: 'idle' },
    crashed: { label: 'Crashed', tone: 'danger' },
    'pm-suspended': { label: 'Suspended', tone: 'warn' },
    unknown: { label: 'Unknown', tone: 'idle' },
  }
  let look = $derived(LOOK[state] ?? LOOK.unknown)
</script>

<span class="pill {progress ? 'busy' : look.tone}" title="libvirt state: {state}">
  <span class="dot"></span>
  {progress ?? look.label}
</span>

<style>
  .pill {
    --c: var(--idle);
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 24px;
    padding: 0 9px 0 7px;
    border: 1px solid var(--border-strong);
    font-family: var(--font-display);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--c);
    background: var(--bg-elev);
    box-shadow: var(--bevel-in);
    white-space: nowrap;
    transition:
      color var(--dur) var(--ease-out),
      background var(--dur) var(--ease-out);
  }
  .ok {
    --c: var(--ok);
  }
  .warn {
    --c: var(--warn);
  }
  .info,
  .busy {
    --c: var(--info);
  }
  .danger {
    --c: var(--danger);
  }
  .idle {
    --c: var(--text-3);
  }
  .dot {
    width: 8px;
    height: 8px;
    background: var(--c);
    outline: 1px solid var(--ink);
  }
  .ok .dot {
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--ok) 60%, transparent);
    animation: pulse 2.4s infinite;
  }
  .busy .dot {
    background: transparent;
    border: 1.5px solid var(--c);
    border-right-color: transparent;
    width: 9px;
    height: 9px;
    animation: spin 0.8s linear infinite;
  }
  @keyframes pulse {
    0% {
      box-shadow: 0 0 0 0 color-mix(in srgb, var(--ok) 55%, transparent);
    }
    70% {
      box-shadow: 0 0 0 6px transparent;
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
