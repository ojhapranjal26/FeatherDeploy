import client from './client'

export interface Branding {
  company_name: string
  logo_url: string
}

export interface SMTPStatus {
  configured: boolean
  host: string
  port: string
  from: string
  tls: string
  username_set: boolean
  password_set: boolean
}

export interface GitHubOAuthStatus {
  configured: boolean
  client_id: string
  client_secret_set: boolean
}

export const settingsApi = {
  getBranding: async (): Promise<Branding> => {
    const res = await client.get<Branding>('/settings/branding')
    return res.data
  },

  setBranding: async (data: { company_name?: string; logo_url?: string }): Promise<void> => {
    await client.put('/settings/branding', data)
  },

  // ── SMTP ────────────────────────────────────────────────────────────────

  getSMTPStatus: async (): Promise<SMTPStatus> => {
    const res = await client.get<SMTPStatus>('/settings/smtp')
    return res.data
  },

  setSMTP: async (data: {
    host?: string; port?: string; user?: string
    pass?: string; from?: string; tls?: string
  }): Promise<SMTPStatus> => {
    const res = await client.post<SMTPStatus>('/settings/smtp', data)
    return res.data
  },

  deleteSMTP: async (): Promise<void> => {
    await client.delete('/settings/smtp')
  },

  // ── GitHub OAuth ─────────────────────────────────────────────────────────

  getGitHubOAuthStatus: async (): Promise<GitHubOAuthStatus> => {
    const res = await client.get<GitHubOAuthStatus>('/settings/github-oauth')
    return res.data
  },

  setGitHubOAuth: async (data: { client_id?: string; client_secret?: string }): Promise<GitHubOAuthStatus> => {
    const res = await client.post<GitHubOAuthStatus>('/settings/github-oauth', data)
    return res.data
  },

  deleteGitHubOAuth: async (): Promise<void> => {
    await client.delete('/settings/github-oauth')
  },
}
