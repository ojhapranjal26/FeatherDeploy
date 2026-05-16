import client from './client'

export type NodeStatus = 'pending' | 'connected' | 'offline' | 'error'

export interface Node {
  id: number
  name: string
  ip: string
  port: number
  status: NodeStatus
  hostname: string
  os_info: string
  rqlite_addr: string
  last_seen: string | null
  created_at: string
  // Resource stats (populated by node heartbeat every 10s)
  cpu_usage: number | null
  ram_used: number | null
  ram_total: number | null
  disk_used: number | null
  disk_total: number | null
  last_stats_at: string | null
  node_id: string | null
  node_type: string
  assigned_domains: string[]
  is_brain: boolean
  wg_public_key?: string
  wg_mesh_ip?: string
}

export interface ClusterBrain {
  BrainID: string
  BrainAddr: string
  LastHeartbeat: string
  Alive: boolean
  CPU: number
  RAMUsed: number
  RAMTotal: number
  DiskUsed: number
  DiskTotal: number
}

export interface AddNodePayload {
  name: string
  ip: string
  node_type: 'worker' | 'brain'
}

export interface AddNodeResponse extends Node {
  join_token: string
  token_expires_at: string
  join_command: string
}

export const nodesApi = {
  list: () => client.get<Node[]>('/nodes').then((r) => r.data),

  add: (data: AddNodePayload) =>
    client.post<AddNodeResponse>('/nodes', data).then((r) => r.data),

  updateDomains: (id: number, domains: string[]) =>
    client.patch(`/nodes/${id}/domains`, { domains }).then((r) => r.data),

  delete: (id: number) => client.delete(`/nodes/${id}`),

  regenerateToken: (id: number) =>
    client.post<AddNodeResponse>(`/nodes/${id}/token`).then((r) => r.data),

  sshCommand: (id: number) =>
    client.get<{ command: string; key_path: string; note: string }>(`/nodes/${id}/ssh-command`).then((r) => r.data),

  rotateWireguard: (id: number) =>
    client.post<{ status: string; wg_public_key: string; message: string }>(`/nodes/${id}/rotate-wireguard`).then((r) => r.data),
}

export const clusterApi = {
  getBrain: () => client.get<ClusterBrain>('/cluster/brain').then((r) => r.data),
}
