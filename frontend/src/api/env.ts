import { delay, uid, envVars as store } from './_mock'

export interface EnvVariable {
  id: string
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
  list: async (_projectId: string, serviceId: string): Promise<EnvVariable[]> => {
    await delay()
    return (store[serviceId] ?? []) as EnvVariable[]
  },

  upsert: async (_projectId: string, serviceId: string, data: UpsertEnvPayload): Promise<EnvVariable> => {
    await delay(400)
    if (!store[serviceId]) store[serviceId] = []
    const existing = (store[serviceId] as EnvVariable[]).find((e) => e.key === data.key)
    if (existing) {
      Object.assign(existing, { value: data.value, is_secret: data.is_secret ?? existing.is_secret, updated_at: new Date().toISOString() })
      return existing
    }
    const entry: EnvVariable = {
      id: `env-${uid()}`,
      key: data.key,
      value: data.value,
      is_secret: data.is_secret ?? false,
      updated_at: new Date().toISOString(),
    }
    store[serviceId].push(entry)
    return entry
  },

  bulkUpsert: async (_projectId: string, serviceId: string, data: UpsertEnvPayload[]): Promise<{ upserted: number }> => {
    await delay(500)
    for (const item of data) {
      await envApi.upsert(_projectId, serviceId, item)
    }
    return { upserted: data.length }
  },

  delete: async (_projectId: string, serviceId: string, envId: string): Promise<void> => {
    await delay()
    if (store[serviceId]) {
      const i = (store[serviceId] as EnvVariable[]).findIndex((e) => e.id === envId)
      if (i !== -1) store[serviceId].splice(i, 1)
    }
  },
}
