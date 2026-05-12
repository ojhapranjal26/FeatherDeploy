import { useRef, useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useMutation } from '@tanstack/react-query'
import {
  Activity, AlertTriangle, Rocket, CheckCircle2, XCircle,
  Cpu, MemoryStick, HardDrive, Server, Wifi, WifiOff,
  FolderGit2, TrendingUp, Layers, ArrowUpCircle, X, RefreshCw,
  Tag, GitCommit, Clock, Loader2, Circle,
} from 'lucide-react'
import client from '@/api/client'
import { systemApi, type VersionInfo } from '@/api/system'
import { useStatsSSE } from '@/hooks/useStatsSSE'
import { useAuth } from '@/context/AuthContext'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Card, CardContent,
} from '@/components/ui/card'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDate } from '@/lib/dateFormat'

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
const fmt = (ms: number) => {
  const s = Math.floor((Date.now() - ms) / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  return `${Math.floor(m / 60)}h ago`
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
    const ramPct = brain.RAMTotal > 0 ? (brain.RAMUsed / brain.RAMTotal) * 100 : 0
    brainRamHistoryRef.current = [...brainRamHistoryRef.current, { t, v: ramPct }].slice(-30)
    setTick(n => n + 1)
  }, [brain, timezone])

  useEffect(() => {
    if (!liveNodes.length) return
    const t = new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    }).format(new Date())
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

      {/* Cluster Health card — click to open the full cluster page */}
      {(() => {
        // Aggregate CPU / RAM / Disk across Brain + all worker nodes
        const allNodes = [
          ...(brain ? [{ cpu: brain.CPU ?? 0, ramUsed: brain.RAMUsed, ramTotal: brain.RAMTotal, diskUsed: brain.DiskUsed, diskTotal: brain.DiskTotal, alive: brain.Alive }] : []),
          ...liveNodes.map(n => ({ cpu: n.cpu_usage, ramUsed: n.ram_used, ramTotal: n.ram_total, diskUsed: n.disk_used, diskTotal: n.disk_total, alive: n.status === 'connected' })),
        ]
        const nodeCount = allNodes.length || 1
        const avgCpu   = allNodes.length ? Math.round(allNodes.reduce((s, n) => s + n.cpu, 0) / nodeCount) : 0
        const totalRamUsed  = allNodes.reduce((s, n) => s + (n.ramUsed  || 0), 0)
        const totalRamTotal = allNodes.reduce((s, n) => s + (n.ramTotal || 0), 0)
        const totalDiskUsed  = allNodes.reduce((s, n) => s + (n.diskUsed  || 0), 0)
        const totalDiskTotal = allNodes.reduce((s, n) => s + (n.diskTotal || 0), 0)
        const ramPct  = totalRamTotal  > 0 ? Math.round((totalRamUsed  / totalRamTotal)  * 100) : 0
        const diskPct = totalDiskTotal > 0 ? Math.round((totalDiskUsed / totalDiskTotal) * 100) : 0
        const onlineCount = allNodes.filter(n => n.alive).length

        const bar = (v: number) => v > 85 ? 'bg-red-500' : v > 65 ? 'bg-amber-400' : 'bg-emerald-500'

        return (
          <div className="space-y-3">
            <h2 className="text-lg font-semibold flex items-center gap-2">
              <Layers className="h-5 w-5 text-muted-foreground" />
              Cluster Health
            </h2>
            <button
              onClick={() => navigate('/cluster')}
              className="w-full text-left group"
            >
              <Card className="border-border/60 overflow-hidden hover:border-primary/40 hover:shadow-md transition-all cursor-pointer">
                <div className={cn('h-0.5', connected ? 'bg-emerald-500' : 'bg-muted-foreground/30')} />
                <CardContent className="p-5 space-y-4">
                  {/* Top row */}
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
                        <Server className="h-5 w-5" />
                      </div>
                      <div>
                        <p className="font-semibold text-base">
                          {onlineCount} / {allNodes.length} {allNodes.length === 1 ? 'Node' : 'Nodes'} Online
                        </p>
                        <p className="text-xs text-muted-foreground">Click to view all nodes & logs</p>
                      </div>
                    </div>
                    <span className={cn(
                      'text-xs font-medium rounded-full px-2.5 py-1 border',
                      connected
                        ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-300/30'
                        : 'bg-muted text-muted-foreground border-border',
                    )}>
                      {connected ? '● Live' : '○ Reconnecting'}
                    </span>
                  </div>

                  {/* Stats grid */}
                  <div className="grid grid-cols-3 gap-4">
                    {/* CPU */}
                    <div className="space-y-1.5">
                      <div className="flex items-center justify-between text-xs text-muted-foreground">
                        <span className="flex items-center gap-1"><Cpu className="h-3 w-3" /> Avg CPU</span>
                        <span className="font-bold text-foreground tabular-nums">{avgCpu}%</span>
                      </div>
                      <div className="h-2 rounded-full bg-muted overflow-hidden">
                        <div className={cn('h-full rounded-full transition-all', bar(avgCpu))} style={{ width: `${avgCpu}%` }} />
                      </div>
                    </div>
                    {/* RAM */}
                    <div className="space-y-1.5">
                      <div className="flex items-center justify-between text-xs text-muted-foreground">
                        <span className="flex items-center gap-1"><MemoryStick className="h-3 w-3" /> RAM</span>
                        <span className="font-bold text-foreground tabular-nums">{ramPct}%</span>
                      </div>
                      <div className="h-2 rounded-full bg-muted overflow-hidden">
                        <div className={cn('h-full rounded-full transition-all', bar(ramPct))} style={{ width: `${ramPct}%` }} />
                      </div>
                      <p className="text-[10px] text-muted-foreground tabular-nums">{gb(totalRamUsed)} / {gb(totalRamTotal)} GB</p>
                    </div>
                    {/* Disk */}
                    <div className="space-y-1.5">
                      <div className="flex items-center justify-between text-xs text-muted-foreground">
                        <span className="flex items-center gap-1"><HardDrive className="h-3 w-3" /> Disk</span>
                        <span className="font-bold text-foreground tabular-nums">{diskPct}%</span>
                      </div>
                      <div className="h-2 rounded-full bg-muted overflow-hidden">
                        <div className={cn('h-full rounded-full transition-all', bar(diskPct))} style={{ width: `${diskPct}%` }} />
                      </div>
                      <p className="text-[10px] text-muted-foreground tabular-nums">{gb(totalDiskUsed)} / {gb(totalDiskTotal)} GB</p>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </button>
          </div>
        )
      })()}

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
                    <span
                      className="flex items-center gap-1 text-xs text-muted-foreground shrink-0 whitespace-nowrap"
                      title={formatDate(dep.created_at, timezone)}
                    >
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
