import client from './client'

export interface Project {
  id: string
  name: string
  description: string
  owner_id: number
  service_count?: number
  created_at: string
  updated_at: string
  my_role?: 'owner' | 'editor' | 'viewer'
}

export interface CreateProjectPayload {
  name: string
  description?: string
}

export interface ProjectMember {
  user_id: number
  email: string
  name: string
  role: 'owner' | 'editor' | 'viewer'
}

export interface UserLookup {
  id: number
  email: string
  name: string
}

export const projectsApi = {
  list: () => client.get<Project[]>('/projects').then((r) => r.data),

  get: (id: string) => client.get<Project>(`/projects/${id}`).then((r) => r.data),

  create: (data: CreateProjectPayload) =>
    client.post<Project>('/projects', data).then((r) => r.data),

  update: (id: string, data: Partial<CreateProjectPayload>) =>
    client.patch<Project>(`/projects/${id}`, data).then((r) => r.data),

  delete: (id: string) => client.delete(`/projects/${id}`),

  listMembers: (projectId: string) =>
    client.get<ProjectMember[]>(`/projects/${projectId}/members`).then((r) => r.data),

  addMember: (projectId: string, userId: number, role: 'owner' | 'editor' | 'viewer') =>
    client.post(`/projects/${projectId}/members`, { user_id: userId, role }).then((r) => r.data),

  updateMember: (projectId: string, userId: number, role: 'owner' | 'editor' | 'viewer') =>
    client.patch(`/projects/${projectId}/members/${userId}`, { user_id: userId, role }).then((r) => r.data),

  removeMember: (projectId: string, userId: number) =>
    client.delete(`/projects/${projectId}/members/${userId}`).then(() => undefined),
}

export const usersApi = {
  lookup: (email: string) =>
    client.get<UserLookup>(`/users/lookup`, { params: { email } }).then((r) => r.data),
}
