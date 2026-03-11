import client from './client'

export type NodeStatus = 'pending' | 'connected' | 'offline' | 'error'

export interface Node {
  id: number
  name: string
  ip: string
  port: number
  status: NodeStatus
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
  port?: number
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

  delete: (id: number) => client.delete(`/nodes/${id}`),

  sshCommand: (id: number) =>
    client.get<{ command: string; key_path: string; note: string }>(`/nodes/${id}/ssh-command`).then((r) => r.data),
}

export const clusterApi = {
  getBrain: () => client.get<ClusterBrain>('/cluster/brain').then((r) => r.data),
}
