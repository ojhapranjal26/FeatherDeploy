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
  repo_folder?: string
  framework?: string
  build_command?: string
  start_command?: string
  app_port: number
  host_port?: number
  status: ServiceStatus
  container_id?: string
  auto_deploy: boolean
  created_at: string
  updated_at: string
}

export interface CreateServicePayload {
  name: string
  description?: string
  deploy_type?: DeployType
  repo_url?: string
  repo_branch?: string
  repo_folder?: string
  framework?: string
  app_port?: number
  build_command?: string
  start_command?: string
  domain?: string
}

export interface UpdateServicePayload {
  name?: string
  description?: string
  deploy_type?: DeployType
  repo_url?: string
  repo_branch?: string
  repo_folder?: string
  framework?: string
  build_command?: string
  start_command?: string
  app_port?: number
  host_port?: number
  auto_deploy?: boolean
  clear_repo?: boolean
}

export interface DetectionResult {
  language: string           // "nodejs" | "python" | "php" | "static" | "unknown"
  framework: string          // e.g. "nextjs", "flask", "laravel"
  type: string               // "fullstack" | "backend" | "frontend" | "static" | "ai"
  version: string            // runtime version, e.g. "20", "3.12"
  build_command: string
  pre_build_command: string  // e.g. "npx prisma generate"
  start_command: string
  app_port: number
  base_image: string
  orm: string                // "prisma" | "typeorm" | "mongoose" | "sequelize" | "drizzle"
  build_tool: string         // "npm" | "yarn" | "pnpm" | "bun"
  is_monorepo: boolean
  needs_build: boolean
}

export const servicesApi = {
  list: (projectId: string | number): Promise<Service[]> =>
    client.get<Service[]>(`/projects/${projectId}/services`).then((r) => r.data),

  get: (projectId: string | number, serviceId: string | number): Promise<Service> =>
    client.get<Service>(`/projects/${projectId}/services/${serviceId}`).then((r) => r.data),

  create: (projectId: string | number, data: CreateServicePayload): Promise<Service> =>
    client.post<Service>(`/projects/${projectId}/services`, data).then((r) => r.data),

  update: (projectId: string | number, serviceId: string | number, data: UpdateServicePayload): Promise<Service> =>
    client.patch<Service>(`/projects/${projectId}/services/${serviceId}`, data).then((r) => r.data),

  delete: (projectId: string | number, serviceId: string | number): Promise<void> =>
    client.delete(`/projects/${projectId}/services/${serviceId}`).then(() => undefined),

  detect: (projectId: string | number, serviceId: string | number): Promise<DetectionResult> =>
    client.post<DetectionResult>(`/projects/${projectId}/services/${serviceId}/detect`).then((r) => r.data),

  restart: (projectId: string | number, serviceId: string | number): Promise<{ status: string }> =>
    client.post<{ status: string }>(`/projects/${projectId}/services/${serviceId}/restart`).then((r) => r.data),
}

// ── Historical stats ──────────────────────────────────────────────────────────

export interface StatPoint {
  ts: number       // unix ms
  cpu_pct: number
  mem_pct: number
  mem_used: number
  mem_total: number
  net_in: number
  net_out: number
  blk_in: number
  blk_out: number
  pids: number
}

export interface PeakValue {
  value: number
  ts: number
}

export interface StatsHistoryResponse {
  range: '1h' | '6h' | '24h' | '7d'
  points: StatPoint[]
  peaks: {
    cpu: PeakValue
    mem: PeakValue
    net_in: PeakValue
    net_out: PeakValue
  }
  hourly_avg: {
    hour: number    // 0–23 UTC
    cpu_avg: number
    mem_avg: number
    samples: number
  }[]
}

export type StatsRange = '1h' | '6h' | '24h' | '7d'

export const statsApi = {
  getHistory: (
    projectId: string | number,
    serviceId: string | number,
    range: StatsRange,
  ): Promise<StatsHistoryResponse> =>
    client
      .get<StatsHistoryResponse>(`/projects/${projectId}/services/${serviceId}/stats/history`, {
        params: { range },
      })
      .then((r) => r.data),
}
