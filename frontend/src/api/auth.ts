import client from './client'

export interface LoginPayload {
  email: string
  password: string
}

export interface User {
  id: number
  email: string
  name: string
  role: 'user' | 'admin' | 'superadmin'
  github_login?: string
  created_at: string
  updated_at?: string
}

export interface AuthResponse {
  token: string
  user: User
}

// ── QR login types ────────────────────────────────────────────────────────────

export interface QRInitResponse {
  qr_token: string
  expires_at: number   // unix ms — QR code validity (~5 min)
}

export interface QRPollResponse {
  status: 'pending' | 'approved' | 'expired'
  token?: string       // present when status === 'approved'
  user?: User          // present when status === 'approved'
}

// ── Session / device types ────────────────────────────────────────────────────

export interface Session {
  id: string
  user_agent: string
  ip_address: string
  created_at: string
  last_seen: string
  is_current: boolean
}

export const authApi = {
  login: async (data: LoginPayload): Promise<AuthResponse> => {
    const res = await client.post<AuthResponse>('/auth/login', data)
    localStorage.setItem('token', res.data.token)
    return res.data
  },

  logout: async (): Promise<void> => {
    try {
      await client.post('/auth/logout')
    } catch {
      // best-effort — always clear the local token
    }
    localStorage.removeItem('token')
  },

  me: (): Promise<User> =>
    client.get<User>('/auth/me').then((r) => r.data),
}

export const qrApi = {
  /** POST /api/auth/qr/init — public, login page calls this to start a QR session */
  init: (): Promise<QRInitResponse> =>
    client.post<QRInitResponse>('/auth/qr/init').then((r) => r.data),

  /** GET /api/auth/qr/{token}/poll — public, login page polls until approved */
  poll: (token: string): Promise<QRPollResponse> =>
    client.get<QRPollResponse>(`/auth/qr/${token}/poll`).then((r) => r.data),

  /** POST /api/auth/qr/{token}/approve — authenticated, approving device calls this */
  approve: (token: string): Promise<{ status: string }> =>
    client.post<{ status: string }>(`/auth/qr/${token}/approve`).then((r) => r.data),
}

export const sessionsApi = {
  /** GET /api/auth/sessions — list active sessions for the current user */
  list: (): Promise<Session[]> =>
    client.get<Session[]>('/auth/sessions').then((r) => r.data),

  /** DELETE /api/auth/sessions/{id} — revoke a specific session */
  revoke: (id: string): Promise<void> =>
    client.delete(`/auth/sessions/${id}`).then(() => undefined),

  /** DELETE /api/auth/sessions/others — revoke all sessions except current */
  revokeOthers: (): Promise<void> =>
    client.delete('/auth/sessions/others').then(() => undefined),
}

