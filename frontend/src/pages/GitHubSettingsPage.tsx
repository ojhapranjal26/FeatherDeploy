import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Github, CheckCircle2, AlertCircle, Loader2, Key, Plus, Trash2,
  Download, Copy, Check, AppWindow, RefreshCw, XCircle, Clock,
} from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter,
  DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog'
import { useAuth } from '@/context/AuthContext'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDate } from '@/lib/dateFormat'

const API_BASE = import.meta.env.VITE_API_BASE ?? ''

// ─── Shared fetch helper ────────────────────────────────────────────────────
async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const token = localStorage.getItem('token')
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    const d = await res.json().catch(() => ({}))
    throw new Error(d.error ?? `Request failed (${res.status})`)
  }
  return res.json()
}

// ─── Types ──────────────────────────────────────────────────────────────────
interface GitHubOAuthStatus {
  connected: boolean
  github_login: string
  configured: boolean
}

interface GitHubAppStatus {
  configured: boolean
  app_id: string
  app_name: string
  installation_id: string
}

interface GitHubAppConfig {
  app_id: string
  app_name: string
  installation_id: string
  client_id: string
  private_key_pem: string  // "(set)" when already saved
  webhook_secret: string
  client_secret: string
}

interface SSHKey {
  id: number
  name: string
  public_key: string
  fingerprint: string
  has_private: boolean
  created_at: string
}

// ─── OAuth Tab ──────────────────────────────────────────────────────────────
function OAuthTab() {
  const [searchParams] = useSearchParams()
  const [status, setStatus] = useState<GitHubOAuthStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [disconnecting, setDisconnecting] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [tokenExpiredBanner, setTokenExpiredBanner] = useState(false)
  const { user } = useAuth()

  const canConnect = user?.role === 'superadmin' || user?.role === 'admin'

  const load = () => {
    setLoading(true)
    apiFetch<GitHubOAuthStatus>('/api/github/status')
      .then((s) => {
        // If the server cleared the token (connected→false) without user action,
        // the status endpoint auto-detects staleness — show a banner.
        setStatus((prev) => {
          if (prev?.connected && !s.connected) setTokenExpiredBanner(true)
          return s
        })
      })
      .catch(() => toast.error('Failed to load GitHub OAuth status.'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    const connected = searchParams.get('connected')
    const error = searchParams.get('error')
    if (connected === '1') toast.success('GitHub account connected successfully!')
    if (error) toast.error(`GitHub connection failed: ${decodeURIComponent(error)}`)
    load()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const handleConnect = async () => {
    setConnecting(true)
    try {
      const data = await apiFetch<{ url: string }>('/api/github/auth')
      window.location.href = data.url
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Could not start OAuth flow.')
      setConnecting(false)
    }
  }

  const handleDisconnect = async () => {
    setDisconnecting(true)
    try {
      await apiFetch('/api/github/disconnect', { method: 'DELETE' })
      toast.success('GitHub account disconnected.')
      load()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to disconnect.')
    } finally {
      setDisconnecting(false)
    }
  }

  return (
    <div className="space-y-4">
      {!canConnect && (
        <div className="flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
          <AlertCircle className="h-4 w-4 shrink-0" />
          GitHub integration is restricted to admins and super-admins. Contact your administrator to connect GitHub for deployments.
        </div>
      )}

      {tokenExpiredBanner && (
        <div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
          <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
          <div className="flex-1">
            <p className="font-medium">GitHub token expired</p>
            <p className="mt-0.5 text-xs opacity-80">Your GitHub token was revoked or expired and has been cleared. Reconnect to restore access to your repositories.</p>
          </div>
          <button type="button" className="shrink-0 opacity-60 hover:opacity-100" onClick={() => setTokenExpiredBanner(false)}>
            <XCircle className="h-4 w-4" />
          </button>
        </div>
      )}
      <p className="text-sm text-muted-foreground">
        Connect your personal GitHub account via OAuth to browse and deploy your repositories.
      </p>

      <div className="rounded-xl border border-border bg-card p-6">
        <div className="flex items-start gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl border border-border bg-muted">
            <Github className="h-6 w-6" />
          </div>

          <div className="flex-1 space-y-1">
            <p className="font-medium">GitHub OAuth App</p>
            <p className="text-sm text-muted-foreground">
              Personal access — connects your individual GitHub account.
            </p>

            {loading ? (
              <div className="flex items-center gap-2 pt-2 text-sm text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" /> Checking…
              </div>
            ) : !status?.configured ? (
              <div className="flex items-center gap-2 pt-2 text-sm text-amber-500">
                <AlertCircle className="h-3.5 w-3.5" />
                Set <code className="font-mono">GITHUB_CLIENT_ID</code> and{' '}
                <code className="font-mono">GITHUB_CLIENT_SECRET</code> on the server.
              </div>
            ) : status.connected ? (
              <div className="flex items-center gap-2 pt-2 text-sm text-emerald-600 dark:text-emerald-400">
                <CheckCircle2 className="h-3.5 w-3.5" />
                Connected as <strong>@{status.github_login}</strong>
              </div>
            ) : (
              <p className="pt-2 text-sm text-muted-foreground">Not connected</p>
            )}
          </div>

          <div className="shrink-0">
            {!loading && status?.configured && canConnect && (
              status.connected ? (
                <Button variant="outline" size="sm" onClick={handleDisconnect} disabled={disconnecting}>
                  {disconnecting
                    ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Disconnecting…</>
                    : 'Disconnect'}
                </Button>
              ) : (
                <Button size="sm" onClick={handleConnect} disabled={connecting}>
                  {connecting
                    ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Redirecting…</>
                    : <><Github className="mr-1.5 h-3.5 w-3.5" />Connect GitHub</>}
                </Button>
              )
            )}
          </div>
        </div>
      </div>

      {status?.connected && (
        <div className="rounded-xl border border-border bg-card p-6 space-y-3">
          <h3 className="text-sm font-medium">Permissions granted</h3>
          <ul className="space-y-1.5 text-sm text-muted-foreground">
            <li className="flex items-center gap-2"><CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" /> Read access to repositories</li>
            <li className="flex items-center gap-2"><CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" /> Read user profile information</li>
          </ul>
        </div>
      )}
    </div>
  )
}

// ─── GitHub App Tab ─────────────────────────────────────────────────────────
interface WebhookDelivery {
  id: number
  guid: string
  delivered_at: string
  redelivery: boolean
  duration: number
  status: string
  status_code: number
  event: string
  action: string | null
  repository_id: number | null
}

function GitHubAppTab({ isSuperAdmin }: { isSuperAdmin: boolean }) {
  const { timezone } = useTimezone()
  const [status, setStatus] = useState<GitHubAppStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [configOpen, setConfigOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [repos, setRepos] = useState<{ full_name: string; private: boolean }[]>([])
  const [reposLoading, setReposLoading] = useState(false)
  const [deliveries, setDeliveries] = useState<WebhookDelivery[] | null>(null)
  const [deliveriesLoading, setDeliveriesLoading] = useState(false)
  const [copied, setCopied] = useState(false)

  const [form, setForm] = useState<Partial<GitHubAppConfig>>({})

  const webhookURL = `${window.location.origin}/api/github-app/webhook`

  const loadStatus = () => {
    apiFetch<GitHubAppStatus>('/api/github-app/status')
      .then(setStatus)
      .catch(() => toast.error('Failed to load GitHub App status.'))
      .finally(() => setLoading(false))
  }

  const loadConfig = () => {
    if (!isSuperAdmin) return
    apiFetch<GitHubAppConfig>('/api/github-app/config')
      .then(cfg => setForm({
        app_id: cfg.app_id,
        app_name: cfg.app_name,
        installation_id: cfg.installation_id,
        client_id: cfg.client_id,
        private_key_pem: '',
        webhook_secret: '',
        client_secret: '',
      }))
      .catch(() => {})
  }

  useEffect(() => { loadStatus() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const handleSave = async () => {
    setSaving(true)
    try {
      await apiFetch('/api/github-app/config', {
        method: 'POST',
        body: JSON.stringify(form),
      })
      toast.success('GitHub App configuration saved.')
      setConfigOpen(false)
      setLoading(true)
      loadStatus()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to save config.')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    setDeleting(true)
    try {
      await apiFetch('/api/github-app/config', { method: 'DELETE' })
      toast.success('GitHub App configuration removed.')
      setStatus(s => s ? { ...s, configured: false } : s)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to remove config.')
    } finally {
      setDeleting(false)
    }
  }

  const handleLoadRepos = async () => {
    setReposLoading(true)
    try {
      const data = await apiFetch<{ repositories: { full_name: string; private: boolean }[] }>('/api/github-app/repos')
      setRepos(data.repositories ?? [])
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to list repos.')
    } finally {
      setReposLoading(false)
    }
  }

  const handleLoadDeliveries = async () => {
    setDeliveriesLoading(true)
    try {
      const data = await apiFetch<WebhookDelivery[]>('/api/github-app/webhook-deliveries')
      setDeliveries(Array.isArray(data) ? data : [])
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to fetch deliveries.')
    } finally {
      setDeliveriesLoading(false)
    }
  }

  const copyWebhookURL = () => {
    navigator.clipboard.writeText(webhookURL).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  const f = (k: keyof GitHubAppConfig) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    setForm(prev => ({ ...prev, [k]: e.target.value }))

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Use a GitHub App for organisation-wide repository access. The app is installed once and
        can access all repositories granted during installation — no per-user tokens needed.
      </p>

      {/* ── App status card ─────────────────────────────────────────── */}
      <div className="rounded-xl border border-border bg-card p-6">
        <div className="flex items-start gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl border border-border bg-muted">
            <AppWindow className="h-6 w-6" />
          </div>

          <div className="flex-1 space-y-1">
            <p className="font-medium">GitHub App</p>
            {loading ? (
              <div className="flex items-center gap-2 pt-2 text-sm text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" /> Checking…
              </div>
            ) : status?.configured ? (
              <div className="space-y-0.5 pt-1">
                <div className="flex items-center gap-2 text-sm text-emerald-600 dark:text-emerald-400">
                  <CheckCircle2 className="h-3.5 w-3.5" />
                  Configured{status.app_name ? ` — ${status.app_name}` : ''}
                </div>
                {status.installation_id && (
                  <p className="text-xs text-muted-foreground pl-5">Installation ID: {status.installation_id}</p>
                )}
              </div>
            ) : (
              <p className="pt-2 text-sm text-muted-foreground">Not configured</p>
            )}
          </div>

          {isSuperAdmin && (
            <div className="flex shrink-0 gap-2">
              {status?.configured && (
                <Button variant="outline" size="sm" onClick={handleDelete} disabled={deleting}>
                  {deleting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                </Button>
              )}
              <Dialog open={configOpen} onOpenChange={open => { setConfigOpen(open); if (open) loadConfig() }}>
                <DialogTrigger>
                  <Button size="sm" variant={status?.configured ? 'outline' : 'default'}>
                    {status?.configured ? 'Edit' : 'Configure'}
                  </Button>
                </DialogTrigger>
                <DialogContent className="max-w-lg">
                  <DialogHeader>
                    <DialogTitle>Configure GitHub App</DialogTitle>
                    <DialogDescription>
                      Enter the details from your GitHub App settings page.
                    </DialogDescription>
                  </DialogHeader>
                  <div className="space-y-3 py-2">
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-1.5">
                        <Label htmlFor="ga-app-id">App ID <span className="text-red-500">*</span></Label>
                        <Input id="ga-app-id" value={form.app_id ?? ''} onChange={f('app_id')} placeholder="123456" />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="ga-app-name">App Name</Label>
                        <Input id="ga-app-name" value={form.app_name ?? ''} onChange={f('app_name')} placeholder="My Deploy App" />
                      </div>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="ga-install-id">Installation ID <span className="text-red-500">*</span></Label>
                      <Input id="ga-install-id" value={form.installation_id ?? ''} onChange={f('installation_id')} placeholder="987654321" />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="ga-private-key">
                        Private Key (PEM) <span className="text-red-500">*</span>
                        {status?.configured && <span className="ml-1 text-xs text-muted-foreground">(leave blank to keep existing)</span>}
                      </Label>
                      <textarea
                        id="ga-private-key"
                        className="flex min-h-[120px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm font-mono shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;..."
                        value={form.private_key_pem ?? ''}
                        onChange={f('private_key_pem')}
                      />
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-1.5">
                        <Label htmlFor="ga-client-id">Client ID (optional)</Label>
                        <Input id="ga-client-id" value={form.client_id ?? ''} onChange={f('client_id')} />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="ga-client-secret">Client Secret (optional)</Label>
                        <Input id="ga-client-secret" type="password" value={form.client_secret ?? ''} onChange={f('client_secret')} />
                      </div>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="ga-webhook-secret">Webhook Secret (optional)</Label>
                      <Input id="ga-webhook-secret" type="password" value={form.webhook_secret ?? ''} onChange={f('webhook_secret')} />
                    </div>
                  </div>
                  <DialogFooter>
                    <Button variant="outline" onClick={() => setConfigOpen(false)}>Cancel</Button>
                    <Button onClick={handleSave} disabled={saving || !form.app_id || !form.installation_id}>
                      {saving ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Saving…</> : 'Save'}
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
            </div>
          )}
        </div>
      </div>

      {status?.configured && (
        <div className="rounded-xl border border-border bg-card p-6 space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-medium">Accessible Repositories</h3>
            <Button variant="outline" size="sm" onClick={handleLoadRepos} disabled={reposLoading}>
              {reposLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : 'Load repos'}
            </Button>
          </div>
          {repos.length > 0 ? (
            <ul className="divide-y divide-border text-sm">
              {repos.map(r => (
                <li key={r.full_name} className="flex items-center justify-between py-2">
                  <span className="font-mono">{r.full_name}</span>
                  {r.private && (
                    <span className="rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground">private</span>
                  )}
                </li>
              ))}
            </ul>
          ) : reposLoading ? null : (
            <p className="text-sm text-muted-foreground">Click "Load repos" to fetch the list.</p>
          )}
        </div>
      )}

      {/* ── Webhook setup guide ──────────────────────────────────────── */}
      <div className="rounded-xl border border-border bg-card p-6 space-y-4">
        <h3 className="text-sm font-medium">Webhook &amp; Auto-Deploy Setup</h3>

        <div className="space-y-1.5">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Step 1 — Set this URL in your GitHub App settings → Webhook URL</p>
          <div className="flex items-center gap-2">
            <code className="flex-1 rounded-md border border-border bg-muted px-3 py-2 text-xs font-mono break-all">
              {webhookURL}
            </code>
            <Button variant="outline" size="sm" className="shrink-0" onClick={copyWebhookURL}>
              {copied ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
            </Button>
          </div>
        </div>

        <div className="space-y-2">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Step 2 — Required GitHub App permissions</p>
          <ul className="space-y-1 text-sm">
            <li className="flex items-center gap-2 text-emerald-600 dark:text-emerald-400"><CheckCircle2 className="h-3.5 w-3.5 shrink-0" /><span><strong>Repository permissions → Contents:</strong> Read-only</span></li>
            <li className="flex items-center gap-2 text-emerald-600 dark:text-emerald-400"><CheckCircle2 className="h-3.5 w-3.5 shrink-0" /><span><strong>Repository permissions → Metadata:</strong> Read-only (mandatory)</span></li>
          </ul>
        </div>

        <div className="space-y-2">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Step 3 — Subscribe to events</p>
          <ul className="space-y-1 text-sm">
            <li className="flex items-center gap-2 text-emerald-600 dark:text-emerald-400"><CheckCircle2 className="h-3.5 w-3.5 shrink-0" /><span><strong>Push</strong> — triggers auto-deploy when you push to a branch</span></li>
          </ul>
          <p className="text-xs text-muted-foreground pt-1">In your GitHub App settings → <em>Permissions &amp; events</em> → scroll to <em>Subscribe to events</em> → tick <strong>Push</strong>.</p>
        </div>

        <div className="space-y-2">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Step 4 — Enable auto-deploy on the service</p>
          <p className="text-sm text-muted-foreground">On the service page → Overview tab → toggle <strong>Auto-deploy</strong> on and ensure <strong>Repo URL</strong> and <strong>Branch</strong> are set.</p>
        </div>
      </div>

      {/* ── Recent webhook deliveries ────────────────────────────────── */}
      {isSuperAdmin && status?.configured && (
        <div className="rounded-xl border border-border bg-card p-6 space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-medium">Recent Webhook Deliveries</h3>
              <p className="text-xs text-muted-foreground mt-0.5">Shows the last 20 events GitHub attempted to deliver — use this to diagnose why auto-deploy isn't triggering.</p>
            </div>
            <Button variant="outline" size="sm" onClick={handleLoadDeliveries} disabled={deliveriesLoading}>
              {deliveriesLoading
                ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                : <><RefreshCw className="h-3.5 w-3.5 mr-1.5" />Fetch</>}
            </Button>
          </div>

          {deliveries === null ? (
            <p className="text-sm text-muted-foreground">Click "Fetch" to load recent deliveries from GitHub.</p>
          ) : deliveries.length === 0 ? (
            <p className="text-sm text-muted-foreground">No deliveries found. Make sure the Webhook URL above is set in your GitHub App settings and at least one push event has been sent.</p>
          ) : (
            <div className="divide-y divide-border text-sm">
              {deliveries.map(d => {
                const ok = d.status_code >= 200 && d.status_code < 300
                const isPush = d.event === 'push'
                return (
                  <div key={d.id} className="flex items-center gap-3 py-2.5">
                    {ok
                      ? <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-500" />
                      : <XCircle className="h-4 w-4 shrink-0 text-red-500" />}
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className={`font-medium ${isPush ? 'text-foreground' : 'text-muted-foreground'}`}>{d.event}</span>
                        <span className={`text-xs px-1.5 py-0.5 rounded-full border ${ok ? 'border-emerald-500/30 text-emerald-600 dark:text-emerald-400 bg-emerald-500/10' : 'border-red-500/30 text-red-600 dark:text-red-400 bg-red-500/10'}`}>
                          {d.status_code}
                        </span>
                        {d.redelivery && <span className="text-xs text-muted-foreground">(redelivery)</span>}
                      </div>
                      <p className="text-xs text-muted-foreground truncate">{d.status}</p>
                    </div>
                    <div className="text-right shrink-0">
                      <p className="text-xs text-muted-foreground flex items-center gap-1">
                        <Clock className="h-3 w-3" />
                        {formatDate(d.delivered_at, timezone)}
                      </p>
                    </div>
                  </div>
                )
              })}
            </div>
          )}

          {deliveries !== null && deliveries.filter(d => d.event === 'push').length === 0 && deliveries.length > 0 && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-400">
              <strong>No push events found</strong> in the last 20 deliveries. GitHub is not sending Push events to this app.
              Check: <em>GitHub App Settings → Permissions &amp; events → Subscribe to events → Push</em> must be ticked.
            </div>
          )}
          {deliveries !== null && deliveries.some(d => d.event === 'push' && (d.status_code < 200 || d.status_code >= 300)) && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-700 dark:text-red-400">
              <strong>Push delivery failed.</strong> GitHub delivered the push event but got a non-200 response.
              Check that the Webhook URL is correct and the server is reachable.
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ─── SSH Keys Tab ───────────────────────────────────────────────────────────
function SSHKeysTab() {
  const [keys, setKeys] = useState<SSHKey[]>([])
  const [loading, setLoading] = useState(true)
  const [generateOpen, setGenerateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [newKeyName, setNewKeyName] = useState('')
  const [importName, setImportName] = useState('')
  const [importPubKey, setImportPubKey] = useState('')
  const [generatedKey, setGeneratedKey] = useState<SSHKey | null>(null)
  const [generating, setGenerating] = useState(false)
  const [importing, setImporting] = useState(false)
  const [copiedId, setCopiedId] = useState<number | null>(null)

  const load = () => {
    setLoading(true)
    apiFetch<SSHKey[]>('/api/ssh-keys')
      .then(setKeys)
      .catch(() => toast.error('Failed to load SSH keys.'))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const handleGenerate = async () => {
    setGenerating(true)
    try {
      const key = await apiFetch<SSHKey>('/api/ssh-keys/generate', {
        method: 'POST',
        body: JSON.stringify({ name: newKeyName }),
      })
      setGeneratedKey(key)
      load()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to generate key.')
    } finally {
      setGenerating(false)
    }
  }

  const handleImport = async () => {
    setImporting(true)
    try {
      await apiFetch('/api/ssh-keys/import', {
        method: 'POST',
        body: JSON.stringify({ name: importName, public_key: importPubKey }),
      })
      toast.success('SSH key imported.')
      setImportOpen(false)
      setImportName('')
      setImportPubKey('')
      load()
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to import key.')
    } finally {
      setImporting(false)
    }
  }

  const handleDelete = async (id: number) => {
    try {
      await apiFetch(`/api/ssh-keys/${id}`, { method: 'DELETE' })
      toast.success('SSH key deleted.')
      setKeys(ks => ks.filter(k => k.id !== id))
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to delete key.')
    }
  }

  const handleDownloadPrivate = async (id: number, name: string) => {
    try {
      const data = await apiFetch<{ private_key: string }>(`/api/ssh-keys/${id}/private`)
      const blob = new Blob([data.private_key], { type: 'text/plain' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${name.replace(/\s+/g, '_')}_ed25519`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Failed to export private key.')
    }
  }

  const copyToClipboard = (text: string, id: number) => {
    navigator.clipboard.writeText(text)
      .then(() => {
        setCopiedId(id)
        setTimeout(() => setCopiedId(null), 2000)
      })
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Manage ED25519 SSH keys for authenticating with GitHub deploy keys, Git over SSH, and
        other services. Add the public key to your GitHub repo under <em>Settings → Deploy keys</em>.
      </p>

      <div className="flex gap-2">
        <Dialog open={generateOpen} onOpenChange={open => { setGenerateOpen(open); if (!open) { setGeneratedKey(null); setNewKeyName('') } }}>
          <DialogTrigger>
            <Button size="sm"><Plus className="mr-1.5 h-3.5 w-3.5" />Generate key</Button>
          </DialogTrigger>
          <DialogContent className="max-w-lg">
            <DialogHeader>
              <DialogTitle>Generate SSH Key</DialogTitle>
              <DialogDescription>
                Creates a new ED25519 key pair. The private key is encrypted and stored securely.
              </DialogDescription>
            </DialogHeader>
            {!generatedKey ? (
              <>
                <div className="space-y-1.5 py-2">
                  <Label htmlFor="key-name">Key name <span className="text-red-500">*</span></Label>
                  <Input
                    id="key-name"
                    value={newKeyName}
                    onChange={e => setNewKeyName(e.target.value)}
                    placeholder="e.g. my-repo-deploy-key"
                  />
                </div>
                <DialogFooter>
                  <Button variant="outline" onClick={() => setGenerateOpen(false)}>Cancel</Button>
                  <Button onClick={handleGenerate} disabled={generating || !newKeyName.trim()}>
                    {generating ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Generating…</> : 'Generate'}
                  </Button>
                </DialogFooter>
              </>
            ) : (
              <>
                <div className="space-y-3 py-2">
                  <div className="flex items-center gap-2 text-sm text-emerald-600 dark:text-emerald-400">
                    <CheckCircle2 className="h-4 w-4" /> Key generated successfully
                  </div>
                  <div className="space-y-1.5">
                    <Label>Public key (add to GitHub)</Label>
                    <div className="relative">
                      <textarea
                        readOnly
                        className="flex min-h-[80px] w-full rounded-md border border-input bg-muted px-3 py-2 pr-10 text-xs font-mono"
                        value={generatedKey.public_key}
                      />
                      <button
                        className="absolute right-2 top-2 text-muted-foreground hover:text-foreground"
                        onClick={() => copyToClipboard(generatedKey.public_key, -1)}
                      >
                        {copiedId === -1 ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                      </button>
                    </div>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Fingerprint: <code className="font-mono">{generatedKey.fingerprint}</code>
                  </p>
                </div>
                <DialogFooter>
                  <Button onClick={() => setGenerateOpen(false)}>Done</Button>
                </DialogFooter>
              </>
            )}
          </DialogContent>
        </Dialog>

        <Dialog open={importOpen} onOpenChange={setImportOpen}>
          <DialogTrigger>
            <Button size="sm" variant="outline"><Key className="mr-1.5 h-3.5 w-3.5" />Import public key</Button>
          </DialogTrigger>
          <DialogContent className="max-w-lg">
            <DialogHeader>
              <DialogTitle>Import Public Key</DialogTitle>
              <DialogDescription>
                Import an existing SSH public key. The private key is not stored.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-3 py-2">
              <div className="space-y-1.5">
                <Label htmlFor="import-name">Key name <span className="text-red-500">*</span></Label>
                <Input id="import-name" value={importName} onChange={e => setImportName(e.target.value)} placeholder="e.g. my-laptop" />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="import-pubkey">Public key <span className="text-red-500">*</span></Label>
                <textarea
                  id="import-pubkey"
                  className="flex min-h-[80px] w-full rounded-md border border-input bg-background px-3 py-2 text-xs font-mono shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  placeholder="ssh-ed25519 AAAA... user@host"
                  value={importPubKey}
                  onChange={e => setImportPubKey(e.target.value)}
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setImportOpen(false)}>Cancel</Button>
              <Button onClick={handleImport} disabled={importing || !importName.trim() || !importPubKey.trim()}>
                {importing ? <><Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />Importing…</> : 'Import'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {loading ? (
        <div className="flex items-center gap-2 py-8 justify-center text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading keys…
        </div>
      ) : keys.length === 0 ? (
        <div className="rounded-xl border border-dashed border-border bg-card/50 p-10 text-center">
          <Key className="mx-auto h-8 w-8 text-muted-foreground/50 mb-3" />
          <p className="text-sm text-muted-foreground">No SSH keys yet. Generate or import one above.</p>
        </div>
      ) : (
        <div className="rounded-xl border border-border bg-card divide-y divide-border">
          {keys.map(k => (
            <div key={k.id} className="flex items-center gap-4 px-5 py-3">
              <Key className="h-4 w-4 shrink-0 text-muted-foreground" />
              <div className="flex-1 min-w-0">
                <p className="font-medium text-sm truncate">{k.name}</p>
                <p className="text-xs text-muted-foreground font-mono truncate">{k.fingerprint}</p>
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <button
                  className="text-muted-foreground hover:text-foreground p-1"
                  title="Copy public key"
                  onClick={() => copyToClipboard(k.public_key, k.id)}
                >
                  {copiedId === k.id ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
                </button>
                {k.has_private && (
                  <button
                    className="text-muted-foreground hover:text-foreground p-1"
                    title="Download private key"
                    onClick={() => handleDownloadPrivate(k.id, k.name)}
                  >
                    <Download className="h-3.5 w-3.5" />
                  </button>
                )}
                <button
                  className="text-muted-foreground hover:text-destructive p-1"
                  title="Delete key"
                  onClick={() => handleDelete(k.id)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ─── Main Page ───────────────────────────────────────────────────────────────
export function GitHubSettingsPage() {
  const { user } = useAuth()
  const isSuperAdmin = user?.role === 'superadmin'

  return (
    <div className="space-y-6 max-w-3xl">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">GitHub Integration</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Connect GitHub via OAuth, a GitHub App, or manage SSH deploy keys.
        </p>
      </div>

      <Tabs defaultValue="oauth">
        <TabsList className="mb-4">
          <TabsTrigger value="oauth">OAuth App</TabsTrigger>
          <TabsTrigger value="github-app">GitHub App</TabsTrigger>
          <TabsTrigger value="ssh-keys">SSH Keys</TabsTrigger>
        </TabsList>

        <TabsContent value="oauth">
          <OAuthTab />
        </TabsContent>

        <TabsContent value="github-app">
          <GitHubAppTab isSuperAdmin={isSuperAdmin} />
        </TabsContent>

        <TabsContent value="ssh-keys">
          <SSHKeysTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}
