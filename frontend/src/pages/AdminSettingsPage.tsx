import { useState, useEffect, useRef, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ShieldCheck, Server, ImageIcon, Building2, Mail, Github,
  CheckCircle2, XCircle, Trash2, Eye, EyeOff, ExternalLink, Upload,
  Globe, Clock, Check, Search,
} from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { settingsApi } from '@/api/settings'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDateFull } from '@/lib/dateFormat'
import { cn } from '@/lib/utils'

// ─── IANA timezone list ───────────────────────────────────────────────────────
const ALL_TIMEZONES: string[] = (() => {
  try {
    return (Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf('timeZone')
  } catch {
    return [
      'UTC',
      'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
      'America/Toronto', 'America/Vancouver', 'America/Sao_Paulo',
      'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Rome',
      'Europe/Moscow', 'Europe/Istanbul',
      'Asia/Dubai', 'Asia/Karachi', 'Asia/Kolkata', 'Asia/Dhaka',
      'Asia/Bangkok', 'Asia/Singapore', 'Asia/Shanghai', 'Asia/Tokyo',
      'Australia/Sydney', 'Pacific/Auckland',
      'Africa/Cairo', 'Africa/Nairobi', 'Africa/Johannesburg',
    ]
  }
})()

function tzRegion(tz: string) {
  const i = tz.indexOf('/')
  return i !== -1 ? tz.slice(0, i) : 'Other'
}

// ─── Write-only password field ────────────────────────────────────────────────
function SecretInput(props: React.InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  const [show, setShow] = useState(false)
  const { label, hint, ...rest } = props
  return (
    <div className="space-y-1.5">
      <Label className="text-xs">{label}</Label>
      <div className="relative">
        <Input
          {...rest}
          type={show ? 'text' : 'password'}
          className={cn('pr-9 font-mono text-xs h-8', rest.className)}
        />
        <button
          type="button"
          className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          onClick={() => setShow(v => !v)}
          tabIndex={-1}
        >
          {show ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
      </div>
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  )
}

// ─── Configured / Not configured badge ───────────────────────────────────────
function StatusPill({ ok, label }: { ok: boolean; label: string }) {
  return (
    <span className={cn(
      'inline-flex items-center gap-1 text-[11px] font-medium rounded-full px-2 py-0.5',
      ok
        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
        : 'bg-muted text-muted-foreground',
    )}>
      {ok ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
      {label}
    </span>
  )
}

// ─── Section heading ──────────────────────────────────────────────────────────
function SectionHeader({ icon: Icon, title, desc }: { icon: React.ElementType; title: string; desc: string }) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary shrink-0">
        <Icon className="h-4.5 w-4.5" />
      </div>
      <div>
        <p className="font-medium leading-tight">{title}</p>
        <p className="text-sm text-muted-foreground mt-0.5">{desc}</p>
      </div>
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────
export function AdminSettingsPage() {
  const qc = useQueryClient()
  const { timezone, setTimezone } = useTimezone()

  // ── Timezone ─────────────────────────────────────────────────────────────────
  const [tzSearch, setTzSearch] = useState('')
  const [tzPending, setTzPending] = useState(timezone)
  // Keep pending in sync when the context timezone loads from the server
  useEffect(() => { setTzPending(timezone) }, [timezone])

  const tzFiltered = useMemo(() => {
    const q = tzSearch.toLowerCase().replace(/\s+/g, '_')
    return q ? ALL_TIMEZONES.filter(t => t.toLowerCase().includes(q)) : ALL_TIMEZONES
  }, [tzSearch])
  const tzGrouped = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const t of tzFiltered) {
      const r = tzRegion(t)
      if (!map.has(r)) map.set(r, [])
      map.get(r)!.push(t)
    }
    return map
  }, [tzFiltered])

  const tzMutation = useMutation({
    mutationFn: () => settingsApi.setTimezone(tzPending),
    onSuccess: () => {
      setTimezone(tzPending)
      toast.success(`Platform timezone set to ${tzPending}`)
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'Failed to save timezone'),
  })

  // ── Branding ────────────────────────────────────────────────────────────────
  const { data: branding } = useQuery({ queryKey: ['branding'], queryFn: settingsApi.getBranding })
  const [companyName, setCompanyName] = useState('')
  const [logoUrl, setLogoUrl] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [filePreview, setFilePreview] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (branding) { setCompanyName(branding.company_name); setLogoUrl(branding.logo_url) }
  }, [branding])
  // Revoke object URL on cleanup
  useEffect(() => () => { if (filePreview) URL.revokeObjectURL(filePreview) }, [filePreview])
  const brandingMutation = useMutation({
    mutationFn: async () => {
      let url = logoUrl
      if (selectedFile) {
        const res = await settingsApi.uploadLogo(selectedFile)
        url = res.logo_url
      }
      await settingsApi.setBranding({ company_name: companyName, logo_url: url })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['branding'] })
      setSelectedFile(null)
      setFilePreview(null)
      toast.success('Branding saved')
    },
    onError: () => toast.error('Failed to save branding'),
  })

  // ── SMTP ─────────────────────────────────────────────────────────────────────
  const { data: smtpStatus, refetch: refetchSMTP } = useQuery({
    queryKey: ['settings-smtp'],
    queryFn: settingsApi.getSMTPStatus,
  })
  const [smtpHost, setSmtpHost] = useState('')
  const [smtpPort, setSmtpPort] = useState('587')
  const [smtpUser, setSmtpUser] = useState('')
  const [smtpPass, setSmtpPass] = useState('')
  const [smtpFrom, setSmtpFrom] = useState('')
  const [smtpTLS, setSmtpTLS] = useState('true')
  useEffect(() => {
    if (smtpStatus) {
      setSmtpHost(smtpStatus.host ?? '')
      setSmtpPort(smtpStatus.port || '587')
      setSmtpFrom(smtpStatus.from ?? '')
      setSmtpTLS(smtpStatus.tls || 'true')
    }
  }, [smtpStatus])
  const smtpSaveMutation = useMutation({
    mutationFn: () => settingsApi.setSMTP({
      host: smtpHost || undefined, port: smtpPort || undefined,
      user: smtpUser || undefined, pass: smtpPass || undefined,
      from: smtpFrom || undefined, tls: smtpTLS,
    }),
    onSuccess: () => { refetchSMTP(); toast.success('SMTP settings saved') },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'Failed to save SMTP settings'),
  })
  const smtpDeleteMutation = useMutation({
    mutationFn: settingsApi.deleteSMTP,
    onSuccess: () => {
      refetchSMTP()
      setSmtpHost(''); setSmtpPort('587'); setSmtpUser(''); setSmtpPass(''); setSmtpFrom(''); setSmtpTLS('true')
      toast.success('SMTP settings cleared')
    },
    onError: () => toast.error('Failed to clear SMTP settings'),
  })

  // ── GitHub OAuth ─────────────────────────────────────────────────────────────
  const { data: ghStatus, refetch: refetchGH } = useQuery({
    queryKey: ['settings-github-oauth'],
    queryFn: settingsApi.getGitHubOAuthStatus,
  })
  const [ghClientID, setGhClientID] = useState('')
  const [ghClientSecret, setGhClientSecret] = useState('')
  const ghSaveMutation = useMutation({
    mutationFn: () => settingsApi.setGitHubOAuth({
      client_id:     ghClientID     || undefined,
      client_secret: ghClientSecret || undefined,
    }),
    onSuccess: () => {
      refetchGH(); setGhClientID(''); setGhClientSecret('')
      toast.success('GitHub OAuth credentials saved')
    },
    onError: (e: any) => toast.error(e?.response?.data?.error ?? 'Failed to save GitHub OAuth settings'),
  })
  const ghDeleteMutation = useMutation({
    mutationFn: settingsApi.deleteGitHubOAuth,
    onSuccess: () => { refetchGH(); toast.success('GitHub OAuth credentials removed') },
    onError: () => toast.error('Failed to remove GitHub OAuth settings'),
  })

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">System Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">Platform-wide configuration (superadmin only).</p>
      </div>

      {/* ── Timezone ──────────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <SectionHeader icon={Globe} title="Platform Timezone"
          desc="All dates and times shown to every user in the panel will use this timezone." />

        {/* Preview */}
        <div className="flex items-center gap-2 rounded-lg border bg-muted/30 px-4 py-2.5 text-sm">
          <Clock className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="text-muted-foreground">Preview in <strong className="text-foreground font-mono">{tzPending}</strong>:</span>
          <span className="font-mono font-medium ml-1">{formatDateFull(new Date().toISOString(), tzPending)}</span>
        </div>

        {/* Search */}
        <div className="space-y-1.5">
          <Label className="text-xs">Search timezone</Label>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground pointer-events-none" />
            <Input
              className="pl-8 h-8 text-xs"
              placeholder="e.g. Asia/Kolkata, New_York, UTC…"
              value={tzSearch}
              onChange={e => setTzSearch(e.target.value)}
            />
          </div>
        </div>

        {/* Scrollable timezone list */}
        <div className="rounded-lg border overflow-hidden">
          <div className="max-h-72 overflow-y-auto divide-y">
            {tzGrouped.size === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">No timezones match "{tzSearch}"</p>
            ) : (
              Array.from(tzGrouped.entries()).map(([region, tzs]) => (
                <div key={region}>
                  <div className="sticky top-0 bg-muted/80 backdrop-blur-sm px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground border-b">
                    {region}
                  </div>
                  {tzs.map(tz => (
                    <button
                      key={tz}
                      type="button"
                      onClick={() => setTzPending(tz)}
                      className={cn(
                        'flex w-full items-center justify-between px-4 py-2 text-sm transition-colors hover:bg-muted/40',
                        tzPending === tz && 'bg-primary/8 text-primary font-medium',
                      )}
                    >
                      <span>{tz.replace(/_/g, ' ')}</span>
                      {tzPending === tz && <Check className="h-3.5 w-3.5 shrink-0" />}
                    </button>
                  ))}
                </div>
              ))
            )}
          </div>
        </div>

        <div className="flex items-center justify-between">
          <p className="text-xs text-muted-foreground">
            Current: <span className="font-mono">{timezone}</span>
          </p>
          <button
            type="button"
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
            onClick={() => tzMutation.mutate()}
            disabled={tzMutation.isPending || tzPending === timezone}
          >
            <Check className="h-3.5 w-3.5" />
            {tzMutation.isPending ? 'Saving…' : 'Save timezone'}
          </button>
        </div>
      </section>

      {/* ── Branding ──────────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <SectionHeader icon={Building2} title="Company Branding"
          desc="Customise the name and logo shown on the login page and sidebar." />

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="company-name" className="text-xs">Company name</Label>
            <Input id="company-name" className="h-8 text-xs" placeholder="FeatherDeploy"
              value={companyName} onChange={(e) => setCompanyName(e.target.value)} maxLength={120} />
            <p className="text-[11px] text-muted-foreground">Shown in the sidebar and login page.</p>
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs">Logo</Label>
            <div className="flex gap-2">
              <Input
                className="h-8 text-xs"
                placeholder="https://example.com/logo.png"
                value={logoUrl}
                onChange={(e) => {
                  setLogoUrl(e.target.value)
                  setSelectedFile(null)
                  if (filePreview) { URL.revokeObjectURL(filePreview); setFilePreview(null) }
                }}
              />
              <Button type="button" size="sm" variant="outline" className="h-8 gap-1.5 shrink-0 text-xs"
                onClick={() => fileInputRef.current?.click()}>
                <Upload className="h-3.5 w-3.5" />
                Browse
              </Button>
            </div>
            <p className="text-[11px] text-muted-foreground">
              Paste an https:// URL or upload a file — recommended 120×40 px, PNG/JPG/SVG, max 2 MB.
            </p>
            <input
              ref={fileInputRef}
              type="file"
              accept="image/jpeg,image/png,image/gif,image/webp,image/svg+xml"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (!f) return
                setSelectedFile(f)
                if (filePreview) URL.revokeObjectURL(filePreview)
                setFilePreview(URL.createObjectURL(f))
                setLogoUrl('')
                e.target.value = ''
              }}
            />
          </div>
        </div>
        {(filePreview || logoUrl) && (
          <div className="flex items-center gap-3 rounded-lg border bg-muted/40 px-4 py-2.5">
            <ImageIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
            <img src={filePreview ?? logoUrl} alt="Logo preview" className="h-8 w-auto max-w-[200px] object-contain"
              onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }} />
            {selectedFile && (
              <span className="text-[11px] text-muted-foreground ml-1 truncate">
                {selectedFile.name} — will be uploaded on save
              </span>
            )}
          </div>
        )}
        <div className="flex justify-end">
          <Button size="sm" onClick={() => brandingMutation.mutate()} disabled={brandingMutation.isPending}>
            {brandingMutation.isPending ? 'Saving…' : 'Save branding'}
          </Button>
        </div>
      </section>

      {/* ── SMTP ──────────────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <div className="flex items-start justify-between flex-wrap gap-3">
          <SectionHeader icon={Mail} title="SMTP / Email"
            desc="Transactional email for invitations. Credentials are stored encrypted with AES-256-GCM." />
          <StatusPill ok={smtpStatus?.configured ?? false}
            label={smtpStatus?.configured ? 'Configured' : 'Not configured'} />
        </div>

        {smtpStatus?.configured && (
          <div className="rounded-lg border bg-muted/30 px-3 py-2.5 text-xs space-y-1">
            <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Current configuration</p>
            <div className="grid grid-cols-2 gap-x-6 gap-y-0.5 font-mono">
              {smtpStatus.host && <span>Host: <strong>{smtpStatus.host}</strong></span>}
              {smtpStatus.port && <span>Port: <strong>{smtpStatus.port}</strong></span>}
              {smtpStatus.from && <span>From: <strong>{smtpStatus.from}</strong></span>}
              <span>TLS: <strong>{smtpStatus.tls === 'true' ? 'yes' : 'no'}</strong></span>
              <span>Username: <strong>{smtpStatus.username_set ? '●●●● set' : '— not set'}</strong></span>
              <span>Password: <strong>{smtpStatus.password_set ? '●●●● set' : '— not set'}</strong></span>
            </div>
          </div>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="smtp-host" className="text-xs">Host</Label>
            <Input id="smtp-host" className="h-8 font-mono text-xs" placeholder="smtp.gmail.com"
              value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="smtp-port" className="text-xs">Port</Label>
            <Input id="smtp-port" className="h-8 font-mono text-xs" placeholder="587"
              value={smtpPort} onChange={(e) => setSmtpPort(e.target.value)} />
          </div>
          <SecretInput
            label="Username / email"
            placeholder={smtpStatus?.username_set ? '●●●● leave blank to keep current' : 'user@example.com'}
            hint="Leave blank to keep the stored value unchanged."
            value={smtpUser}
            onChange={(e) => setSmtpUser(e.target.value)}
          />
          <SecretInput
            label="Password / app password"
            placeholder={smtpStatus?.password_set ? '●●●● leave blank to keep current' : 'app password or SMTP password'}
            hint="Leave blank to keep the stored value unchanged."
            value={smtpPass}
            onChange={(e) => setSmtpPass(e.target.value)}
          />
          <div className="space-y-1.5">
            <Label htmlFor="smtp-from" className="text-xs">From address</Label>
            <Input id="smtp-from" className="h-8 text-xs" placeholder="FeatherDeploy <noreply@example.com>"
              value={smtpFrom} onChange={(e) => setSmtpFrom(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="smtp-tls" className="text-xs">TLS / STARTTLS</Label>
            <select
              id="smtp-tls"
              className="flex h-8 w-full rounded-md border border-input bg-background px-3 text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={smtpTLS}
              onChange={(e) => setSmtpTLS(e.target.value)}
            >
              <option value="true">Enabled (STARTTLS — recommended)</option>
              <option value="false">Disabled (plain / implicit TLS)</option>
            </select>
          </div>
        </div>
        <div className="flex justify-between items-center gap-2 pt-1">
          {smtpStatus?.configured && (
            <Button size="sm" variant="outline"
              className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10 border-destructive/30"
              onClick={() => smtpDeleteMutation.mutate()}
              disabled={smtpDeleteMutation.isPending}
            >
              <Trash2 className="h-3.5 w-3.5" />
              {smtpDeleteMutation.isPending ? 'Clearing…' : 'Clear SMTP'}
            </Button>
          )}
          <Button size="sm" className="ml-auto"
            onClick={() => smtpSaveMutation.mutate()}
            disabled={smtpSaveMutation.isPending}
          >
            {smtpSaveMutation.isPending ? 'Saving…' : 'Save SMTP'}
          </Button>
        </div>
      </section>

      {/* ── GitHub OAuth ──────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <div className="flex items-start justify-between flex-wrap gap-3">
          <SectionHeader icon={Github} title="GitHub OAuth"
            desc="Lets users connect their GitHub account to browse repos, branches and folders when creating services." />
          <StatusPill ok={ghStatus?.configured ?? false}
            label={ghStatus?.configured ? 'Configured' : 'Not configured'} />
        </div>

        {/* Setup guide */}
        <div className="rounded-lg border bg-muted/20 px-4 py-3 space-y-2 text-xs">
          <p className="font-semibold">How to create a GitHub OAuth App</p>
          <ol className="list-decimal list-inside space-y-1.5 text-muted-foreground">
            <li>
              Open{' '}
              <a href="https://github.com/settings/developers" target="_blank" rel="noreferrer"
                className="text-foreground underline inline-flex items-center gap-0.5">
                github.com/settings/developers <ExternalLink className="h-2.5 w-2.5" />
              </a>
              {' '}→ <strong>OAuth Apps</strong> → <strong>New OAuth App</strong>
            </li>
            <li>
              <strong>Application name</strong>: anything, e.g. <code>FeatherDeploy</code>
            </li>
            <li>
              <strong>Homepage URL</strong>: your frontend URL, e.g.{' '}
              <code>{window.location.origin}</code>
            </li>
            <li>
              <strong>Authorization callback URL</strong> — copy this exactly:
              <div className="mt-1 flex items-center gap-2">
                <code className="break-all bg-muted px-2 py-0.5 rounded">{window.location.origin}/api/github/callback</code>
                <Badge variant="outline" className="text-[10px] shrink-0">exact match required</Badge>
              </div>
            </li>
            <li>Click <strong>Register application</strong></li>
            <li>Copy the <strong>Client ID</strong> and generate + copy the <strong>Client Secret</strong></li>
            <li>Paste both below and click <strong>Save credentials</strong></li>
          </ol>
        </div>

        {ghStatus?.configured && (
          <div className="rounded-lg border bg-muted/30 px-3 py-2.5 text-xs space-y-1">
            <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Stored credentials</p>
            <div className="font-mono space-y-0.5">
              <p>Client ID: <strong>{ghStatus.client_id || '(set via env var)'}</strong></p>
              <p>Client Secret: <strong>{ghStatus.client_secret_set ? '●●●● set' : '— not set'}</strong></p>
            </div>
          </div>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="gh-client-id" className="text-xs">Client ID</Label>
            <Input id="gh-client-id" className="h-8 font-mono text-xs"
              placeholder={ghStatus?.client_id ? ghStatus.client_id : 'Ov23li…'}
              value={ghClientID}
              onChange={(e) => setGhClientID(e.target.value)}
            />
            <p className="text-[11px] text-muted-foreground">Leave blank to keep the stored value.</p>
          </div>
          <SecretInput
            label="Client Secret"
            placeholder={ghStatus?.client_secret_set ? '●●●● leave blank to keep current' : 'paste secret here'}
            hint="Leave blank to keep the stored value."
            value={ghClientSecret}
            onChange={(e) => setGhClientSecret(e.target.value)}
          />
        </div>
        <div className="flex justify-between items-center gap-2 pt-1">
          {ghStatus?.configured && (
            <Button size="sm" variant="outline"
              className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10 border-destructive/30"
              onClick={() => ghDeleteMutation.mutate()}
              disabled={ghDeleteMutation.isPending}
            >
              <Trash2 className="h-3.5 w-3.5" />
              {ghDeleteMutation.isPending ? 'Removing…' : 'Remove credentials'}
            </Button>
          )}
          <Button size="sm" className="ml-auto"
            disabled={ghSaveMutation.isPending || (!ghClientID && !ghClientSecret)}
            onClick={() => ghSaveMutation.mutate()}
          >
            {ghSaveMutation.isPending ? 'Saving…' : 'Save credentials'}
          </Button>
        </div>
      </section>

      {/* ── Placeholder cards ─────────────────────────────────────────────── */}
      <div className="grid gap-4 sm:grid-cols-2">
        {([
          { icon: ShieldCheck, title: 'Security',       desc: 'JWT expiry, password policy' },
          { icon: Server,      title: 'Infrastructure', desc: 'Resource limits, quotas' },
        ] as const).map(({ icon: Icon, title, desc }) => (
          <div key={title} className="rounded-xl border bg-card p-5 space-y-3 opacity-60">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <Icon className="h-4.5 w-4.5" />
            </div>
            <div>
              <p className="font-medium">{title}</p>
              <p className="text-sm text-muted-foreground mt-0.5">{desc}</p>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
