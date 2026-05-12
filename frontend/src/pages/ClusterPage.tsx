import { useRef, useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Crown, Cpu, MemoryStick, HardDrive, Server, Wifi, WifiOff,
  CheckCircle2, XCircle, Loader2, ScrollText, X, ArrowLeft,
} from 'lucide-react'
import {
  AreaChart, Area, ResponsiveContainer, Tooltip, XAxis,
} from 'recharts'
import { useStatsSSE, type NodeStats } from '@/hooks/useStatsSSE'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { useTimezone } from '@/context/TimezoneContext'

// ── Helpers ───────────────────────────────────────────────────────────────────
const gb = (b: number) => (b / 1073741824).toFixed(1)
const pct = (used: number, total: number) => total > 0 ? Math.round((used / total) * 100) : 0

function barColor(v: number) {
  if (v > 85) return '[&>div]:bg-red-500'
  if (v > 65) return '[&>div]:bg-amber-400'
  return '[&>div]:bg-emerald-500'
}

type CpuPoint = { t: string; v: number }
type RamPoint = { t: string; v: number }

function Sparkline({ data, color = '#6366f1' }: { data: CpuPoint[]; color?: string }) {
  if (data.length < 2) return null
  const id = `spk${color.replace('#', '')}`
  return (
    <ResponsiveContainer width="100%" height={44}>
      <AreaChart data={data} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
        <defs>
          <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity={0.4} />
            <stop offset="100%" stopColor={color} stopOpacity={0.04} />
          </linearGradient>
        </defs>
        <XAxis dataKey="t" hide />
        <Tooltip
          contentStyle={{ fontSize: 11, padding: '2px 8px', borderRadius: 6 }}
          formatter={(v: unknown) => [`${Number(v).toFixed(1)}%`, 'CPU']}
          labelFormatter={() => ''}
        />
        <Area type="monotone" dataKey="v" stroke={color} strokeWidth={1.5}
          fill={`url(#${id})`} dot={false} isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  )
}

// ── Log Viewer ────────────────────────────────────────────────────────────────
function LogViewer({ nodeId, nodeName, onClose }: {
  nodeId: string | number; nodeName: string; onClose: () => void
}) {
  const [lines, setLines] = useState<string[]>([])
  const [error, setError] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const token = localStorage.getItem('token') || ''
    const url = `/api/nodes/${nodeId}/logs?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef.current = es
    es.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)
        if (data.error) { setError(data.error); return }
        if (data.line) setLines(prev => [...prev.slice(-600), data.line])
      } catch { /* ignore */ }
    }
    es.onerror = () => setError('Connection lost. The node may be offline or disconnected from the tunnel.')
    return () => { es.close() }
  }, [nodeId])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lines])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-3 sm:p-6">
      <div className="relative w-full max-w-5xl bg-zinc-950 border border-zinc-800 rounded-2xl shadow-2xl flex flex-col" style={{ height: '85vh' }}>
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-zinc-800 shrink-0">
          <div className="flex items-center gap-3">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-emerald-500/15">
              <ScrollText className="h-4 w-4 text-emerald-400" />
            </div>
            <div>
              <p className="font-semibold text-white text-sm">{nodeName}</p>
              <p className="text-[11px] text-zinc-500">Live system logs (last 100 lines + stream)</p>
            </div>
            <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-emerald-500/15 border border-emerald-500/30 text-xs text-emerald-400">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
              streaming
            </span>
          </div>
          <button onClick={onClose} className="flex h-8 w-8 items-center justify-center rounded-lg text-zinc-400 hover:text-white hover:bg-zinc-800 transition-colors">
            <X className="h-4 w-4" />
          </button>
        </div>

        {/* Log body */}
        <div className="flex-1 overflow-y-auto p-5 font-mono text-xs text-zinc-300 leading-6 space-y-px">
          {error ? (
            <div className="flex flex-col items-center justify-center h-full gap-3 text-center">
              <div className="flex h-12 w-12 items-center justify-center rounded-full bg-red-500/15">
                <XCircle className="h-6 w-6 text-red-400" />
              </div>
              <p className="text-red-400 font-medium">{error}</p>
              <p className="text-zinc-500 text-xs max-w-sm">Make sure the node is connected and the featherdeploy-node service is running.</p>
            </div>
          ) : lines.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-full gap-2 text-zinc-500">
              <Loader2 className="h-5 w-5 animate-spin" />
              <p>Waiting for log lines…</p>
            </div>
          ) : (
            lines.map((l, i) => (
              <div key={i} className={cn('whitespace-pre-wrap break-all',
                l.includes('ERROR') || l.includes('error') || l.includes('ERR') ? 'text-red-400' :
                l.includes('WARN') || l.includes('warn') ? 'text-amber-400' :
                l.includes('INFO') || l.includes('info') ? 'text-zinc-200' : 'text-zinc-500'
              )}>
                {l}
              </div>
            ))
          )}
          <div ref={bottomRef} />
        </div>
      </div>
    </div>
  )
}

// ── Brain Card ────────────────────────────────────────────────────────────────
function BrainCard({ brain, history, ramHistory, connected }: {
  brain: NonNullable<ReturnType<typeof useStatsSSE>['brain']>
  history: CpuPoint[]; ramHistory: RamPoint[]; connected: boolean
}) {
  const [showLogs, setShowLogs] = useState(false)
  const cpuPct = Math.round(brain.CPU ?? 0)
  const ramPct = pct(brain.RAMUsed, brain.RAMTotal)
  const diskPct = pct(brain.DiskUsed, brain.DiskTotal)
  const cpuColor = cpuPct > 80 ? '#ef4444' : cpuPct > 60 ? '#f59e0b' : '#6366f1'

  return (
    <>
      <Card className="border-border/60 overflow-hidden">
        <div className={cn('h-0.5', brain.Alive ? 'bg-emerald-500' : 'bg-red-500')} />
        <CardHeader className="pb-3">
          <div className="flex items-start justify-between gap-2">
            <div className="flex items-center gap-2.5">
              <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Crown className="h-4 w-4" />
              </div>
              <div>
                <CardTitle className="text-base font-semibold flex items-center gap-2">
                  Brain Node
                  {connected ? <Wifi className="h-3.5 w-3.5 text-emerald-500" /> : <WifiOff className="h-3.5 w-3.5 text-muted-foreground animate-pulse" />}
                </CardTitle>
                <CardDescription className="text-xs font-mono">{brain.BrainID || 'main'}</CardDescription>
              </div>
            </div>
            <div className="flex items-center gap-1.5">
              <button
                onClick={() => setShowLogs(true)}
                title="View Logs"
                className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
              >
                <ScrollText className="h-3.5 w-3.5" />
              </button>
              <Badge className={brain.Alive
                ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-0'
                : 'bg-red-500/15 text-red-600 dark:text-red-400 border-0'
              }>
                {brain.Alive ? <><CheckCircle2 className="h-3 w-3 mr-1" />Healthy</> : <><XCircle className="h-3 w-3 mr-1" />Degraded</>}
              </Badge>
            </div>
          </div>
        </CardHeader>

        <CardContent className="space-y-4 pb-4">
          <div>
            <div className="flex items-center justify-between mb-1.5">
              <span className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
                <Cpu className="h-3.5 w-3.5" /> CPU
              </span>
              <span className="text-2xl font-bold tabular-nums">{cpuPct}%</span>
            </div>
            <Sparkline data={history} color={cpuColor} />
          </div>

          {brain.RAMTotal > 0 && (
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-sm">
                <span className="flex items-center gap-1.5 text-muted-foreground"><MemoryStick className="h-3.5 w-3.5" /> RAM</span>
                <span className="tabular-nums font-medium">
                  {gb(brain.RAMUsed)} / {gb(brain.RAMTotal)} GB
                  <span className="text-muted-foreground ml-1.5 text-xs">({ramPct}%)</span>
                </span>
              </div>
              <Progress value={ramPct} className={cn('h-2.5 rounded-full', barColor(ramPct))} />
            </div>
          )}

          {brain.DiskTotal > 0 && (
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-sm">
                <span className="flex items-center gap-1.5 text-muted-foreground"><HardDrive className="h-3.5 w-3.5" /> Disk</span>
                <span className="tabular-nums font-medium">
                  {gb(brain.DiskUsed)} / {gb(brain.DiskTotal)} GB
                  <span className="text-muted-foreground ml-1.5 text-xs">({diskPct}%)</span>
                </span>
              </div>
              <Progress value={diskPct} className={cn('h-2.5 rounded-full', barColor(diskPct))} />
            </div>
          )}
        </CardContent>
      </Card>

      {showLogs && <LogViewer nodeId="brain" nodeName="Brain (Main Server)" onClose={() => setShowLogs(false)} />}
    </>
  )
}

// ── Worker Node Card ──────────────────────────────────────────────────────────
function WorkerNodeCard({ node, history, ramHistory }: {
  node: NodeStats; history: CpuPoint[]; ramHistory: RamPoint[]
}) {
  const [showLogs, setShowLogs] = useState(false)
  const isConn = node.status === 'connected'
  const stale = node.last_stats_at
    ? Date.now() - new Date(node.last_stats_at).getTime() > 30_000 : true
  const live = isConn && !stale
  const ramPct = pct(node.ram_used, node.ram_total)
  const diskPct = pct(node.disk_used, node.disk_total)
  const cpuPct = Math.round(node.cpu_usage ?? 0)
  const cpuColor = cpuPct > 80 ? '#ef4444' : cpuPct > 60 ? '#f59e0b' : '#6366f1'

  return (
    <>
      <Card className={cn('border-border/60 overflow-hidden transition-all', !isConn && 'opacity-60')}>
        <div className={cn('h-0.5', isConn ? 'bg-emerald-500' : 'bg-muted-foreground/30')} />
        <CardHeader className="pb-3">
          <div className="flex items-start justify-between gap-2">
            <div className="flex items-center gap-2.5">
              <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-muted text-muted-foreground">
                <Server className="h-4 w-4" />
              </div>
              <div>
                <CardTitle className="text-base font-semibold">{node.name}</CardTitle>
                {node.hostname && (
                  <CardDescription className="text-xs font-mono">{node.hostname}</CardDescription>
                )}
              </div>
            </div>
            <div className="flex items-center gap-1.5">
              <button
                onClick={() => setShowLogs(true)}
                title="View Logs"
                className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
              >
                <ScrollText className="h-3.5 w-3.5" />
              </button>
              <span className={cn(
                'text-xs font-medium rounded-full px-2 py-0.5',
                isConn ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400' : 'bg-muted text-muted-foreground',
              )}>
                {node.status}
              </span>
            </div>
          </div>
        </CardHeader>

        <CardContent className="space-y-3 pb-4">
          {live ? (
            <>
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <span className="flex items-center gap-1.5 text-sm text-muted-foreground"><Cpu className="h-3.5 w-3.5" /> CPU</span>
                  <span className="text-2xl font-bold tabular-nums">{cpuPct}%</span>
                </div>
                <Sparkline data={history} color={cpuColor} />
              </div>
              {node.ram_total > 0 && (
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between text-sm">
                    <span className="flex items-center gap-1.5 text-muted-foreground"><MemoryStick className="h-3.5 w-3.5" /> RAM</span>
                    <span className="tabular-nums font-medium">
                      {gb(node.ram_used)} / {gb(node.ram_total)} GB
                      <span className="text-muted-foreground ml-1.5 text-xs">({ramPct}%)</span>
                    </span>
                  </div>
                  <Progress value={ramPct} className={cn('h-2.5 rounded-full', barColor(ramPct))} />
                </div>
              )}
              {node.disk_total > 0 && (
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between text-sm">
                    <span className="flex items-center gap-1.5 text-muted-foreground"><HardDrive className="h-3.5 w-3.5" /> Disk</span>
                    <span className="tabular-nums font-medium">
                      {gb(node.disk_used)} / {gb(node.disk_total)} GB
                      <span className="text-muted-foreground ml-1.5 text-xs">({diskPct}%)</span>
                    </span>
                  </div>
                  <Progress value={diskPct} className={cn('h-2.5 rounded-full', barColor(diskPct))} />
                </div>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              {isConn ? 'Collecting stats…' : 'Node offline — no stats available'}
            </p>
          )}
        </CardContent>
      </Card>

      {showLogs && <LogViewer nodeId={node.id} nodeName={node.name} onClose={() => setShowLogs(false)} />}
    </>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────
export function ClusterPage() {
  const navigate = useNavigate()
  const { brain, nodes: liveNodes, connected } = useStatsSSE()
  const { timezone } = useTimezone()

  const brainHistoryRef = useRef<CpuPoint[]>([])
  const brainRamHistoryRef = useRef<RamPoint[]>([])
  const nodeHistoriesRef = useRef<Record<number, CpuPoint[]>>({})
  const nodeRamHistoriesRef = useRef<Record<number, RamPoint[]>>({})
  const [, setTick] = useState(0)

  useEffect(() => {
    if (!brain) return
    const t = new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date())
    brainHistoryRef.current = [...brainHistoryRef.current, { t, v: brain.CPU ?? 0 }].slice(-30)
    brainRamHistoryRef.current = [...brainRamHistoryRef.current, { t, v: brain.RAMTotal > 0 ? (brain.RAMUsed / brain.RAMTotal) * 100 : 0 }].slice(-30)
    setTick(n => n + 1)
  }, [brain, timezone])

  useEffect(() => {
    if (!liveNodes.length) return
    const t = new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date())
    for (const node of liveNodes) {
      nodeHistoriesRef.current[node.id] = [...(nodeHistoriesRef.current[node.id] ?? []), { t, v: node.cpu_usage }].slice(-30)
      nodeRamHistoriesRef.current[node.id] = [...(nodeRamHistoriesRef.current[node.id] ?? []), { t, v: node.ram_total > 0 ? (node.ram_used / node.ram_total) * 100 : 0 }].slice(-30)
    }
    setTick(n => n + 1)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveNodes])

  return (
    <div className="space-y-6 p-1">
      {/* Header */}
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Button variant="ghost" size="icon" onClick={() => navigate('/dashboard')} title="Back to Dashboard">
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div>
            <h1 className="text-2xl font-bold tracking-tight">Cluster Nodes</h1>
            <p className="text-sm text-muted-foreground mt-0.5">
              Live stats for every node. Click <ScrollText className="inline h-3.5 w-3.5 mx-0.5" /> to view real-time logs.
            </p>
          </div>
        </div>
        <div className={cn(
          'flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium border',
          connected
            ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-300/30'
            : 'bg-muted text-muted-foreground border-border',
        )}>
          {connected
            ? <><Wifi className="h-3 w-3 mr-1" />Live</>
            : <><WifiOff className="h-3 w-3 mr-1 animate-pulse" />Reconnecting…</>}
        </div>
      </div>

      {/* Node grid */}
      {!brain ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Card key={i} className="border-border/60">
              <CardContent className="p-4 space-y-3">
                <Skeleton className="h-5 w-32" />
                <Skeleton className="h-12 w-full" />
                <Skeleton className="h-3 w-full" />
                <Skeleton className="h-3 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <BrainCard
            brain={brain}
            history={brainHistoryRef.current}
            ramHistory={brainRamHistoryRef.current}
            connected={connected}
          />
          {liveNodes.map(node => (
            <WorkerNodeCard
              key={node.id}
              node={node}
              history={nodeHistoriesRef.current[node.id] ?? []}
              ramHistory={nodeRamHistoriesRef.current[node.id] ?? []}
            />
          ))}
        </div>
      )}
    </div>
  )
}
