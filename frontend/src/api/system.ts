import client from './client'

export interface VersionInfo {
  current_version: string
  latest_version: string
  update_available: boolean
  changelog: string
  branch: string
}

export const systemApi = {
  checkVersion: async (): Promise<VersionInfo> => {
    const res = await client.get<VersionInfo>('/system/version')
    return res.data
  },

  triggerUpdate: async (): Promise<{ message: string; version: string }> => {
    const res = await client.post<{ message: string; version: string }>('/system/update')
    return res.data
  },
}
