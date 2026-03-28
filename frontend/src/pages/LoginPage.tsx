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
import { Rocket, GitBranch, Globe, Zap, Feather, Github, ArrowRight, Shield, RotateCcw, Smartphone, QrCode } from 'lucide-react'
import { settingsApi, type Branding } from '@/api/settings'

const schema = z.object({
  email:    z.string().email('Invalid email address'),
  password: z.string().min(8, 'Password must be at least 8 characters'),
})
type FormData = z.infer<typeof schema>

const FEATURES = [
  { icon: Rocket,    text: 'One-click deployments from Git' },
  { icon: GitBranch, text: 'Preview environments for every branch' },
  { icon: Globe,     text: 'Custom domains with automatic TLS' },
  { icon: Zap,       text: 'Instant rollbacks & live deployment logs' },
  { icon: Shield,    text: 'Role-based access control built-in' },
  { icon: RotateCcw, text: 'Self-hosted — your infra, your data' },
]

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [branding, setBranding] = useState<Branding>({ company_name: '', logo_url: '' })
  const [loginMode, setLoginMode] = useState<'password' | 'qr'>('password')

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
      <div className="relative hidden lg:flex lg:w-[55%] flex-col justify-between overflow-hidden bg-[oklch(0.13_0.03_265)] p-12">
        {/* Background decoration */}
        <div className="pointer-events-none absolute -top-40 -left-40 h-[600px] w-[600px] rounded-full bg-primary/15 blur-[120px]" />
        <div className="pointer-events-none absolute bottom-0 right-0 h-[500px] w-[500px] rounded-full bg-violet-600/12 blur-[100px]" />
        <div className="pointer-events-none absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 h-[800px] w-[800px] rounded-full bg-indigo-500/5 blur-[150px]" />

        {/* Subtle grid pattern */}
        <div className="pointer-events-none absolute inset-0 opacity-[0.03]"
          style={{ backgroundImage: 'linear-gradient(rgba(255,255,255,.4) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.4) 1px, transparent 1px)', backgroundSize: '48px 48px' }} />

        {/* Logo */}
        <div className="relative flex items-center gap-4">
          {branding.logo_url ? (
            <img
              src={branding.logo_url}
              alt={platformName}
              className="h-16 w-auto max-w-[220px] object-contain"
            />
          ) : (
            <>
              <div className="flex h-16 w-16 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-2xl shadow-primary/40 ring-1 ring-white/10">
                <Feather className="h-8 w-8" />
              </div>
              <div>
                <span className="text-2xl font-bold text-white tracking-tight">{platformName}</span>
                <p className="text-xs text-white/40 font-medium mt-0.5">Self-hosted PaaS</p>
              </div>
            </>
          )}
        </div>

        {/* Headline */}
        <div className="relative space-y-8">
          <div>
            <div className="inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-primary/15 border border-primary/25 mb-6">
              <span className="h-1.5 w-1.5 rounded-full bg-primary animate-pulse" />
              <span className="text-xs font-medium text-primary/90">Open Source · Self-Hosted</span>
            </div>
            <h1 className="text-5xl font-bold text-white leading-[1.15] tracking-tight">
              Ship faster.<br />
              <span className="text-transparent bg-clip-text bg-gradient-to-r from-primary via-violet-400 to-indigo-400">
                Scale effortlessly.
              </span>
            </h1>
            <p className="mt-4 text-base text-white/50 leading-relaxed max-w-md">
              The self-hosted developer platform that gets out of your way.
              Deploy any app in seconds with zero infrastructure knowledge required.
            </p>
          </div>

          {/* Feature grid */}
          <div className="grid grid-cols-2 gap-3">
            {FEATURES.map(({ icon: Icon, text }) => (
              <div key={text} className="flex items-center gap-2.5">
                <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-white/6 text-primary border border-white/8">
                  <Icon className="h-3.5 w-3.5" />
                </div>
                <span className="text-sm text-white/65 leading-snug">{text}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Footer with GitHub repo link */}
        <div className="relative flex items-center justify-between">
          <a
            href="https://github.com/ojhapranjal26/FeatherDeploy"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-2 text-sm text-white/40 hover:text-white/70 transition-colors group"
          >
            <Github className="h-4 w-4" />
            <span>ojhapranjal26/FeatherDeploy</span>
            <ArrowRight className="h-3.5 w-3.5 opacity-0 group-hover:opacity-100 -translate-x-1 group-hover:translate-x-0 transition-all" />
          </a>
          <span className="text-xs text-white/25 font-mono">MIT License</span>
        </div>
      </div>

      {/* ── Right panel ────────────────────────────────────────────────── */}
      <div className="flex flex-1 flex-col items-center justify-center p-6 sm:p-10 bg-background">
        {/* Mobile logo */}
        <div className="mb-10 flex flex-col items-center gap-3 lg:hidden">
          {branding.logo_url ? (
            <img
              src={branding.logo_url}
              alt={platformName}
              className="h-16 w-auto max-w-[200px] object-contain"
            />
          ) : (
            <>
              <div className="flex h-[72px] w-[72px] items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-xl shadow-primary/30">
                <Feather className="h-9 w-9" />
              </div>
              <span className="text-2xl font-bold tracking-tight">{platformName}</span>
            </>
          )}
        </div>

        <div className="w-full max-w-[360px] space-y-7">
          {/* Heading */}
          <div className="space-y-1.5">
            <h2 className="text-2xl font-bold tracking-tight">Welcome back</h2>
            <p className="text-sm text-muted-foreground">Sign in to your {platformName} dashboard</p>
          </div>

          {/* Mode toggle */}
          <div className="flex rounded-lg border border-border/60 p-0.5 bg-muted/40">
            <button
              type="button"
              onClick={() => setLoginMode('password')}
              className={`flex flex-1 items-center justify-center gap-1.5 rounded-md py-2 text-xs font-medium transition-colors ${
                loginMode === 'password' ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <Shield className="h-3.5 w-3.5" /> Password
            </button>
            <button
              type="button"
              onClick={() => setLoginMode('qr')}
              className={`flex flex-1 items-center justify-center gap-1.5 rounded-md py-2 text-xs font-medium transition-colors ${
                loginMode === 'qr' ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <QrCode className="h-3.5 w-3.5" /> QR Login
            </button>
          </div>

          {/* ── Password form ── */}
          {loginMode === 'password' && (
            <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="email">Email address</Label>
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
                <Label htmlFor="password">Password</Label>
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
              <Button
                type="submit"
                className="h-10 w-full font-medium gap-2 mt-2"
                disabled={isSubmitting}
              >
                {isSubmitting
                  ? 'Signing in…'
                  : <><span>Sign in</span><ArrowRight className="h-4 w-4" /></>}
              </Button>
            </form>
          )}

          {/* ── QR login instructions ── */}
          {loginMode === 'qr' && (
            <div className="rounded-xl border border-border/60 bg-muted/30 p-5 space-y-4">
              <div className="flex items-start gap-3">
                <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                  <Smartphone className="h-4 w-4" />
                </div>
                <div className="space-y-1">
                  <p className="text-sm font-medium">Scan QR from a trusted device</p>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    Log into your {platformName} dashboard on a trusted device, click the
                    <span className="font-medium text-foreground"> smartphone icon</span> next to your name in the sidebar,
                    and scan the QR code with this device's camera.
                  </p>
                </div>
              </div>

              <ol className="space-y-2 text-xs text-muted-foreground">
                {[
                  'Open ' + platformName + ' on a trusted device',
                  'Click the smartphone icon (Login on another device)',
                  'Choose a session duration (up to 1 hour)',
                  'Scan the QR code with this device',
                  'Tap Authorize Login',
                ].map((step, i) => (
                  <li key={i} className="flex items-start gap-2">
                    <span className="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-primary/15 text-primary text-[10px] font-bold">{i + 1}</span>
                    {step}
                  </li>
                ))}
              </ol>
            </div>
          )}

          {/* Divider */}
          <div className="relative">
            <div className="absolute inset-0 flex items-center">
              <span className="w-full border-t border-border/60" />
            </div>
            <div className="relative flex justify-center text-xs text-muted-foreground">
              <span className="bg-background px-3">Access is by invitation only</span>
            </div>
          </div>

          <p className="text-center text-xs text-muted-foreground">
            Don't have access?{' '}
            <span className="text-foreground/70">Contact your administrator</span>
            {' '}for an invitation link.
          </p>

          {/* Repo link on mobile */}
          <div className="flex justify-center lg:hidden">
            <a
              href="https://github.com/ojhapranjal26/FeatherDeploy"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              <Github className="h-3.5 w-3.5" />
              ojhapranjal26/FeatherDeploy
            </a>
          </div>
        </div>
      </div>
    </div>
  )
}

