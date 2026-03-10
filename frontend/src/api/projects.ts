import client from './client'

export interface Project {
  id: string
  name: string
  description: string
  service_count?: number
  created_at: string
  updated_at: string
}

export interface CreateProjectPayload {
  name: string
  description?: string
}

export const projectsApi = {
  list: () => client.get<Project[]>('/projects').then((r) => r.data),

  get: (id: string) => client.get<Project>(`/projects/${id}`).then((r) => r.data),

  create: (data: CreateProjectPayload) =>
    client.post<Project>('/projects', data).then((r) => r.data),

  update: (id: string, data: Partial<CreateProjectPayload>) =>
    client.patch<Project>(`/projects/${id}`, data).then((r) => r.data),

  delete: (id: string) => client.delete(`/projects/${id}`),
}
