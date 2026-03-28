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

export const authApi = {
  login: async (data: LoginPayload): Promise<AuthResponse> => {
    const res = await client.post<AuthResponse>('/auth/login', data)
    localStorage.setItem('token', res.data.token)
    return res.data
  },

  logout: async (): Promise<void> => {
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

