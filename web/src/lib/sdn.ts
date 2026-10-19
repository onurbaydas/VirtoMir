// SDN on OVN (ADR-0072): types and calls for /api/v1/sdn. Mirrors internal/sdn and
// internal/api/sdn.go.

import { api } from './api'
import { t } from './i18n.svelte'

const enc = encodeURIComponent

export interface VPC {
  id: string
  name: string
  description?: string
  projectId: string
  ipv4Cidr: string
  defaultDeny: boolean
  created?: string
  updated?: string
}

export interface Route {
  destinationCidr: string
  nextHopIp: string
}

export interface Router {
  id: string
  vpcId: string
  name: string
  logicalRouter: string
  routes: Route[]
}

export interface ExternalGateway {
  vpcId?: string
  externalNetwork: string
  gatewayIp: string
  externalIp: string
  enabled: boolean
  gatewayChassis: string
}

export interface VPCDetail extends VPC {
  router: Router
  gateway?: ExternalGateway
}

export interface Subnet {
  id: string
  vpcId: string
  name: string
  cidr: string
  gatewayIp?: string
  dhcpEnabled: boolean
  allocationPoolStart?: string
  allocationPoolEnd?: string
  dnsServers?: string[]
  ovnDhcpId?: string
  logicalSwitch?: string
}

export interface SDNPort {
  id: string
  subnetId: string
  vmUuid: string
  mac: string
  ip?: string
  securityGroups?: string[]
}

export interface FloatingIP {
  id: string
  projectId?: string
  vpcId: string
  externalIp: string
  internalIp?: string
  portId?: string
  description?: string
  created?: string
}

export type RuleDirection = 'ingress' | 'egress'
export type RuleAction = 'allow' | 'allow-related' | 'drop' | 'reject'
export type RuleProtocol = 'tcp' | 'udp' | 'icmp' | 'any'

export interface SecurityRule {
  id?: string
  direction: RuleDirection
  action: RuleAction
  priority: number
  protocol: RuleProtocol
  portMin?: number
  portMax?: number
  remoteCidr?: string
  remoteGroupId?: string
  description?: string
}

export interface SecurityGroup {
  id: string
  projectId?: string
  vpcId: string
  name: string
  description?: string
  rules: SecurityRule[]
  defaultDeny: boolean
  memberPorts: string[]
  created?: string
  updated?: string
}

export interface HealthCheckSpec {
  protocol: 'tcp' | 'udp'
  port: number
  intervalSeconds: number
  timeoutSeconds: number
  fall: number
  rise: number
  sourceIp?: string
}

export interface LoadBalancer {
  id: string
  projectId?: string
  vpcId: string
  name: string
  description?: string
  vip: string
  port: number
  protocol: 'tcp' | 'udp'
  members: string[]
  attachedSwitches?: string[]
  attachedRouters?: string[]
  healthCheck?: HealthCheckSpec
  status?: string
}

export interface IPsecConfig {
  enabled: boolean
  mode: 'cert'
  certFile?: string
  keyFile?: string
  caCertFile?: string
  status: 'disabled' | 'active' | 'error'
  lastError?: string
}

export interface HostStatus {
  installed: boolean
  ovsVersion?: string
  ovnVersion?: string
  serviceOvs: boolean
  serviceOvnCentral: boolean
  serviceOvnHost: boolean
  integrationBridge: string
  systemId?: string
  encapsulationType?: string
  encapsulationIp?: string
  role: 'central' | 'controller'
  centralEndpoint?: string
}

export interface ChassisInfo {
  id: string
  hostname: string
  encapsulationIp: string
  encapsulationType: string
}

export interface SDNStatus {
  host: HostStatus
  chassis: ChassisInfo[]
  chassisError?: string
}

export interface ReconcileResult {
  applied: number
  errors: string[]
}

const q = (params: Record<string, string | undefined>) => {
  const s = new URLSearchParams(Object.entries(params).filter((e): e is [string, string] => !!e[1])).toString()
  return s ? '?' + s : ''
}

export const sdn = {
  status: () => api.get<SDNStatus>('/api/v1/sdn/status'),
  reconcile: () => api.post<ReconcileResult>('/api/v1/sdn/reconcile', {}),
  vpcs: async (project?: string) => (await api.get<{ vpcs: VPC[] }>('/api/v1/sdn/vpcs' + q({ project }))).vpcs,
  vpc: (id: string) => api.get<VPCDetail>(`/api/v1/sdn/vpcs/${enc(id)}`),
  createVPC: (v: Partial<VPC>) => api.post<VPCDetail>('/api/v1/sdn/vpcs', v),
  updateVPC: (id: string, v: Partial<VPC>) => api.put<VPCDetail>(`/api/v1/sdn/vpcs/${enc(id)}`, v),
  deleteVPC: (id: string) => api.delete<void>(`/api/v1/sdn/vpcs/${enc(id)}`),
  setRoutes: (id: string, routes: Route[]) => api.put<Router>(`/api/v1/sdn/vpcs/${enc(id)}/routes`, { routes }),
  setGateway: (id: string, gw: ExternalGateway) => api.put<ExternalGateway>(`/api/v1/sdn/vpcs/${enc(id)}/gateway`, gw),
  subnets: async (vpc?: string) => (await api.get<{ subnets: Subnet[] }>('/api/v1/sdn/subnets' + q({ vpc }))).subnets,
  createSubnet: (s: Partial<Subnet>) => api.post<Subnet>('/api/v1/sdn/subnets', s),
  updateSubnet: (id: string, s: Partial<Subnet>) => api.put<Subnet>(`/api/v1/sdn/subnets/${enc(id)}`, s),
  deleteSubnet: (id: string) => api.delete<void>(`/api/v1/sdn/subnets/${enc(id)}`),
  ports: async (filter: { vm?: string; subnet?: string } = {}) => (await api.get<{ ports: SDNPort[] }>('/api/v1/sdn/ports' + q(filter))).ports,
  floatingIPs: async (vpc?: string) => (await api.get<{ floatingIps: FloatingIP[] }>('/api/v1/sdn/floating-ips' + q({ vpc }))).floatingIps,
  createFloatingIP: (f: Partial<FloatingIP>) => api.post<FloatingIP>('/api/v1/sdn/floating-ips', f),
  deleteFloatingIP: (id: string) => api.delete<void>(`/api/v1/sdn/floating-ips/${enc(id)}`),
  assignFloatingIP: (id: string, internalIp: string, portId?: string) => api.post<FloatingIP>(`/api/v1/sdn/floating-ips/${enc(id)}/assign`, { internalIp, portId }),
  unassignFloatingIP: (id: string) => api.post<FloatingIP>(`/api/v1/sdn/floating-ips/${enc(id)}/unassign`, {}),
  securityGroups: async (vpc?: string) => (await api.get<{ securityGroups: SecurityGroup[] }>('/api/v1/sdn/security-groups' + q({ vpc }))).securityGroups,
  createSecurityGroup: (g: Partial<SecurityGroup>) => api.post<SecurityGroup>('/api/v1/sdn/security-groups', g),
  updateSecurityGroup: (id: string, g: Partial<SecurityGroup>) => api.put<SecurityGroup>(`/api/v1/sdn/security-groups/${enc(id)}`, g),
  deleteSecurityGroup: (id: string) => api.delete<void>(`/api/v1/sdn/security-groups/${enc(id)}`),
  addRule: (id: string, r: SecurityRule) => api.post<SecurityRule>(`/api/v1/sdn/security-groups/${enc(id)}/rules`, r),
  deleteRule: (id: string, rule: string) => api.delete<void>(`/api/v1/sdn/security-groups/${enc(id)}/rules/${enc(rule)}`),
  loadBalancers: async (vpc?: string) => (await api.get<{ loadBalancers: LoadBalancer[] }>('/api/v1/sdn/load-balancers' + q({ vpc }))).loadBalancers,
  createLoadBalancer: (lb: Partial<LoadBalancer>) => api.post<LoadBalancer>('/api/v1/sdn/load-balancers', lb),
  updateLoadBalancer: (id: string, lb: Partial<LoadBalancer>) => api.put<LoadBalancer>(`/api/v1/sdn/load-balancers/${enc(id)}`, lb),
  deleteLoadBalancer: (id: string) => api.delete<void>(`/api/v1/sdn/load-balancers/${enc(id)}`),
  ipsec: () => api.get<IPsecConfig>('/api/v1/sdn/ipsec'),
  setIPsec: (c: Partial<IPsecConfig>) => api.put<IPsecConfig>('/api/v1/sdn/ipsec', c),
}

// ---- Helpers ------------------------------------------------------------------------------------

const ipv4 = (s: string): number | null => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s.trim())
  if (!m) return null
  const o = m.slice(1).map(Number)
  if (o.some((x) => x > 255) || m.slice(1).some((x) => x.length > 1 && x.startsWith('0'))) return null
  return ((o[0] << 24) | (o[1] << 16) | (o[2] << 8) | o[3]) >>> 0
}

/** Whether s is an IPv4 address. */
export const isIPv4 = (s: string) => ipv4(s) !== null

/** Whether s is an IPv4 network in canonical form (host bits zero), at most maxBits long. */
export function isCIDR(s: string, maxBits = 32): boolean {
  const [addr, bits, extra] = s.trim().split('/')
  const a = ipv4(addr ?? '')
  const b = Number(bits)
  if (a === null || extra !== undefined || !/^\d{1,2}$/.test(bits ?? '') || b > maxBits) return false
  const mask = b === 0 ? 0 : (0xffffffff << (32 - b)) >>> 0
  return (a & mask) >>> 0 === a
}

/** Whether network inner lies inside network outer (both canonical CIDRs). */
export function cidrContains(outer: string, inner: string): boolean {
  if (!isCIDR(outer) || !isCIDR(inner)) return false
  const [oa, ob] = outer.split('/')
  const [ia, ib] = inner.split('/')
  if (Number(ib) < Number(ob)) return false
  const mask = Number(ob) === 0 ? 0 : (0xffffffff << (32 - Number(ob))) >>> 0
  return ((ipv4(ia)! & mask) >>> 0) === ipv4(oa)
}

/** Number of usable host addresses of a CIDR (network and broadcast excluded). */
export function hostCount(cidr: string): number {
  if (!isCIDR(cidr)) return 0
  const bits = Number(cidr.split('/')[1])
  return Math.max(0, 2 ** (32 - bits) - 2)
}

/** What a rule matches, in short: "tcp 80–443 from 0.0.0.0/0". */
export function ruleText(r: SecurityRule, groupName: (id: string) => string = (id) => id): string {
  const proto = r.protocol === 'any' ? 'any' : r.protocol
  let ports = ''
  if ((r.protocol === 'tcp' || r.protocol === 'udp') && r.portMin) {
    ports = r.portMax && r.portMax !== r.portMin ? ` ${r.portMin}–${r.portMax}` : ` ${r.portMin}`
  }
  const peer = r.remoteGroupId ? t('group {name}', { name: groupName(r.remoteGroupId) }) : r.remoteCidr || t('anywhere')
  const what = proto + ports
  return r.direction === 'ingress' ? t('{what} from {peer}', { what, peer }) : t('{what} to {peer}', { what, peer })
}
