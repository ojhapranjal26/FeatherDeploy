import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, Clock, RefreshCw, GitCommit, CheckCircle2, XCircle, Loader2, Circle, GitBranch } from 'lucide-react'
import { deploymentsApi } from '@/api/deployments'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

function formatDate(iso?: string) {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function formatDuration(start?: string, end?: string) {
  if (!start) return '—'
  const ms = new Date(end ?? Date.now()).getTime() - new Date(start).getTime()
  if (ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `${m}:${String(sec).padStart(2, '0')}`
}

type DepStatus = 'success' | 'failed' | 'running' | 'pending' | string

function StatusIcon({ status }: { status: DepStatus }) {
  if (status === 'success') return <CheckCircle2 className="h-4 w-4 text-emerald-500" />
  if (status === 'failed') return <XCircle className="h-4 w-4 text-red-500" />
  if (status === 'running') return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />
  return <Circle className="h-4 w-4 text-muted-foreground/50" />
}

function StatusBadge({ status }: { status: DepStatus }) {
  const map: Record<string, string> = {
    success: 'bg-emerald-500/12 text-emerald-700 dark:text-emerald-400 border-emerald-400/20',
    failed:  'bg-red-500/12 text-red-700 dark:text-red-400 border-red-400/20',
    running: 'bg-blue-500/12 text-blue-700 dark:text-blue-400 border-blue-400/20',
    pending: 'bg-muted text-muted-foreground border-border/60',
  }
  return (
    <span className={cn(
      'inline-flex items-center gap-1 text-[11px] font-semibold px-2 py-0.5 rounded-full border capitalize',
      map[status] ?? map.pending,
    )}>
      <StatusIcon status={status} />
      {status}
    </span>
  )
}

export function DeploymentListPage() {
  const { projectId, serviceId } = useParams<{ projectId: string; serviceId: string }>()
  const navigate = useNavigate()

  const { data, isLoading, refetch, isFetching } = useQuery({
    queryKey: ['deployments', serviceId],
    queryFn: () => deploymentsApi.list(projectId!, serviceId!, { limit: 30 }),
    enabled: !!projectId && !!serviceId,
    refetchInterval: 5000,
  })

  // Sort newest first on the client side — the API may not guarantee order.
  const deployments = [...(data?.deployments ?? [])].sort(
    (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
  )

  return (
    <div className="space-y-6">
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 gap-1.5 text-muted-foreground"
        onClick={() => navigate(`/projects/${projectId}/services/${serviceId}`)}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> Back to service
      </Button>

      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Deployments</h1>
          <p className="text-sm text-muted-foreground mt-0.5">Newest first — auto-refreshes every 5 s</p>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => refetch()}
          disabled={isFetching}
        >
          <RefreshCw className={cn('h-3.5 w-3.5', isFetching && 'animate-spin')} />
          Refresh
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(5)].map((_, i) => <Skeleton key={i} className="h-16 w-full rounded-xl" />)}
        </div>
      ) : deployments.length === 0 ? (
        <div className="rounded-xl border border-dashed py-16 text-center text-muted-foreground">
          No deployments yet.
        </div>
      ) : (
        <div className="rounded-xl border divide-y overflow-hidden">
          {deployments.map((d, idx) => {
            const isLatest = idx === 0
            return (
              <div
                key={d.id}
                className={cn(
                  'group relative flex items-center gap-3 sm:gap-4 px-4 py-3.5 cursor-pointer transition-colors hover:bg-muted/40',
                  isLatest && 'bg-muted/20',
                )}
                onClick={() =>
                  navigate(`/projects/${projectId}/services/${serviceId}/deployments/${d.id}`)
                }
              >
                {/* Status stripe */}
                <div className={cn(
                  'absolute inset-y-0 left-0 w-0.5',
                  d.status === 'success' ? 'bg-emerald-500' :
                  d.status === 'failed'  ? 'bg-red-500' :
                  d.status === 'running' ? 'bg-blue-500 animate-pulse' :
                  'bg-border',
                )} />

                {/* Status icon */}
                <div className="shrink-0 ml-1">
                  <StatusIcon status={d.status} />
                </div>

                {/* Main content */}
                <div className="flex-1 min-w-0 grid grid-cols-1 sm:grid-cols-[1fr_auto] gap-0.5 sm:gap-x-4">
                  <div className="flex items-center gap-2 min-w-0">
                    <StatusBadge status={d.status} />
                    {isLatest && (
                      <Badge variant="outline" className="text-[10px] px-1.5 py-0 font-normal text-muted-foreground">
                        latest
                      </Badge>
                    )}
                  </div>
                  <div className="flex items-center gap-3 text-xs text-muted-foreground mt-1 sm:mt-0 sm:justify-end">
                    {d.commit_sha ? (
                      <span className="flex items-center gap-1 font-mono shrink-0">
                        <GitCommit className="h-3 w-3" />
                        {d.commit_sha.slice(0, 7)}
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 shrink-0">
                        <GitBranch className="h-3 w-3" />
                        {d.deploy_type}
                      </span>
                    )}
                    <span className="shrink-0">{formatDate(d.started_at ?? d.created_at)}</span>
                    <span className="flex items-center gap-1 shrink-0 tabular-nums font-mono">
                      <Clock className="h-3 w-3" />
                      {formatDuration(d.started_at, d.finished_at)}
                    </span>
                  </div>
                </div>

                {/* Arrow */}
                <span className="text-xs text-muted-foreground shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">
                  View →
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
