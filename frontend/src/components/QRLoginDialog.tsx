import { useState, useEffect, useCallback, useRef } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import { Smartphone, RefreshCw, CheckCircle2, Clock, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { qrApi } from '@/api/auth'
import type { QRGenerateResponse } from '@/api/auth'

interface Props {
  open: boolean
  onClose: () => void
}

const TTL_OPTIONS = [
  { label: '15 min', value: 15 },
  { label: '30 min', value: 30 },
  { label: '1 hour', value: 60 },
]

type ModalStatus = 'configure' | 'showing' | 'claimed' | 'expired'

export function QRLoginDialog({ open, onClose }: Props) {
  const [ttl, setTtl] = useState(60)
  const [status, setStatus] = useState<ModalStatus>('configure')
  const [qrData, setQrData] = useState<QRGenerateResponse | null>(null)
  const [secondsLeft, setSecondsLeft] = useState(0)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const stopPolling = useCallback(() => {
    if (pollRef.current)     { clearInterval(pollRef.current);     pollRef.current = null }
    if (countdownRef.current){ clearInterval(countdownRef.current); countdownRef.current = null }
  }, [])

  // Reset when dialog opens/closes
  useEffect(() => {
    if (!open) {
      stopPolling()
      setStatus('configure')
      setQrData(null)
    }
  }, [open, stopPolling])

  const generate = async () => {
    stopPolling()
    setStatus('showing')
    try {
      const data = await qrApi.generate(ttl)
      setQrData(data)

      // Countdown from QR expiry (~5 min)
      const remaining = Math.max(0, Math.floor((data.expires_at - Date.now()) / 1000))
      setSecondsLeft(remaining)
      countdownRef.current = setInterval(() => {
        setSecondsLeft((s) => {
          if (s <= 1) {
            stopPolling()
            setStatus('expired')
            return 0
          }
          return s - 1
        })
      }, 1000)

      // Poll status every 2 s
      pollRef.current = setInterval(async () => {
        try {
          const res = await qrApi.status(data.qr_token)
          if (res.status === 'claimed') {
            stopPolling()
            setStatus('claimed')
          } else if (res.status === 'expired') {
            stopPolling()
            setStatus('expired')
          }
        } catch {
          // network blip — keep polling
        }
      }, 2000)
    } catch {
      setStatus('configure')
    }
  }

  const reset = () => {
    stopPolling()
    setStatus('configure')
    setQrData(null)
  }

  const qrUrl = qrData ? `${window.location.origin}/qr-login/${qrData.qr_token}` : ''

  const fmtSeconds = (s: number) => {
    const m = Math.floor(s / 60)
    const sec = s % 60
    return `${m}:${String(sec).padStart(2, '0')}`
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) { stopPolling(); onClose() } }}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Smartphone className="h-4 w-4 text-muted-foreground" />
            Login on another device
          </DialogTitle>
        </DialogHeader>

        {/* ── Configure ──────────────────────────────────────────────── */}
        {status === 'configure' && (
          <div className="space-y-5 pt-1">
            <p className="text-sm text-muted-foreground">
              Generate a QR code and scan it with another device. The session
              expires automatically — max 1 hour.
            </p>

            <div className="space-y-2">
              <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Session duration</p>
              <div className="flex gap-2">
                {TTL_OPTIONS.map((o) => (
                  <button
                    key={o.value}
                    onClick={() => setTtl(o.value)}
                    className={`flex-1 rounded-lg border px-3 py-2 text-sm font-medium transition-colors ${
                      ttl === o.value
                        ? 'border-primary bg-primary/10 text-primary'
                        : 'border-border hover:bg-muted text-muted-foreground'
                    }`}
                  >
                    {o.label}
                  </button>
                ))}
              </div>
            </div>

            <Button className="w-full" onClick={generate}>
              Generate QR Code
            </Button>
          </div>
        )}

        {/* ── Showing QR ─────────────────────────────────────────────── */}
        {status === 'showing' && qrData && (
          <div className="space-y-4 pt-1">
            <p className="text-sm text-muted-foreground text-center">
              Scan with the other device's camera and tap{' '}
              <span className="font-medium text-foreground">Authorize Login</span>.
            </p>

            <div className="flex justify-center">
              <div className="rounded-2xl border-2 border-border p-3 bg-white dark:bg-white">
                <QRCodeSVG
                  value={qrUrl}
                  size={180}
                  bgColor="#ffffff"
                  fgColor="#0f172a"
                  level="M"
                />
              </div>
            </div>

            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <span className="inline-block h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
                Waiting for scan…
              </span>
              <span className="flex items-center gap-1">
                <Clock className="h-3 w-3" />
                {fmtSeconds(secondsLeft)}
              </span>
            </div>

            <p className="text-xs text-muted-foreground text-center">
              Session valid for <span className="font-medium text-foreground">{ttl} min</span> after login.
            </p>

            <div className="flex gap-2">
              <Button variant="outline" size="sm" className="flex-1 gap-1.5" onClick={reset}>
                <RefreshCw className="h-3.5 w-3.5" /> Regenerate
              </Button>
              <Button variant="ghost" size="sm" className="flex-1 gap-1.5" onClick={() => { stopPolling(); onClose() }}>
                <X className="h-3.5 w-3.5" /> Close
              </Button>
            </div>
          </div>
        )}

        {/* ── Claimed / success ──────────────────────────────────────── */}
        {status === 'claimed' && (
          <div className="flex flex-col items-center gap-3 py-6 text-center">
            <CheckCircle2 className="h-12 w-12 text-emerald-500" />
            <div>
              <p className="font-semibold">Device authorized!</p>
              <p className="text-xs text-muted-foreground mt-1">
                The other device is now logged in for {ttl} minute{ttl !== 1 ? 's' : ''}.
              </p>
            </div>
            <Button variant="outline" size="sm" onClick={reset}>Generate another</Button>
          </div>
        )}

        {/* ── Expired ────────────────────────────────────────────────── */}
        {status === 'expired' && (
          <div className="flex flex-col items-center gap-3 py-6 text-center">
            <Clock className="h-12 w-12 text-amber-500" />
            <div>
              <p className="font-semibold">QR code expired</p>
              <p className="text-xs text-muted-foreground mt-1">
                The code wasn't scanned in time. Generate a new one.
              </p>
            </div>
            <Button size="sm" onClick={reset}>Generate new QR</Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
