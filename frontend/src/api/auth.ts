import { delay, MOCK_USER } from './_mock'

export interface LoginPayload {
  email: string
  password: string
}

export interface RegisterPayload {
  email: string
  password: string
  name: string
}

export interface User {
  id: string
  email: string
  name: string
  role: 'user' | 'admin' | 'superadmin'
  created_at: string
}

export interface AuthResponse {
  token: string
  expires_at: string
  user: User
}

export const authApi = {
  register: async (data: RegisterPayload): Promise<User> => {
    await delay(600)
    const user: User = { ...MOCK_USER, email: data.email, name: data.name }
    localStorage.setItem('token', 'mock-token')
    return user
  },

  login: async (_data: LoginPayload): Promise<AuthResponse> => {
    await delay(700)
    localStorage.setItem('token', 'mock-token')
    return {
      token: 'mock-token',
      expires_at: new Date(Date.now() + 86_400_000).toISOString(),
      user: MOCK_USER,
    }
  },

  logout: async (): Promise<void> => {
    await delay(200)
    localStorage.removeItem('token')
  },

  me: async (): Promise<User> => {
    await delay(200)
    return MOCK_USER
  },
}
