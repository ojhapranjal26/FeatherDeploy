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
