import { useRef, useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useMutation } from '@tanstack/react-query'
import {
  Activity, AlertTriangle, Rocket, CheckCircle2, XCircle,
  Crown, Cpu, MemoryStick, HardDrive, Server, Wifi, WifiOff,
  FolderGit2, TrendingUp, Layers, ArrowUpCircle, X, RefreshCw,
  Tag, GitCommit, Clock, Loader2, Circle,
} from 'lucide-react'
import {
  AreaChart, Area, ResponsiveContainer, Tooltip, XAxis,
} from 'recharts'
import client from '@/api/client'
import { systemApi, type VersionInfo } from '@/api/system'
import { useStatsSSE, type NodeStats } from '@/hooks/useStatsSSE'
import { useAuth } from '@/context/AuthContext'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Progress } from '@/components/ui/progress'
import { Badge } from '@/components/ui/badge'
import {
  Card, CardContent, CardHeader, CardTitle, CardDescription,
} from '@/components/ui/card'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'

// ── Types ─────────────────────────────────────────────────────────────────────
type DashDep = {
  id: number; service_id: number; service_name: string
  status: 'success' | 'failed' | 'running' | 'pending'
  commit_sha?: string; deploy_type: string; created_at: string
}
type DashStats = {
  total_projects: number; total_services: number
  running_services: number; total_deployments: number
  failed_deployments: number; recent_deployments: DashDep[]
}
type CpuPoint = { t: string; v: number }
type RamPoint = { t: string; v: number }

// ── Helpers ───────────────────────────────────────────────────────────────────
const gb = (bytes: number) => (bytes / 1073741824).toFixed(1)
const pct = (used: number, total: number) => total > 0 ? Math.round((used / total) * 100) : 0
const fmt = (ms: number) => {
  const s = Math.floor((Date.now() - ms) / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  return `${Math.floor(m / 60)}h ago`
}

function barColor(p: number) {
  if (p > 85) return '[&>div]:bg-red-500'
  if (p > 65) return '[&>div]:bg-amber-400'
  return '[&>div]:bg-emerald-500'
}

// ── CpuSparkline ──────────────────────────────────────────────────────────────
function CpuSparkline({ data, color = '#6366f1' }: { data: CpuPoint[]; color?: string }) {
  if (data.length < 2) return null
  const gradId = `grad${color.replace('#', '')}`
  return (
    <ResponsiveContainer width="100%" height={48}>
      <AreaChart data={data} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
        <defs>
          <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
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
        <Area
          type="monotone" dataKey="v" stroke={color} strokeWidth={1.5}
          fill={`url(#${gradId})`}
          dot={false} isAnimationActive={false}
        />
      </AreaChart>
    </ResponsiveContainer>
  )
}

// ── RamSparkline ──────────────────────────────────────────────────────────────
function RamSparkline({ data }: { data: RamPoint[] }) {
  if (data.length < 2) return null
  const color = '#10b981'
  return (
    <ResponsiveContainer width="100%" height={32}>
      <AreaChart data={data} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
        <defs>
          <linearGradient id="gradRam" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity={0.35} />
            <stop offset="100%" stopColor={color} stopOpacity={0.03} />
          </linearGradient>
        </defs>
        <XAxis dataKey="t" hide />
        <Tooltip
          contentStyle={{ fontSize: 11, padding: '2px 8px', borderRadius: 6 }}
          formatter={(v: unknown) => [`${Number(v).toFixed(1)}%`, 'RAM']}
          labelFormatter={() => ''}
        />
        <Area
          type="monotone" dataKey="v" stroke={color} strokeWidth={1.5}
          fill="url(#gradRam)"
          dot={false} isAnimationActive={false}
        />
      </AreaChart>
    </ResponsiveContainer>
  )
}

// ── StatCard ──────────────────────────────────────────────────────────────────
function StatCard({ title, value, icon: Icon, iconCls, bgCls, loading }: {
  title: string; value: number; icon: React.ElementType
  iconCls: string; bgCls: string; loading?: boolean
}) {
  return (
    <Card className="border-border/60">
      <CardContent className="flex items-center gap-3 p-4 sm:gap-4 sm:p-5">
        <div className={cn('flex h-10 w-10 sm:h-11 sm:w-11 shrink-0 items-center justify-center rounded-xl', bgCls)}>
          <Icon className={cn('h-4 w-4 sm:h-5 sm:w-5', iconCls)} />
        </div>
        <div>
          {loading ? <Skeleton className="h-8 w-12 mb-1" /> : (
            <p className="text-2xl font-bold tabular-nums leading-none">{value}</p>
          )}
          <p className="text-sm text-muted-foreground mt-0.5">{title}</p>
        </div>
      </CardContent>
    </Card>
  )
}

// ── BrainCard ─────────────────────────────────────────────────────────────────
function BrainCard({ brain, history, ramHistory, connected }: {
  brain: NonNullable<ReturnType<typeof useStatsSSE>['brain']>
  history: CpuPoint[]; ramHistory: RamPoint[]; connected: boolean
}) {
  const cpuPct = Math.round(brain.CPU ?? 0)
  const ramPct = pct(brain.RAMUsed, brain.RAMTotal)
  const diskPct = pct(brain.DiskUsed, brain.DiskTotal)
  const cpuColor = cpuPct > 80 ? '#ef4444' : cpuPct > 60 ? '#f59e0b' : '#6366f1'

  return (
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
                {connected
                  ? <Wifi className="h-3.5 w-3.5 text-emerald-500" />
                  : <WifiOff className="h-3.5 w-3.5 text-muted-foreground animate-pulse" />}
              </CardTitle>
              <CardDescription className="text-xs font-mono">{brain.BrainID || 'main'}</CardDescription>
            </div>
          </div>
          <Badge className={brain.Alive
            ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-0'
            : 'bg-red-500/15 text-red-600 dark:text-red-400 border-0'
          }>
            {brain.Alive
              ? <><CheckCircle2 className="h-3 w-3 mr-1" />Healthy</>
              : <><XCircle className="h-3 w-3 mr-1" />Degraded</>}
          </Badge>
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
          <CpuSparkline data={history} color={cpuColor} />
        </div>

        {brain.RAMTotal > 0 && (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between text-sm">
              <span className="flex items-center gap-1.5 text-muted-foreground">
                <MemoryStick className="h-3.5 w-3.5" /> RAM
              </span>
              <span className="tabular-nums font-medium">
                {gb(brain.RAMUsed)} / {gb(brain.RAMTotal)} GB
                <span className="text-muted-foreground ml-1.5 text-xs">({ramPct}%)</span>
              </span>
            </div>
            <Progress value={ramPct} className={cn('h-2.5 rounded-full', barColor(ramPct))} />
            <RamSparkline data={ramHistory} />
          </div>
        )}

        {brain.DiskTotal > 0 && (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between text-sm">
              <span className="flex items-center gap-1.5 text-muted-foreground">
                <HardDrive className="h-3.5 w-3.5" /> Disk
              </span>
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
  )
}

// ── NodeCard ──────────────────────────────────────────────────────────────────
function NodeCard({ node, history, ramHistory }: { node: NodeStats; history: CpuPoint[]; ramHistory: RamPoint[] }) {
  const isConn = node.status === 'connected'
  const stale = node.last_stats_at
    ? Date.now() - new Date(node.last_stats_at).getTime() > 30_000 : true
  const live = isConn && !stale
  const ramPct = pct(node.ram_used, node.ram_total)
  const diskPct = pct(node.disk_used, node.disk_total)

  return (
    <Card className={cn('border-border/60 overflow-hidden transition-all', !isConn && 'opacity-60')}>
      <div className={cn('h-0.5', isConn ? 'bg-emerald-500' : 'bg-muted-foreground/30')} />
      <CardContent className="p-4 space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 min-w-0">
            <Server className="h-4 w-4 text-muted-foreground shrink-0" />
            <span className="text-sm font-semibold truncate">{node.name}</span>
          </div>
          <span className={cn(
            'text-xs font-medium rounded-full px-2 py-0.5',
            isConn
              ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
              : 'bg-muted text-muted-foreground',
          )}>
            {node.status}
          </span>
        </div>

        {live ? (
          <>
            <div>
              <div className="flex items-center justify-between mb-1 text-xs">
                <span className="text-muted-foreground flex items-center gap-1">
                  <Cpu className="h-3 w-3" /> CPU
                </span>
                <span className="tabular-nums font-bold">{Math.round(node.cpu_usage)}%</span>
              </div>
              <CpuSparkline data={history} color="#6366f1" />
            </div>
            {node.ram_total > 0 && (
              <div className="space-y-1">
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span className="flex items-center gap-1"><MemoryStick className="h-3 w-3" /> RAM</span>
                  <span className="tabular-nums">{gb(node.ram_used)} / {gb(node.ram_total)} GB</span>
                </div>
                <Progress value={ramPct} className={cn('h-1.5', barColor(ramPct))} />
                <RamSparkline data={ramHistory} />
              </div>
            )}
            {node.disk_total > 0 && (
              <div className="space-y-1">
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span className="flex items-center gap-1"><HardDrive className="h-3 w-3" /> Disk</span>
                  <span className="tabular-nums">{gb(node.disk_used)} / {gb(node.disk_total)} GB</span>
                </div>
                <Progress value={diskPct} className={cn('h-1.5', barColor(diskPct))} />
              </div>
            )}
          </>
        ) : (
          <p className="text-xs text-muted-foreground">
            {isConn ? 'Collecting stats…' : 'No stats available'}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

// ── UpdateBanner ──────────────────────────────────────────────────────────────
function UpdateBanner({ info, isSuperAdmin }: { info: VersionInfo; isSuperAdmin: boolean }) {
  const [dismissed, setDismissed] = useState(() => {
    const key = `update-dismissed-${info.latest_version}`
    return localStorage.getItem(key) === '1'
  })
  const [dialogOpen, setDialogOpen] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [showChangelog, setShowChangelog] = useState(false)

  const dismiss = () => {
    localStorage.setItem(`update-dismissed-${info.latest_version}`, '1')
    setDismissed(true)
  }

  const updateMutation = useMutation({
    mutationFn: systemApi.triggerUpdate,
    onSuccess: (data) => {
      setDialogOpen(false)
      setUpdating(true)
      toast.success(`Update to v${data.version} started — panel will restart in ~60 seconds.`)
      // Reload the page after ~70 seconds to pick up the new version
      setTimeout(() => window.location.reload(), 70_000)
    },
    onError: (err: unknown) => {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? 'Update failed — check server logs for details.'
      toast.error(msg)
    },
  })

  if (dismissed && !updating) return null

  if (updating) {
    return (
      <div className="rounded-xl border border-blue-400/30 bg-blue-500/8 px-4 py-3 flex items-center gap-3">
        <RefreshCw className="h-4 w-4 text-blue-500 animate-spin shrink-0" />
        <p className="text-sm font-medium text-blue-700 dark:text-blue-300">
          Updating to v{info.latest_version}… The panel will restart automatically. Page will refresh shortly.
        </p>
      </div>
    )
  }

  return (
    <>
      <div className="rounded-xl border border-amber-400/40 bg-amber-500/8 px-4 py-3.5">
        <div className="flex items-start gap-3">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-amber-500/15 text-amber-600 dark:text-amber-400 mt-0.5">
            <ArrowUpCircle className="h-4 w-4" />
          </div>
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <p className="text-sm font-semibold text-foreground">
                Update available — FeatherDeploy v{info.latest_version}
              </p>
              <span className="text-xs font-mono px-1.5 py-0.5 rounded bg-amber-500/15 text-amber-700 dark:text-amber-300 border border-amber-400/30 flex items-center gap-1">
                <Tag className="h-3 w-3" /> v{info.current_version} → v{info.latest_version}
              </span>
            </div>
            <button
              onClick={() => setShowChangelog(v => !v)}
              className="mt-0.5 text-xs text-muted-foreground hover:text-foreground transition-colors underline-offset-2 hover:underline"
            >
              {showChangelog ? 'Hide changelog' : 'Show what\'s new'}
            </button>
            {showChangelog && (
              <div className="mt-3 rounded-lg border border-border/60 bg-background/60 p-3 text-xs text-muted-foreground leading-relaxed max-h-48 overflow-y-auto whitespace-pre-wrap">
                {info.changelog}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0 mt-0.5">
            {isSuperAdmin && (
              <Button size="sm" className="h-7 text-xs gap-1.5 bg-amber-500 hover:bg-amber-600 text-white border-0"
                onClick={() => setDialogOpen(true)}>
                <ArrowUpCircle className="h-3.5 w-3.5" />
                Update Now
              </Button>
            )}
            <button
              onClick={dismiss}
              className="flex h-7 w-7 items-center justify-center rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
              title="Dismiss"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <ArrowUpCircle className="h-5 w-5 text-amber-500" />
              Update FeatherDeploy
            </DialogTitle>
            <DialogDescription>
              This will download v{info.latest_version}, replace the running binary, apply any
              database migrations, and restart the panel service. The dashboard will be
              unavailable for ~60 seconds during the restart.
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-lg bg-muted/50 p-3 text-xs text-muted-foreground space-y-1">
            <p className="font-medium text-foreground text-sm">What's new in v{info.latest_version}</p>
            <div className="max-h-32 overflow-y-auto whitespace-pre-wrap leading-relaxed mt-1">
              {info.changelog}
            </div>
          </div>
          <DialogFooter className="gap-2">
            <Button variant="outline" onClick={() => setDialogOpen(false)}
              disabled={updateMutation.isPending}>
              Cancel
            </Button>
            <Button
              className="bg-amber-500 hover:bg-amber-600 text-white border-0 gap-1.5"
              onClick={() => updateMutation.mutate()}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending
                ? <><RefreshCw className="h-3.5 w-3.5 animate-spin" />Starting update…</>
                : <><ArrowUpCircle className="h-3.5 w-3.5" />Confirm update to v{info.latest_version}</>}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────
export function DashboardPage() {
  const navigate = useNavigate()
  const { user } = useAuth()
  const isSuperAdmin = user?.role === 'superadmin'

  const { data: dash, isLoading: dashLoading } = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => client.get<DashStats>('/dashboard').then(r => r.data),
    refetchInterval: 30_000,
  })

  // Version check: at most once per 24 hours thanks to staleTime.
  const { data: versionInfo } = useQuery({
    queryKey: ['system-version'],
    queryFn: systemApi.checkVersion,
    staleTime: 24 * 60 * 60 * 1000,
    retry: false,
  })

  const { brain, nodes: liveNodes, connected } = useStatsSSE()

  const brainHistoryRef = useRef<CpuPoint[]>([])
  const brainRamHistoryRef = useRef<RamPoint[]>([])
  const nodeHistoriesRef = useRef<Record<number, CpuPoint[]>>({})
  const nodeRamHistoriesRef = useRef<Record<number, RamPoint[]>>({})
  const [, setTick] = useState(0)

  useEffect(() => {
    if (!brain) return
    const t = new Date().toLocaleTimeString('en', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
    brainHistoryRef.current = [...brainHistoryRef.current, { t, v: brain.CPU ?? 0 }].slice(-30)
    const ramPct = brain.RAMTotal > 0 ? (brain.RAMUsed / brain.RAMTotal) * 100 : 0
    brainRamHistoryRef.current = [...brainRamHistoryRef.current, { t, v: ramPct }].slice(-30)
    setTick(n => n + 1)
  }, [brain])

  useEffect(() => {
    if (!liveNodes.length) return
    const t = new Date().toLocaleTimeString('en', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
    for (const node of liveNodes) {
      const prev = nodeHistoriesRef.current[node.id] ?? []
      nodeHistoriesRef.current[node.id] = [...prev, { t, v: node.cpu_usage }].slice(-30)
      const ramPct = node.ram_total > 0 ? (node.ram_used / node.ram_total) * 100 : 0
      const prevRam = nodeRamHistoriesRef.current[node.id] ?? []
      nodeRamHistoriesRef.current[node.id] = [...prevRam, { t, v: ramPct }].slice(-30)
    }
    setTick(n => n + 1)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveNodes])

  const recentDeps = dash?.recent_deployments ?? []

  return (
    <div className="space-y-6">
      {/* Update banner — shown when an update is available */}
      {versionInfo?.update_available && (
        <UpdateBanner info={versionInfo} isSuperAdmin={isSuperAdmin} />
      )}

      {/* Header */}
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Cluster Overview</h1>
          <p className="text-sm text-muted-foreground mt-0.5">Live infrastructure performance and health.</p>
        </div>
        <div className="flex items-center gap-2">
          {/* Current version badge */}
          {versionInfo && (
            <span className="hidden sm:flex items-center gap-1 text-xs text-muted-foreground font-mono px-2 py-1 rounded-md bg-muted/60 border border-border/50">
              <Tag className="h-3 w-3" /> v{versionInfo.current_version}
            </span>
          )}
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
      </div>

      {/* Stats row */}
      <div className="grid grid-cols-1 gap-3 xs:grid-cols-2 sm:gap-4 lg:grid-cols-4">
        <StatCard title="Projects" value={dash?.total_projects ?? 0}
          icon={FolderGit2} iconCls="text-indigo-600 dark:text-indigo-400"
          bgCls="bg-indigo-500/10" loading={dashLoading} />
        <StatCard title="Running Services" value={dash?.running_services ?? 0}
          icon={Activity} iconCls="text-emerald-600 dark:text-emerald-400"
          bgCls="bg-emerald-500/10" loading={dashLoading} />
        <StatCard title="Total Deployments" value={dash?.total_deployments ?? 0}
          icon={Rocket} iconCls="text-blue-600 dark:text-blue-400"
          bgCls="bg-blue-500/10" loading={dashLoading} />
        <StatCard title="Failed Deployments" value={dash?.failed_deployments ?? 0}
          icon={AlertTriangle} iconCls="text-red-600 dark:text-red-400"
          bgCls="bg-red-500/10" loading={dashLoading} />
      </div>

      {/* Infrastructure nodes */}
      <div className="space-y-3">
        <h2 className="text-lg font-semibold flex items-center gap-2">
          <Layers className="h-5 w-5 text-muted-foreground" />
          Infrastructure Nodes
        </h2>
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
            <BrainCard brain={brain} history={brainHistoryRef.current} ramHistory={brainRamHistoryRef.current} connected={connected} />
            {liveNodes.map(node => (
              <NodeCard key={node.id} node={node} history={nodeHistoriesRef.current[node.id] ?? []} ramHistory={nodeRamHistoriesRef.current[node.id] ?? []} />
            ))}
          </div>
        )}
      </div>

      {/* Recent deployments */}
      {recentDeps.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-lg font-semibold flex items-center gap-2">
            <TrendingUp className="h-5 w-5 text-muted-foreground" />
            Recent Deployments
          </h2>
          <Card className="border-border/60 overflow-hidden">
            <div className="divide-y divide-border/50">
              {recentDeps.slice(0, 8).map(dep => {
                const statusMap: Record<string, { bg: string; text: string; icon: React.ReactNode }> = {
                  success: {
                    bg:   'bg-emerald-500/10',
                    text: 'text-emerald-600 dark:text-emerald-400',
                    icon: <CheckCircle2 className="h-3.5 w-3.5" />,
                  },
                  failed: {
                    bg:   'bg-red-500/10',
                    text: 'text-red-600 dark:text-red-400',
                    icon: <XCircle className="h-3.5 w-3.5" />,
                  },
                  running: {
                    bg:   'bg-blue-500/10',
                    text: 'text-blue-600 dark:text-blue-400',
                    icon: <Loader2 className="h-3.5 w-3.5 animate-spin" />,
                  },
                  pending: {
                    bg:   'bg-muted',
                    text: 'text-muted-foreground',
                    icon: <Circle className="h-3.5 w-3.5" />,
                  },
                }
                const s = statusMap[dep.status] ?? statusMap.pending
                return (
                  <div
                    key={dep.id}
                    className="flex items-center gap-3 px-4 py-3 hover:bg-muted/40 transition-colors"
                  >
                    {/* Status icon circle */}
                    <div className={cn('flex h-7 w-7 shrink-0 items-center justify-center rounded-full', s.bg, s.text)}>
                      {s.icon}
                    </div>

                    {/* Service name + commit */}
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium truncate">{dep.service_name}</p>
                      {dep.commit_sha && (
                        <p className="flex items-center gap-1 text-[11px] font-mono text-muted-foreground mt-0.5">
                          <GitCommit className="h-3 w-3" />
                          {dep.commit_sha.slice(0, 7)}
                        </p>
                      )}
                    </div>

                    {/* Status badge */}
                    <span className={cn(
                      'hidden sm:inline-flex items-center gap-1 text-[11px] font-semibold px-2 py-0.5 rounded-full capitalize',
                      s.bg, s.text,
                    )}>
                      {dep.status}
                    </span>

                    {/* Time ago */}
                    <span className="flex items-center gap-1 text-xs text-muted-foreground shrink-0 whitespace-nowrap">
                      <Clock className="h-3 w-3" />
                      {fmt(new Date(dep.created_at).getTime())}
                    </span>
                  </div>
                )
              })}
            </div>
            <div className="px-4 py-2 border-t border-border/60">
              <Button variant="ghost" size="sm" className="w-full text-xs"
                onClick={() => navigate('/projects')}>
                View all projects →
              </Button>
            </div>
          </Card>
        </div>
      )}
    </div>
  )
}
