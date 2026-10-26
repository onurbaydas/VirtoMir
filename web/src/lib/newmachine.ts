// The new-machine wizard's rules, kept out of the component so they can be tested.
import { t } from './i18n.svelte'

export const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/

export interface CreateInput {
  kind: 'vm' | 'container'
  name: string
  takenNames: string[]
  vcpus: number
  memGiB: number
  source: 'iso' | 'cloud' | 'disk'
  withDisk: boolean
  iso: string
  baseImage: string
  existingDisk: string
  ciUser: string
  ciPassword: string
  ciKeys: string[]
  containersAvailable: boolean
  rootPassword: string
}

/** What is wrong with the cloud-init settings, or '' (only for cloud images). */
export function cloudInitProblem(i: Pick<CreateInput, 'source' | 'ciUser' | 'ciPassword' | 'ciKeys'>): string {
  if (i.source !== 'cloud') return ''
  if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(i.ciUser)) return t('User name: lowercase letters, digits, "_" or "-".')
  if (!i.ciPassword && i.ciKeys.length === 0) return t('Set a password or an SSH key to be able to sign in.')
  return ''
}

/** The first thing that keeps "Create" disabled, in plain words; '' when it can be created. */
export function createBlocker(i: CreateInput): string {
  if (!i.name.trim()) return t('Give the machine a name.')
  if (!NAME_RE.test(i.name)) return t('The name may only use letters, digits, ".", "_", "+" and "-".')
  if (i.takenNames.includes(i.name)) return t('A machine with this name already exists.')
  if (i.vcpus < 1 || !(i.memGiB > 0)) return t('Give it at least one vCPU and some memory.')
  if (i.kind === 'container') {
    if (!i.containersAvailable) return t('System containers are not available on this host.')
    if (i.rootPassword.length < 8 || /[:\u0000-\u001f]/.test(i.rootPassword)) return t('Set a root password of at least 8 characters.')
    return ''
  }
  const bootable = i.source === 'iso' ? i.withDisk || !!i.iso : i.source === 'cloud' ? !!i.baseImage : !!i.existingDisk
  if (!bootable) {
    if (i.source === 'iso') return t('Add a disk or installation media.')
    return i.source === 'cloud' ? t('Choose a cloud image.') : t('Choose the disk image to boot.')
  }
  return cloudInitProblem(i)
}
