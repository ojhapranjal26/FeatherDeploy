import { useState, useRef, useEffect, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Monitor, Smartphone, Tablet, LogOut, ShieldOff, QrCode, X, Camera, CameraOff } from 'lucide-react'
import { toast } from 'sonner'
import { sessionsApi, type Session } from '@/api/auth'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '@/context/AuthContext'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDateFull } from '@/lib/dateFormat'

// ── Tiny user-agent parser ────────────────────────────────────────────────────

function parseDevice(ua: string): { label: string; icon: React.ElementType } {
  const u = ua.toLowerCase()
  if (u.includes('iphone') || u.includes('android') && u.includes('mobile')) {
    return { label: 'Mobile', icon: Smartphone }
  }
  if (u.includes('ipad') || u.includes('tablet')) {
    return { label: 'Tablet', icon: Tablet }
  }
  return { label: 'Desktop', icon: Monitor }
}

function parseBrowser(ua: string): string {
  if (ua.includes('Edg/')) return 'Edge'
  if (ua.includes('OPR/') || ua.includes('Opera')) return 'Opera'
  if (ua.includes('Chrome')) return 'Chrome'
  if (ua.includes('Firefox')) return 'Firefox'
  if (ua.includes('Safari')) return 'Safari'
  return 'Browser'
}

function parseOS(ua: string): string {
  if (ua.includes('Windows')) return 'Windows'
  if (ua.includes('iPhone') || ua.includes('iPad')) return 'iOS'
  if (ua.includes('Android')) return 'Android'
  if (ua.includes('Mac OS')) return 'macOS'
  if (ua.includes('Linux')) return 'Linux'
  return 'Unknown OS'
}

function deviceLabel(ua: string): string {
  if (!ua) return 'Unknown device'
  return `${parseBrowser(ua)} on ${parseOS(ua)}`
}

function fmtDate(s: string, tz: string): string {
  return formatDateFull(s, tz)
}

// ── Camera QR Scanner ─────────────────────────────────────────────────────────

/** Extract the qr-approve token from a raw QR value (full URL or bare token). */
function extractToken(raw: string): string | null {
  const m = raw.match(/qr-approve\/([a-f0-9]{64})/i)
  if (m) return m[1]
  if (/^[a-f0-9]{64}$/i.test(raw.trim())) return raw.trim()
  return null
}

const hasBarcodeDetector =
  typeof window !== 'undefined' && 'BarcodeDetector' in window

interface QRScannerProps {
  onDetect: (token: string) => void
}

function QRCameraScanner({ onDetect }: QRScannerProps) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const [camError, setCamError] = useState<string | null>(null)
  const [active, setActive] = useState(false)

  const handleDetect = useCallback(
    (token: string) => onDetect(token),
    [onDetect],
  )

  useEffect(() => {
    if (!hasBarcodeDetector) return

    let stream: MediaStream | null = null
    let rafId: number
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let detector: any

    async function start() {
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          video: { facingMode: { ideal: 'environment' }, width: { ideal: 640 } },
        })
        if (!videoRef.current) return
        videoRef.current.srcObject = stream
        await videoRef.current.play()
        // @ts-expect-error BarcodeDetector is not in TS lib yet
        detector = new BarcodeDetector({ formats: ['qr_code'] })
        setActive(true)
      } catch {
        setCamError('Camera access denied. Grant permission and try again.')
        return
      }

      async function scanFrame() {
        if (!videoRef.current || videoRef.current.readyState < 2) {
          rafId = requestAnimationFrame(scanFrame)
          return
        }
        try {
          const barcodes = await detector.detect(videoRef.current)
          if (barcodes.length > 0) {
            const token = extractToken(barcodes[0].rawValue)
            if (token) {
              handleDetect(token)
              return // stop scanning after first detection
            }
          }
        } catch {
          // ignore per-frame detection errors
        }
        rafId = requestAnimationFrame(scanFrame)
      }
      scanFrame()
    }

    start()

    return () => {
      cancelAnimationFrame(rafId)
      stream?.getTracks().forEach((t) => t.stop())
    }
  }, [handleDetect])

  if (!hasBarcodeDetector) return null

  return (
    <div className="space-y-2">
      <div className="relative mx-auto aspect-square max-w-[260px] overflow-hidden rounded-xl bg-black">
        <video
          ref={videoRef}
          className="h-full w-full object-cover"
          muted
          playsInline
        />
        {active && (
          /* scanning overlay — animated corner brackets */
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
            <div className="relative h-40 w-40">
              {/* top-left */}
              <span className="absolute left-0 top-0 h-6 w-6 border-l-2 border-t-2 border-white/80 rounded-tl" />
              {/* top-right */}
              <span className="absolute right-0 top-0 h-6 w-6 border-r-2 border-t-2 border-white/80 rounded-tr" />
              {/* bottom-left */}
              <span className="absolute bottom-0 left-0 h-6 w-6 border-b-2 border-l-2 border-white/80 rounded-bl" />
              {/* bottom-right */}
              <span className="absolute bottom-0 right-0 h-6 w-6 border-b-2 border-r-2 border-white/80 rounded-br" />
            </div>
          </div>
        )}
        {!active && !camError && (
          <div className="absolute inset-0 flex items-center justify-center">
            <Camera className="h-8 w-8 animate-pulse text-white/60" />
          </div>
        )}
      </div>
      {camError ? (
        <p className="text-center text-xs text-destructive">{camError}</p>
      ) : active ? (
        <p className="text-center text-xs text-muted-foreground">
          Point your camera at the QR code on the login page
        </p>
      ) : null}
    </div>
  )
}

// ── Approve Dialog ────────────────────────────────────────────────────────────

interface QRDialogProps {
  open: boolean
  onClose: () => void
}

function QRApproveDialog({ open, onClose }: QRDialogProps) {
  const navigate = useNavigate()
  const [manualToken, setManualToken] = useState('')

  const handleToken = useCallback(
    (token: string) => {
      onClose()
      navigate(`/qr-approve/${token}`)
    },
    [onClose, navigate],
  )

  const handleManualSubmit = () => {
    const raw = manualToken.trim()
    const token = extractToken(raw) ?? raw
    if (token.length !== 64) {
      toast.error('Invalid token — paste the full URL or 64-char hex token')
      return
    }
    handleToken(token)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Camera className="h-5 w-5" />
            Approve another device
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-1">
          <p className="text-sm text-muted-foreground">
            On the device you want to log in, open the login page and show the QR
            code. Then scan it below or paste the token.
          </p>

          {hasBarcodeDetector ? (
            <>
              <QRCameraScanner onDetect={handleToken} />
              <div className="relative flex items-center gap-2 pt-1">
                <div className="flex-1 border-t" />
                <span className="shrink-0 text-xs text-muted-foreground">or paste manually</span>
                <div className="flex-1 border-t" />
              </div>
            </>
          ) : (
            <div className="flex items-center gap-2 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
              <CameraOff className="h-4 w-4 shrink-0" />
              Camera scanner not supported in this browser. Paste the URL or token below.
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="qr-token">QR token / URL from the login page</Label>
            <div className="flex gap-2">
              <Input
                id="qr-token"
                placeholder="https://…/qr-approve/abc123…"
                value={manualToken}
                onChange={(e) => setManualToken(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleManualSubmit()}
              />
              <Button onClick={handleManualSubmit} disabled={!manualToken.trim()}>
                <QrCode className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function DevicesPage() {
  const qc = useQueryClient()
  const { logout } = useAuth()
  const navigate = useNavigate()
  const { timezone } = useTimezone()
  const [qrOpen, setQrOpen] = useState(false)

  const { data: sessions = [], isLoading } = useQuery({
    queryKey: ['sessions'],
    queryFn: sessionsApi.list,
    refetchInterval: 30_000,
  })

  const revokeMut = useMutation({
    mutationFn: sessionsApi.revoke,
    onSuccess: () => {
      toast.success('Device logged out')
      qc.invalidateQueries({ queryKey: ['sessions'] })
    },
    onError: () => toast.error('Failed to revoke session'),
  })

  const revokeOthersMut = useMutation({
    mutationFn: sessionsApi.revokeOthers,
    onSuccess: () => {
      toast.success('All other devices logged out')
      qc.invalidateQueries({ queryKey: ['sessions'] })
    },
    onError: () => toast.error('Failed to revoke sessions'),
  })

  const handleLogoutCurrent = async () => {
    await logout()
    navigate('/login')
  }

  const otherSessions = sessions.filter((s) => !s.is_current)

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Devices</h1>
          <p className="text-sm text-muted-foreground">
            Manage your active login sessions
          </p>
        </div>
        <Button variant="outline" onClick={() => setQrOpen(true)}>
          <QrCode className="mr-2 h-4 w-4" />
          Approve device
        </Button>
      </div>

      {/* Action bar */}
      {otherSessions.length > 0 && (
        <div className="flex justify-end">
          <Button
            variant="destructive"
            size="sm"
            onClick={() => revokeOthersMut.mutate()}
            disabled={revokeOthersMut.isPending}
          >
            <ShieldOff className="mr-2 h-4 w-4" />
            Logout all other devices
          </Button>
        </div>
      )}

      {/* Session list */}
      <div className="space-y-3">
        {isLoading ? (
          Array.from({ length: 2 }).map((_, i) => (
            <div key={i} className="h-24 animate-pulse rounded-lg bg-muted" />
          ))
        ) : sessions.length === 0 ? (
          <Card>
            <CardContent className="py-8 text-center text-sm text-muted-foreground">
              No active sessions found
            </CardContent>
          </Card>
        ) : (
          sessions.map((session: Session) => {
            const { icon: DeviceIcon, label } = parseDevice(session.user_agent)
            return (
              <Card key={session.id} className={session.is_current ? 'border-primary/40' : ''}>
                <CardHeader className="pb-2 pt-4">
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex items-center gap-3">
                      <DeviceIcon className="h-5 w-5 text-muted-foreground shrink-0" />
                      <div>
                        <CardTitle className="text-base font-medium flex items-center gap-2">
                          {deviceLabel(session.user_agent)}
                          {session.is_current && (
                            <Badge variant="secondary" className="text-xs">Current</Badge>
                          )}
                        </CardTitle>
                        <CardDescription className="text-xs mt-0.5">
                          {label} · {session.ip_address || 'unknown IP'}
                        </CardDescription>
                      </div>
                    </div>

                    {session.is_current ? (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={handleLogoutCurrent}
                      >
                        <LogOut className="mr-1 h-3.5 w-3.5" />
                        Logout
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => revokeMut.mutate(session.id)}
                        disabled={revokeMut.isPending}
                      >
                        <X className="mr-1 h-3.5 w-3.5" />
                        Revoke
                      </Button>
                    )}
                  </div>
                </CardHeader>
                <CardContent className="pb-3 pt-0">
                  <div className="flex gap-4 text-xs text-muted-foreground">
                    <span>Signed in {fmtDate(session.created_at, timezone)}</span>
                    <span>·</span>
                    <span>Last seen {fmtDate(session.last_seen, timezone)}</span>
                  </div>
                </CardContent>
              </Card>
            )
          })
        )}
      </div>

      <QRApproveDialog open={qrOpen} onClose={() => setQrOpen(false)} />
    </div>
  )
}
