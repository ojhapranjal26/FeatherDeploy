import client from './client'

export type DeployType = 'git' | 'artifact' | 'dockerfile'
export type ServiceStatus = 'inactive' | 'deploying' | 'running' | 'error' | 'stopped'

export interface Service {
  id: number
  project_id: number
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

export interface DetectionResult {
  language: string     // "nodejs" | "python" | "php" | "static" | "unknown"
  framework: string    // e.g. "nextjs", "flask", "laravel"
  version: string      // runtime version, e.g. "20", "3.12"
  build_command: string
  start_command: string
  app_port: number
  base_image: string
}

export const servicesApi = {
  list: (projectId: string | number): Promise<Service[]> =>
    client.get<Service[]>(`/projects/${projectId}/services`).then((r) => r.data),

  get: (projectId: string | number, serviceId: string | number): Promise<Service> =>
    client.get<Service>(`/projects/${projectId}/services/${serviceId}`).then((r) => r.data),

  create: (projectId: string | number, data: CreateServicePayload): Promise<Service> =>
    client.post<Service>(`/projects/${projectId}/services`, data).then((r) => r.data),

  update: (projectId: string | number, serviceId: string | number, data: Partial<CreateServicePayload>): Promise<Service> =>
    client.patch<Service>(`/projects/${projectId}/services/${serviceId}`, data).then((r) => r.data),

  delete: (projectId: string | number, serviceId: string | number): Promise<void> =>
    client.delete(`/projects/${projectId}/services/${serviceId}`).then(() => undefined),

  detect: (projectId: string | number, serviceId: string | number): Promise<DetectionResult> =>
    client.post<DetectionResult>(`/projects/${projectId}/services/${serviceId}/detect`).then((r) => r.data),
}
