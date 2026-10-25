// Kubernetes clusters on VirtoMir machines (ADR-0076). Types and calls for /api/v1/k8s;
// mirrors internal/k8s and internal/api/k8s.go.

import { api } from './api'

const enc = encodeURIComponent

export interface NodeSize {
  vcpus: number
  memoryMiB: number
  diskGiB: number
}

export interface K8sSpec {
  name: string
  project?: string
  version: string
  controlPlanes: number
  workers: number
  controlPlane: NodeSize
  worker: NodeSize
  image: string
  storagePool?: string
  network: string
  ipPool?: string
  vip?: string
  cni: string
  podCidr?: string
  serviceCidr?: string
  sshKeys?: string[]
  hosts?: string[]
}

export interface K8sNode {
  name: string
  role: 'control-plane' | 'worker'
  hostId: string
  uuid?: string
  ip?: string
  state: string
  version?: string
  message?: string
}

export interface K8sEvent {
  at: string
  message: string
  error?: boolean
}

export interface K8sCluster {
  id: string
  spec: K8sSpec
  state: string
  message?: string
  version: string
  upgradeTo?: string
  endpoint?: string
  reservedVip?: string
  ruleId?: string
  nodes: K8sNode[]
  events: K8sEvent[]
  createdBy: string
  created: string
  updated: string
}

export interface K8sList {
  enabled: boolean
  clusters: K8sCluster[]
  versions: string[]
  defaultVersion: string
  cnis: string[]
}

export const k8s = {
  list: () => api.get<K8sList>('/api/v1/k8s/clusters'),
  get: (id: string) => api.get<K8sCluster>(`/api/v1/k8s/clusters/${enc(id)}`),
  create: (s: K8sSpec) => api.post<K8sCluster>('/api/v1/k8s/clusters', s),
  scale: (id: string, workers: number) => api.post<K8sCluster>(`/api/v1/k8s/clusters/${enc(id)}/scale`, { workers }),
  upgrade: (id: string) => api.post<K8sCluster>(`/api/v1/k8s/clusters/${enc(id)}/upgrade`, {}),
  delete: (id: string) => api.delete<void>(`/api/v1/k8s/clusters/${enc(id)}`),
  kubeconfig: (id: string) => api.getText(`/api/v1/k8s/clusters/${enc(id)}/kubeconfig`, 'application/yaml'),
}

/** Something runs on the cluster: the page polls it. */
export const busy = (c: K8sCluster) => ['provisioning', 'scaling', 'upgrading', 'deleting'].includes(c.state)

/** The next minor version after v among versions, or '' for the newest. */
export function nextVersion(v: string, versions: string[]): string {
  const i = versions.indexOf(v)
  return i >= 0 && i < versions.length - 1 ? versions[i + 1] : ''
}

/** Why a new cluster's form cannot be sent yet ('' = it can). */
export function specProblem(s: K8sSpec): string {
  if (!/^[a-z][a-z0-9-]{1,28}[a-z0-9]$/.test(s.name)) return 'name'
  if (!s.image) return 'image'
  if (!s.network) return 'network'
  if (s.controlPlanes === 3 && !s.vip && !s.ipPool) return 'vip'
  if (s.controlPlane.vcpus < 2 || s.controlPlane.memoryMiB < 1800) return 'cp-size'
  if (s.workers < 0 || s.workers > 50) return 'workers'
  return ''
}
