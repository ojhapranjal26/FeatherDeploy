import { delay, uid, deployments as store, allServices } from './_mock'

export type DeploymentStatus = 'queued' | 'building' | 'success' | 'failed'

export interface Deployment {
  id: string
  service_id: string
  triggered_by?: string
  deploy_type: string
  repo_url?: string
  commit_sha?: string
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
}

export const deploymentsApi = {
  list: async (
    _projectId: string,
    serviceId: string,
    params?: { limit?: number; offset?: number }
  ): Promise<DeploymentListResponse> => {
    await delay()
    const all = (store[serviceId] ?? []) as Deployment[]
    const offset = params?.offset ?? 0
    const limit = params?.limit ?? all.length
    return { total: all.length, deployments: all.slice(offset, offset + limit) }
  },

  get: async (_projectId: string, serviceId: string, deploymentId: string): Promise<Deployment> => {
    await delay()
    const dep = (store[serviceId] ?? []).find((d) => (d as { id: string }).id === deploymentId)
    if (!dep) throw new Error('Deployment not found')
    return dep as Deployment
  },

  trigger: async (_projectId: string, serviceId: string, data: TriggerDeploymentPayload): Promise<{ deployment_id: string; job_id: string; status: string }> => {
    await delay(600)
    const svc = allServices().find((s) => s.id === serviceId)
    const dep: Deployment = {
      id: `dep-${uid()}`,
      service_id: serviceId,
      triggered_by: 'user-1',
      deploy_type: data.deploy_type,
      repo_url: data.repo_url,
      commit_sha: Math.random().toString(16).slice(2, 9),
      status: 'building',
      started_at: new Date().toISOString(),
      created_at: new Date().toISOString(),
    }
    if (!store[serviceId]) store[serviceId] = []
    store[serviceId].unshift(dep)
    // Simulate completion after a delay
    setTimeout(() => {
      dep.status = 'success'
      dep.finished_at = new Date().toISOString()
      if (svc) Object.assign(svc, { status: 'running', container_id: uid() })
    }, 8000)
    return { deployment_id: dep.id, job_id: `job-${uid()}`, status: 'building' }
  },

  triggerArtifact: async (_projectId: string, serviceId: string, _file: File): Promise<{ deployment_id: string; job_id: string; status: string }> => {
    return deploymentsApi.trigger(_projectId, serviceId, { deploy_type: 'artifact' })
  },
}
