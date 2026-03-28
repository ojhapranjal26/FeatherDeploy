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

export interface QRGenerateResponse {
  qr_token: string
  expires_at: number   // unix ms — QR code validity (5 min)
  ttl_minutes: number  // session duration after claim
}

export interface QRStatusResponse {
  status: 'pending' | 'claimed' | 'expired'
  user_name: string
  user_email: string
}

export interface QRClaimResponse {
  token: string
  user: User
  ttl_minutes: number
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
  /** POST /api/auth/qr/generate — authenticated */
  generate: (ttl_minutes: number): Promise<QRGenerateResponse> =>
    client.post<QRGenerateResponse>('/auth/qr/generate', { ttl_minutes }).then((r) => r.data),

  /** GET /api/auth/qr/{token}/status — public */
  status: (token: string): Promise<QRStatusResponse> =>
    client.get<QRStatusResponse>(`/auth/qr/${token}/status`).then((r) => r.data),

  /** POST /api/auth/qr/{token}/claim — public (scanning device) */
  claim: (token: string): Promise<QRClaimResponse> =>
    client.post<QRClaimResponse>(`/auth/qr/${token}/claim`).then((r) => r.data),
}

