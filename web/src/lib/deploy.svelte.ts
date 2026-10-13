// "New machine from template" can start anywhere (list row, machine page, wizard); one dialog
// mounted in App serves them all.

import type { Domain } from './api'

export const deploy = $state<{ template: Pick<Domain, 'uuid' | 'name'> | null }>({ template: null })

export function openDeploy(t: Pick<Domain, 'uuid' | 'name'>) {
  deploy.template = { uuid: t.uuid, name: t.name }
}
