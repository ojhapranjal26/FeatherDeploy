import { delay, uid, services as store, type MockService } from './_mock'

export type DeployType = 'git' | 'artifact' | 'dockerfile'
export type ServiceStatus = 'inactive' | 'deploying' | 'running' | 'error' | 'stopped'

export interface Service {
  id: string
  project_id: string
  name: string
  description: string
  deploy_type: DeployType
  repo_url?: string
  repo_branch: string
  framework?: string
  build_command?: string
  start_command?: string
  app_port: number
  host_port?: number
  status: ServiceStatus
  container_id?: string
  created_at: string
  updated_at: string
}

export interface CreateServicePayload {
  name: string
  description?: string
  deploy_type: DeployType
  repo_url?: string
  repo_branch?: string
  app_port?: number
  build_command?: string
  start_command?: string
}

export const servicesApi = {
  list: async (projectId: string): Promise<Service[]> => {
    await delay()
    return (store[projectId] ?? []) as Service[]
  },

  get: async (projectId: string, serviceId: string): Promise<Service> => {
    await delay()
    const svc = (store[projectId] ?? []).find((s: MockService) => s.id === serviceId)
    if (!svc) throw new Error('Service not found')
    return svc as Service
  },

  create: async (projectId: string, data: CreateServicePayload): Promise<Service> => {
    await delay(500)
    const svc: MockService = {
      id: `svc-${uid()}`,
      project_id: projectId,
      name: data.name,
      description: data.description ?? '',
      deploy_type: data.deploy_type,
      repo_url: data.repo_url,
      repo_branch: data.repo_branch ?? 'main',
      framework: undefined,
      build_command: data.build_command,
      start_command: data.start_command,
      app_port: data.app_port ?? 3000,
      host_port: undefined,
      status: 'inactive',
      container_id: undefined,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    if (!store[projectId]) store[projectId] = []
    store[projectId].push(svc)
    return svc as Service
  },

  update: async (projectId: string, serviceId: string, data: Partial<CreateServicePayload>): Promise<Service> => {
    await delay()
    const svc = (store[projectId] ?? []).find((s: MockService) => s.id === serviceId)
    if (!svc) throw new Error('Service not found')
    Object.assign(svc, data, { updated_at: new Date().toISOString() })
    return svc as Service
  },

  delete: async (projectId: string, serviceId: string): Promise<void> => {
    await delay()
    if (store[projectId]) {
      const i = store[projectId].findIndex((s: MockService) => s.id === serviceId)
      if (i !== -1) store[projectId].splice(i, 1)
    }
  },
}
