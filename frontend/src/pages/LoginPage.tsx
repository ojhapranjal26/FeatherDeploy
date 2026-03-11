import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useNavigate } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { useAuth } from '@/context/AuthContext'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { toast } from 'sonner'
import { Rocket, GitBranch, Globe, Zap, Feather } from 'lucide-react'
import { settingsApi, type Branding } from '@/api/settings'

const schema = z.object({
  email:    z.string().email('Invalid email address'),
  password: z.string().min(8, 'Password must be at least 8 characters'),
})
type FormData = z.infer<typeof schema>

const FEATURES = [
  { icon: Rocket,    text: 'One-click deployments from Git' },
  { icon: GitBranch, text: 'Preview environments for every branch' },
  { icon: Globe,     text: 'Global edge network with TLS' },
  { icon: Zap,       text: 'Instant rollbacks & deployment logs' },
]

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [branding, setBranding] = useState<Branding>({ company_name: '', logo_url: '' })

  useEffect(() => {
    settingsApi.getBranding().then(setBranding).catch(() => {})
  }, [])

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormData>({ resolver: zodResolver(schema) })

  const onSubmit = async (data: FormData) => {
    try {
      await login(data.email, data.password)
      navigate('/dashboard')
    } catch {
      toast.error('Invalid email or password.')
    }
  }

  const platformName = branding.company_name || 'FeatherDeploy'

  return (
    <div className="flex min-h-screen">
      {/* ── Left panel ─────────────────────────────────────────────────── */}
      <div className="relative hidden lg:flex lg:w-[52%] flex-col justify-between overflow-hidden bg-[oklch(0.14_0.025_265)] p-12">
        {/* Gradient orb */}
        <div className="pointer-events-none absolute -top-32 -left-32 h-[500px] w-[500px] rounded-full bg-primary/20 blur-3xl" />
        <div className="pointer-events-none absolute bottom-0 right-0 h-[400px] w-[400px] rounded-full bg-violet-500/10 blur-3xl" />

        {/* Logo */}
        <div className="relative flex items-center gap-3">
          {branding.logo_url ? (
            <img
              src={branding.logo_url}
              alt={platformName}
              className="h-9 w-auto max-w-[160px] object-contain"
            />
          ) : (
            <>
              <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-lg shadow-primary/30">
                <Feather className="h-5 w-5" />
              </div>
              <span className="text-lg font-semibold text-white tracking-tight">{platformName}</span>
            </>
          )}
        </div>

        {/* Headline */}
        <div className="relative space-y-6">
          <div>
            <h1 className="text-4xl font-bold text-white leading-tight tracking-tight">
              Ship faster.<br />Scale effortlessly.
            </h1>
            <p className="mt-3 text-base text-white/55 leading-relaxed max-w-sm">
              The developer platform that gets out of your way.
              Deploy any app in seconds, with zero infrastructure knowledge required.
            </p>
          </div>

          <ul className="space-y-3">
            {FEATURES.map(({ icon: Icon, text }) => (
              <li key={text} className="flex items-center gap-3">
                <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-white/8 text-primary">
                  <Icon className="h-3.5 w-3.5" />
                </div>
                <span className="text-sm text-white/70">{text}</span>
              </li>
            ))}
          </ul>
        </div>

        {/* Testimonial */}
        <div className="relative rounded-xl border border-white/10 bg-white/5 p-5 backdrop-blur-sm">
          <p className="text-sm text-white/70 leading-relaxed">
            "{platformName} cut our release cycle from 2 weeks to a single afternoon. The RBAC model means every team has exactly the access they need."
          </p>
          <div className="mt-3 flex items-center gap-2.5">
            <div className="h-7 w-7 rounded-full bg-primary/30 flex items-center justify-center text-xs font-semibold text-primary">
              JL
            </div>
            <div>
              <p className="text-xs font-medium text-white/80">Jamie Lee</p>
              <p className="text-xs text-white/40">CTO at Acme Corp.</p>
            </div>
          </div>
        </div>
      </div>

      {/* ── Right panel ────────────────────────────────────────────────── */}
      <div className="flex flex-1 flex-col items-center justify-center p-6 sm:p-10">
        {/* Mobile logo */}
        <div className="mb-8 flex items-center gap-2.5 lg:hidden">
          {branding.logo_url ? (
            <img
              src={branding.logo_url}
              alt={platformName}
              className="h-8 w-auto max-w-[140px] object-contain"
            />
          ) : (
            <>
              <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-primary text-primary-foreground">
                <Feather className="h-4 w-4" />
              </div>
              <span className="font-semibold tracking-tight">{platformName}</span>
            </>
          )}
        </div>

        <div className="w-full max-w-sm space-y-6">
          <div className="space-y-1.5">
            <h2 className="text-2xl font-semibold tracking-tight">Welcome back</h2>
            <p className="text-sm text-muted-foreground">Sign in to your account to continue</p>
          </div>

          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="email">Email</Label>
              <Input
                id="email"
                type="email"
                placeholder="you@company.com"
                autoComplete="email"
                className="h-10"
                {...register('email')}
              />
              {errors.email && (
                <p className="text-xs text-destructive">{errors.email.message}</p>
              )}
            </div>
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <Label htmlFor="password">Password</Label>
              </div>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                className="h-10"
                {...register('password')}
              />
              {errors.password && (
                <p className="text-xs text-destructive">{errors.password.message}</p>
              )}
            </div>
            <Button type="submit" className="h-10 w-full font-medium" disabled={isSubmitting}>
              {isSubmitting ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>

          <p className="text-center text-sm text-muted-foreground">
            Access is by invitation only. Contact your administrator.
          </p>
        </div>
      </div>
    </div>
  )
}

