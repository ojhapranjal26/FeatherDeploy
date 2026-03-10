import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '@/context/AuthContext'

const API_BASE = import.meta.env.VITE_API_BASE ?? ''

interface InviteInfo {
  email: string
  role: string
  expires_at: string
}

const schema = z
  .object({
    name:            z.string().min(2, 'Name must be at least 2 characters'),
    password:        z.string().min(8, 'Password must be at least 8 characters'),
    confirmPassword: z.string(),
  })
  .refine((d) => d.password === d.confirmPassword, {
    message: 'Passwords do not match',
    path: ['confirmPassword'],
  })

type FormData = z.infer<typeof schema>

export function InviteAcceptPage() {
  const { token } = useParams<{ token: string }>()
  const navigate = useNavigate()
  useAuth()

  const [info, setInfo] = useState<InviteInfo | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormData>({ resolver: zodResolver(schema) })

  // Verify token on mount
  useEffect(() => {
    if (!token) { setLoadError('Invalid invitation link.'); setLoading(false); return }
    fetch(`${API_BASE}/api/invitations/${token}`)
      .then((r) => {
        if (!r.ok) return r.json().then((d) => Promise.reject(d.error ?? 'Invalid or expired invitation.'))
        return r.json()
      })
      .then((d: InviteInfo) => setInfo(d))
      .catch((msg: string) => setLoadError(msg))
      .finally(() => setLoading(false))
  }, [token])

  const onSubmit = async (data: FormData) => {
    try {
      const res = await fetch(`${API_BASE}/api/invitations/${token}/accept`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: data.name, password: data.password }),
      })
      if (!res.ok) {
        const d = await res.json()
        toast.error(d.error ?? 'Failed to create account.')
        return
      }
      const d = await res.json()
      // Auto-login using the returned JWT
      localStorage.setItem('token', d.token)
      toast.success('Account created! Welcome to DeployPaaS.')
      navigate('/dashboard')
    } catch {
      toast.error('Something went wrong. Please try again.')
    }
  }

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-muted-foreground text-sm">Verifying invitation…</p>
      </div>
    )
  }

  if (loadError) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-6">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10 text-destructive text-2xl">
          ✕
        </div>
        <h2 className="text-xl font-semibold">Invitation invalid</h2>
        <p className="text-sm text-muted-foreground text-center max-w-xs">{loadError}</p>
        <Button variant="outline" onClick={() => navigate('/login')}>Go to login</Button>
      </div>
    )
  }

  const expiresDate = info ? new Date(info.expires_at) : null

  return (
    <div className="flex min-h-screen">
      {/* Left panel */}
      <div className="relative hidden lg:flex lg:w-[52%] flex-col justify-between overflow-hidden bg-[oklch(0.14_0.025_265)] p-12">
        <div className="pointer-events-none absolute -top-32 -left-32 h-[500px] w-[500px] rounded-full bg-primary/20 blur-3xl" />
        <div className="pointer-events-none absolute bottom-0 right-0 h-[400px] w-[400px] rounded-full bg-violet-500/10 blur-3xl" />
        <div className="relative flex items-center gap-3">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary text-primary-foreground text-base font-bold shadow-lg shadow-primary/30">
            D
          </div>
          <span className="text-lg font-semibold text-white tracking-tight">DeployPaaS</span>
        </div>
        <div className="relative space-y-4">
          <Rocket className="h-10 w-10 text-primary" />
          <h1 className="text-4xl font-bold text-white leading-tight tracking-tight">
            You've been<br />invited.
          </h1>
          <p className="text-base text-white/55 leading-relaxed max-w-sm">
            Set up your account to start deploying applications on DeployPaaS.
          </p>
        </div>
        <div className="relative rounded-xl border border-white/10 bg-white/5 p-5 backdrop-blur-sm text-sm text-white/60">
          Your account will be created with the <strong className="text-white/80">{info?.role}</strong> role.
        </div>
      </div>

      {/* Right panel */}
      <div className="flex flex-1 flex-col items-center justify-center p-6 sm:p-10">
        <div className="mb-8 flex items-center gap-2.5 lg:hidden">
          <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-primary text-primary-foreground text-sm font-bold">D</div>
          <span className="font-semibold tracking-tight">DeployPaaS</span>
        </div>

        <div className="w-full max-w-sm space-y-6">
          <div className="space-y-1.5">
            <h2 className="text-2xl font-semibold tracking-tight">Accept invitation</h2>
            <p className="text-sm text-muted-foreground">
              You were invited as <strong>{info?.email}</strong>. Set your name and password below.
            </p>
            {expiresDate && (
              <p className="text-xs text-amber-500">
                Link expires {expiresDate.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}.
              </p>
            )}
          </div>

          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="name">Full name</Label>
              <Input id="name" placeholder="Jane Smith" className="h-10" {...register('name')} />
              {errors.name && <p className="text-xs text-destructive">{errors.name.message}</p>}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input id="password" type="password" autoComplete="new-password" className="h-10" {...register('password')} />
              {errors.password && <p className="text-xs text-destructive">{errors.password.message}</p>}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="confirmPassword">Confirm password</Label>
              <Input id="confirmPassword" type="password" autoComplete="new-password" className="h-10" {...register('confirmPassword')} />
              {errors.confirmPassword && <p className="text-xs text-destructive">{errors.confirmPassword.message}</p>}
            </div>
            <Button type="submit" className="h-10 w-full font-medium" disabled={isSubmitting}>
              {isSubmitting ? 'Creating account…' : 'Create account'}
            </Button>
          </form>
        </div>
      </div>
    </div>
  )
}
