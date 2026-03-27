import client from './client'

export type DeploymentStatus = 'pending' | 'running' | 'success' | 'failed'

export interface Deployment {
  id: number
  service_id: number
  triggered_by?: number
  deploy_type: string
  repo_url?: string
  commit_sha?: string
  branch?: string
  artifact_path?: string
  status: DeploymentStatus
  error_message?: string
  started_at?: string
  finished_at?: string
  created_at: string
}

export interface DeploymentListResponse {
  total: number
  deployments: Deployment[]
}

export interface TriggerDeploymentPayload {
  deploy_type: string
  repo_url?: string
  repo_branch?: string
  branch?: string
  commit_sha?: string
}

export const deploymentsApi = {
  list: (
    projectId: string | number,
    serviceId: string | number,
    params?: { limit?: number; offset?: number }
  ): Promise<DeploymentListResponse> =>
    client
      .get<Deployment[]>(`/projects/${projectId}/services/${serviceId}/deployments`, { params })
      .then((r) => ({ total: r.data.length, deployments: r.data })),

  get: (projectId: string | number, serviceId: string | number, deploymentId: string | number): Promise<Deployment> =>
    client
      .get<Deployment>(`/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}`)
      .then((r) => r.data),

  trigger: (
    projectId: string | number,
    serviceId: string | number,
    data: TriggerDeploymentPayload
  ): Promise<{ deployment_id: number; status: string }> =>
    client
      .post<{ deployment_id: number; status: string }>(
        `/projects/${projectId}/services/${serviceId}/deployments`,
        data
      )
      .then((r) => r.data),

  triggerArtifact: (
    projectId: string | number,
    serviceId: string | number,
    _file: File
  ): Promise<{ deployment_id: number; status: string }> =>
    deploymentsApi.trigger(projectId, serviceId, { deploy_type: 'artifact' }),
}
