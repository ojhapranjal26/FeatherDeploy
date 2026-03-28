import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Feather, CheckCircle2, XCircle, Loader2, Smartphone } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { qrApi } from '@/api/auth'
import type { QRStatusResponse } from '@/api/auth'
import { useAuth } from '@/context/AuthContext'
import { settingsApi, type Branding } from '@/api/settings'

export function QRLoginPage() {
  const { token } = useParams<{ token: string }>()
  const navigate = useNavigate()
  const { loginWithToken } = useAuth()

  const [branding, setBranding] = useState<Branding>({ company_name: '', logo_url: '' })
  const [info, setInfo] = useState<QRStatusResponse | null>(null)
  const [pageStatus, setPageStatus] = useState<'loading' | 'ready' | 'claiming' | 'success' | 'expired' | 'error'>('loading')
  const [ttl, setTtl] = useState<number | null>(null)

  useEffect(() => {
    settingsApi.getBranding().then(setBranding).catch(() => {})
  }, [])

  useEffect(() => {
    if (!token) { setPageStatus('error'); return }
    qrApi.status(token).then((res) => {
      setInfo(res)
      if (res.status === 'expired')       setPageStatus('expired')
      else if (res.status === 'claimed')  setPageStatus('error')
      else                               setPageStatus('ready')
    }).catch(() => setPageStatus('error'))
  }, [token])

  const handleClaim = async () => {
    if (!token) return
    setPageStatus('claiming')
    try {
      const data = await qrApi.claim(token)
      setTtl(data.ttl_minutes)
      loginWithToken(data.token, data.user)
      setPageStatus('success')
      setTimeout(() => navigate('/dashboard'), 2000)
    } catch (err: unknown) {
      const status = (err as { response?: { status?: number } })?.response?.status
      setPageStatus(status === 410 || status === 409 ? 'expired' : 'error')
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
          {/* ── Loading ── */}
          {pageStatus === 'loading' && (
            <div className="flex flex-col items-center gap-3 py-4">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
              <p className="text-sm text-muted-foreground">Verifying QR code…</p>
            </div>
          )}

          {/* ── Ready to claim ── */}
          {pageStatus === 'ready' && info && (
            <>
              <div className="flex flex-col items-center gap-1 text-center">
                <div className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10 mb-1">
                  <Smartphone className="h-7 w-7 text-primary" />
                </div>
                <h1 className="text-xl font-bold">Authorize this device</h1>
                <p className="text-sm text-muted-foreground mt-1">
                  You are about to log in as:
                </p>
              </div>

              <div className="rounded-xl bg-muted/50 border border-border/50 px-4 py-3 space-y-0.5">
                <p className="font-semibold text-sm">{info.user_name}</p>
                <p className="text-xs text-muted-foreground">{info.user_email}</p>
              </div>

              <p className="text-xs text-muted-foreground text-center">
                Session expires automatically — this is a temporary login for this device only.
              </p>

              <Button className="w-full h-10 font-medium gap-2" onClick={handleClaim}>
                <CheckCircle2 className="h-4 w-4" />
                Authorize Login
              </Button>
            </>
          )}

          {/* ── Claiming ── */}
          {pageStatus === 'claiming' && (
            <div className="flex flex-col items-center gap-3 py-4">
              <Loader2 className="h-8 w-8 animate-spin text-primary" />
              <p className="text-sm text-muted-foreground">Signing you in…</p>
            </div>
          )}

          {/* ── Success ── */}
          {pageStatus === 'success' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <CheckCircle2 className="h-12 w-12 text-emerald-500" />
              <div>
                <p className="font-semibold">Logged in!</p>
                <p className="text-xs text-muted-foreground mt-1">
                  Session valid for {ttl} minute{ttl !== 1 ? 's' : ''}.
                  Redirecting to dashboard…
                </p>
              </div>
            </div>
          )}

          {/* ── Expired ── */}
          {pageStatus === 'expired' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <XCircle className="h-12 w-12 text-amber-500" />
              <div>
                <p className="font-semibold">QR code expired</p>
                <p className="text-xs text-muted-foreground mt-1">
                  Please go back to your dashboard and generate a new QR code.
                </p>
              </div>
            </div>
          )}

          {/* ── Generic error ── */}
          {pageStatus === 'error' && (
            <div className="flex flex-col items-center gap-3 py-4 text-center">
              <XCircle className="h-12 w-12 text-destructive" />
              <div>
                <p className="font-semibold">Invalid or already used</p>
                <p className="text-xs text-muted-foreground mt-1">
                  This QR code is no longer valid. Ask the account owner to generate a new one.
                </p>
              </div>
            </div>
          )}
        </div>

        <p className="text-center text-xs text-muted-foreground">
          This session was initiated from a trusted device.
        </p>
      </div>
    </div>
  )
}
