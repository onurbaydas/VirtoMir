// Power actions for one machine, shared by the machine list and the machine page.

import Play from '@lucide/svelte/icons/play'
import Power from '@lucide/svelte/icons/power'
import Copy from '@lucide/svelte/icons/copy'
import type { Action, Domain } from './api'
import { toasts } from './toast.svelte'
import { t } from './i18n.svelte'
import type { MenuItem } from './components/ActionMenu.svelte'

export interface Primary {
  action: Action
  icon: typeof Play
  accent?: boolean
}

/** The one obvious next step for the machine's current state. */
export function primaryAction(d: Domain): Primary | null {
  if (d.template) return null // its button is "New machine"
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

/** Secondary actions for the "more" menu. Power actions live in the power button's own menu only. */
export function menuItems(d: Domain): MenuItem[] {
  return [{ label: t('Copy UUID'), icon: Copy, run: () => copy(d.uuid, 'UUID') }]
}

export async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toasts.push({ kind: 'info', title: t('{what} copied', { what }), detail: text.length > 80 ? undefined : text })
  } catch {
    toasts.push({ kind: 'error', title: t('Could not copy {what}', { what }), detail: text.length > 80 ? undefined : text })
  }
}
