import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Feather, CheckCircle2, XCircle, Loader2, Smartphone } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { qrApi } from '@/api/auth'
import { useAuth } from '@/context/AuthContext'
import { settingsApi, type Branding } from '@/api/settings'

type PageStatus = 'approving' | 'success' | 'conflict' | 'expired' | 'error'

/** /qr-approve/:token — authenticated device sees this page and approves the login. */
export function QRApprovePage() {
  const { token } = useParams<{ token: string }>()
  const navigate = useNavigate()
  const { user } = useAuth()

  const [branding, setBranding] = useState<Branding>({ company_name: '', logo_url: '' })
  const [pageStatus, setPageStatus] = useState<PageStatus>('approving')

  useEffect(() => {
    settingsApi.getBranding().then(setBranding).catch(() => {})
  }, [])

  const handleApprove = async () => {
    if (!token) return
    setPageStatus('approving')
    try {
      await qrApi.approve(token)
      setPageStatus('success')
    } catch (err: unknown) {
      const status = (err as { response?: { status?: number } })?.response?.status
      if (status === 409) setPageStatus('conflict')
      else if (status === 410 || status === 404) setPageStatus('expired')
      else setPageStatus('error')
    }
  }

  const platformName = branding.company_name || 'FeatherDeploy'

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-6">
      <div className="w-full max-w-sm space-y-6">
        {/* Logo */}
        <div className="flex flex-col items-center gap-3 mb-2">
          {branding.logo_url ? (
            <img src={branding.logo_url} alt={platformName} className="h-12 w-auto object-contain" />
          ) : (
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-lg shadow-primary/30">
              <Feather className="h-6 w-6" />
            </div>
          )}
          <span className="text-lg font-bold tracking-tight">{platformName}</span>
        </div>

        {/* Card */}
        <div className="rounded-2xl border border-border/60 bg-card shadow-xl p-7 space-y-5">

          {/* ── Pending approval ── */}
          {pageStatus === 'approving' && (
            <>
              <div className="flex flex-col items-center gap-1 text-center">
                <div className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10 mb-1">
                  <Smartphone className="h-7 w-7 text-primary" />
                </div>
                <h1 className="text-xl font-bold">Approve device login</h1>
                <p className="text-sm text-muted-foreground mt-1">
                  A device is waiting to log in as:
                </p>
              </div>

              <div className="rounded-xl bg-muted/50 border border-border/50 px-4 py-3 space-y-0.5">
                <p className="font-semibold text-sm">{user?.name}</p>
                <p className="text-xs text-muted-foreground">{user?.email}</p>
              </div>

              <p className="text-xs text-muted-foreground text-center">
                Only approve if you initiated this login. The session lasts up to 1 hour.
              </p>

              <div className="flex gap-2">
                <Button variant="outline" className="flex-1" onClick={() => navigate('/dashboard')}>
                  Cancel
                </Button>
                <Button className="flex-1 gap-2" onClick={handleApprove}>
                  <CheckCircle2 className="h-4 w-4" />
                  Approve Login
                </Button>
              </div>
            </>
          )}

          {/* ── Success ── */}
          {pageStatus === 'success' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <CheckCircle2 className="h-12 w-12 text-emerald-500" />
              <div>
                <p className="font-semibold">Login approved!</p>
                <p className="text-xs text-muted-foreground mt-1">
                  The other device is now logging in.
                </p>
              </div>
              <Button variant="outline" size="sm" onClick={() => navigate('/dashboard')}>
                Back to dashboard
              </Button>
            </div>
          )}

          {/* ── Already approved ── */}
          {pageStatus === 'conflict' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <CheckCircle2 className="h-12 w-12 text-muted-foreground" />
              <div>
                <p className="font-semibold">Already approved</p>
                <p className="text-xs text-muted-foreground mt-1">
                  This QR login has already been approved.
                </p>
              </div>
              <Button variant="outline" size="sm" onClick={() => navigate('/dashboard')}>
                Back to dashboard
              </Button>
            </div>
          )}

          {/* ── Expired ── */}
          {pageStatus === 'expired' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <XCircle className="h-12 w-12 text-amber-500" />
              <div>
                <p className="font-semibold">QR code expired</p>
                <p className="text-xs text-muted-foreground mt-1">
                  The QR code has expired. Ask the other device to refresh it.
                </p>
              </div>
              <Button variant="outline" size="sm" onClick={() => navigate('/dashboard')}>
                Back to dashboard
              </Button>
            </div>
          )}

          {/* ── Error ── */}
          {pageStatus === 'error' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <XCircle className="h-12 w-12 text-destructive" />
              <div>
                <p className="font-semibold">Something went wrong</p>
                <p className="text-xs text-muted-foreground mt-1">
                  Could not approve the login. Please try again.
                </p>
              </div>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => navigate('/dashboard')}>
                  Cancel
                </Button>
                <Button size="sm" onClick={() => setPageStatus('approving')}>
                  <Loader2 className="h-3.5 w-3.5 mr-1.5" />
                  Retry
                </Button>
              </div>
            </div>
          )}
        </div>

        <p className="text-center text-xs text-muted-foreground">
          You are logged in as <span className="font-medium text-foreground">{user?.email}</span>
        </p>
      </div>
    </div>
  )
}
