import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Monitor, Smartphone, Tablet, LogOut, ShieldOff, QrCode, X } from 'lucide-react'
import { toast } from 'sonner'
import { sessionsApi, type Session } from '@/api/auth'
import { qrApi } from '@/api/auth'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { QRCodeSVG } from 'qrcode.react'
import { useAuth } from '@/context/AuthContext'

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

function fmtDate(s: string): string {
  const d = new Date(s.includes('T') ? s : s + 'Z')
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

// ── QR Approve Dialog ─────────────────────────────────────────────────────────

interface QRDialogProps {
  open: boolean
  onClose: () => void
}

function QRApproveDialog({ open, onClose }: QRDialogProps) {
  const navigate = useNavigate()
  const [manualToken, setManualToken] = useState('')

  // Also generate a QR code for devices to scan immediately
  const { data: qrData, isLoading: qrLoading } = useQuery({
    queryKey: ['qr-init-devices'],
    queryFn: qrApi.init,
    enabled: open,
    staleTime: 0,
  })

  const approveUrl = qrData
    ? `${window.location.origin}/qr-approve/${qrData.qr_token}`
    : ''

  const handleManualApprove = () => {
    const raw = manualToken.trim()
    // Accept either the full URL or just the token
    const tokenMatch = raw.match(/qr-approve\/([a-f0-9]{64})/i) ?? raw.match(/^([a-f0-9]{64})$/i)
    const token = tokenMatch ? tokenMatch[1] : raw
    if (!token) {
      toast.error('Please enter a valid token or URL')
      return
    }
    navigate(`/qr-approve/${token}`)
    onClose()
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>Approve another device</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Show QR code that a new device's browser can scan to get the approve URL */}
          <div className="flex flex-col items-center gap-2">
            <p className="text-sm text-muted-foreground text-center">
              Scan this code on a new device to approve its login
            </p>
            {qrLoading ? (
              <div className="h-40 w-40 animate-pulse rounded bg-muted" />
            ) : approveUrl ? (
              <QRCodeSVG value={approveUrl} size={160} className="rounded" />
            ) : null}
          </div>

          <div className="relative flex items-center gap-2">
            <div className="flex-1 border-t" />
            <span className="text-xs text-muted-foreground">or paste a token</span>
            <div className="flex-1 border-t" />
          </div>

          <div className="space-y-1">
            <Label htmlFor="qr-token">QR token or URL from the login page</Label>
            <Input
              id="qr-token"
              placeholder="Paste token/URL here…"
              value={manualToken}
              onChange={(e) => setManualToken(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleManualApprove()}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            <X className="mr-1 h-4 w-4" />
            Cancel
          </Button>
          <Button onClick={handleManualApprove}>
            <QrCode className="mr-1 h-4 w-4" />
            Go to approve
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function DevicesPage() {
  const qc = useQueryClient()
  const { logout } = useAuth()
  const navigate = useNavigate()
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
                    <span>Signed in {fmtDate(session.created_at)}</span>
                    <span>·</span>
                    <span>Last seen {fmtDate(session.last_seen)}</span>
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
