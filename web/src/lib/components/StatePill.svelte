<script lang="ts">
  import { t } from '../i18n.svelte'
  import type { DomainState } from '../api'

  let { state, progress }: { state: DomainState; progress?: string } = $props()

  const LOOK: Record<DomainState, { label: string; tone: string }> = {
    running: { label: t('Running'), tone: 'ok' },
    blocked: { label: t('Running'), tone: 'ok' }, // blocked on I/O: still "on" to the user
    paused: { label: t('Paused'), tone: 'warn' },
    'shutting-down': { label: t('Shutting down'), tone: 'info' },
    shutoff: { label: t('Off'), tone: 'idle' },
    crashed: { label: t('Crashed'), tone: 'danger' },
    'pm-suspended': { label: t('Suspended'), tone: 'warn' },
    unknown: { label: t('Unknown'), tone: 'idle' },
  }
  let look = $derived(LOOK[state] ?? LOOK.unknown)
</script>

<span class="pill {progress ? 'busy' : look.tone}" title={t('libvirt state: {state}', { state: state })}>
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
    border: var(--frame) solid var(--ink);
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 600;
    color: var(--text);
    background: var(--bg-elev);
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
    width: 9px;
    height: 9px;
    border: 1px solid var(--ink);
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
