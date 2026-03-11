import client from './client'

export interface Branding {
  company_name: string
  logo_url: string
}

export const settingsApi = {
  getBranding: async (): Promise<Branding> => {
    const res = await client.get<Branding>('/settings/branding')
    return res.data
  },

  setBranding: async (data: { company_name?: string; logo_url?: string }): Promise<void> => {
    await client.put('/settings/branding', data)
  },
}
