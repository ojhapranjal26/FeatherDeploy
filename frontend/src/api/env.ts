import client from './client'

export interface EnvVariable {
  id: number
  service_id?: number
  key: string
  value: string
  is_secret: boolean
  updated_at: string
}

export interface UpsertEnvPayload {
  key: string
  value: string
  is_secret?: boolean
}

export const envApi = {
  list: (projectId: string | number, serviceId: string | number): Promise<EnvVariable[]> =>
    client.get<EnvVariable[]>(`/projects/${projectId}/services/${serviceId}/env`).then((r) => r.data),

  upsert: (projectId: string | number, serviceId: string | number, data: UpsertEnvPayload): Promise<void> =>
    client.put(`/projects/${projectId}/services/${serviceId}/env`, data).then(() => undefined),

  bulkUpsert: async (
    projectId: string | number,
    serviceId: string | number,
    data: UpsertEnvPayload[]
  ): Promise<{ upserted: number }> => {
    await Promise.all(data.map((item) => envApi.upsert(projectId, serviceId, item)))
    return { upserted: data.length }
  },

  // The backend deletes by key, not by ID
  delete: (projectId: string | number, serviceId: string | number, key: string): Promise<void> =>
    client.delete(`/projects/${projectId}/services/${serviceId}/env/${encodeURIComponent(key)}`).then(() => undefined),
}
