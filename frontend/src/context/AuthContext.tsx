import { createContext, useContext, useEffect, useRef, useState, useCallback } from 'react'
import type { ReactNode } from 'react'
import { authApi } from '@/api/auth'
import type { User } from '@/api/auth'

interface AuthContextValue {
  user: User | null
  token: string | null
  isAuthenticated: boolean
  isLoading: boolean
  login: (email: string, password: string) => Promise<void>
  loginWithToken: (token: string, user: User) => void
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

/** Peek at the JWT exp claim without verifying the signature. */
function jwtExpiry(tok: string): number | null {
  try {
    const payload = JSON.parse(atob(tok.split('.')[1]))
    return typeof payload.exp === 'number' ? payload.exp * 1000 : null
  } catch {
    return null
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [token, setToken] = useState<string | null>(
    () => localStorage.getItem('token')
  )
  const [isLoading, setIsLoading] = useState(!!localStorage.getItem('token'))
  const expiryTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Auto-logout when the JWT expires (critical for short-lived QR sessions).
  useEffect(() => {
    if (expiryTimer.current) clearTimeout(expiryTimer.current)
    if (!token) return
    const exp = jwtExpiry(token)
    if (!exp) return
    const delay = exp - Date.now()
    if (delay <= 0) {
      localStorage.removeItem('token')
      setToken(null)
      setUser(null)
      return
    }
    expiryTimer.current = setTimeout(() => {
      localStorage.removeItem('token')
      setToken(null)
      setUser(null)
      window.location.href = '/login'
    }, delay)
    return () => {
      if (expiryTimer.current) clearTimeout(expiryTimer.current)
    }
  }, [token])

  useEffect(() => {
    if (!token) {
      setIsLoading(false)
      return
    }
    authApi
      .me()
      .then(setUser)
      .catch(() => {
        localStorage.removeItem('token')
        setToken(null)
      })
      .finally(() => setIsLoading(false))
  }, [token])

  const login = useCallback(async (email: string, password: string) => {
    const data = await authApi.login({ email, password })
    localStorage.setItem('token', data.token)
    setToken(data.token)
    setUser(data.user)
  }, [])

  const loginWithToken = useCallback((tok: string, u: User) => {
    localStorage.setItem('token', tok)
    setToken(tok)
    setUser(u)
  }, [])

  const logout = useCallback(() => {
    authApi.logout().catch(() => {})
    localStorage.removeItem('token')
    setToken(null)
    setUser(null)
  }, [])

  return (
    <AuthContext.Provider
      value={{ user, token, isAuthenticated: !!user, isLoading, login, loginWithToken, logout }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

