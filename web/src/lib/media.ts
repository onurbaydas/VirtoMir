// Pools, ISO images and disk volumes across all active pools, for pickers in dialogs.

import { api, type Pool, type Volume } from './api'

export interface PoolVolume {
  pool: string
  vol: Volume
}

export async function activePools(): Promise<Pool[]> {
  return (await api.pools()).filter((p) => p.active)
}

/** Every volume in every active pool (unreadable pools are skipped). */
export async function allVolumes(pools?: Pool[]): Promise<PoolVolume[]> {
  const ps = pools ?? (await activePools())
  const lists = await Promise.all(ps.map(async (p) => ({ p: p.name, v: await api.volumes(p.name).catch(() => [] as Volume[]) })))
  return lists.flatMap(({ p, v }) => v.map((vol) => ({ pool: p, vol })))
}

export const isISO = (v: Volume) => v.format === 'iso'
