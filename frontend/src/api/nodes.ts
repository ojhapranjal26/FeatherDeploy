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
}
