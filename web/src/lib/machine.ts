// Power actions for one machine, shared by the machine list and the machine page.

import Play from '@lucide/svelte/icons/play'
import Power from '@lucide/svelte/icons/power'
import RotateCw from '@lucide/svelte/icons/rotate-cw'
import Pause from '@lucide/svelte/icons/pause'
import Zap from '@lucide/svelte/icons/zap'
import Copy from '@lucide/svelte/icons/copy'
import type { Action, Domain } from './api'
import { isActive, live } from './live.svelte'
import { toasts } from './toast.svelte'
import { session } from './session.svelte'
import type { MenuItem } from './components/ActionMenu.svelte'

export interface Primary {
  action: Action
  icon: typeof Play
  accent?: boolean
}

/** The one obvious next step for the machine's current state. */
export function primaryAction(d: Domain): Primary | null {
  switch (d.state) {
    case 'shutoff':
    case 'crashed':
    case 'unknown':
      return { action: 'start', icon: Play, accent: true }
    case 'paused':
      return { action: 'resume', icon: Play, accent: true }
    case 'running':
    case 'blocked':
      return { action: 'shutdown', icon: Power }
    default:
      return null
  }
}

/** Secondary actions for the "more" menu. */
export function menuItems(d: Domain): MenuItem[] {
  const items: MenuItem[] = []
  const act = (a: Action) => () => live.act(d, a)
  const disabled = session.why('operator') || undefined
  if (d.state === 'running' || d.state === 'blocked') {
    items.push({ label: 'Reboot', icon: RotateCw, run: act('reboot'), disabled })
    items.push({ label: 'Pause', icon: Pause, run: act('pause'), disabled })
  }
  if (isActive(d.state) || d.state === 'paused') {
    items.push({
      label: 'Force off',
      icon: Zap,
      run: act('force-off'),
      danger: { confirm: 'Click again to force off' },
      disabled,
      hint: 'Cuts power immediately (virsh destroy). Unsaved data in the guest is lost.',
    })
  }
  items.push({ label: 'Copy UUID', icon: Copy, run: () => copy(d.uuid, 'UUID') })
  return items
}

export async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push({ kind: 'info', title: `${what} copied`, detail: text.length > 80 ? undefined : text })
  } catch {
    toasts.push({ kind: 'error', title: `Could not copy ${what}`, detail: text.length > 80 ? undefined : text })
  }
}
