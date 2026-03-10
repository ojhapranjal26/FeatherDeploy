import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, Clock, GitCommit } from 'lucide-react'
import { deploymentsApi } from '@/api/deployments'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { LogViewer } from '@/components/LogViewer'
import { useDeploymentLogs } from '@/hooks/useDeploymentLogs'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Progress } from '@/components/ui/progress'

function formatDate(iso?: string) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

function formatDuration(start?: string, end?: string) {
  if (!start) return '—'
  const ms = new Date(end ?? Date.now()).getTime() - new Date(start).getTime()
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

export function DeploymentDetailPage() {
  const { projectId, serviceId, deploymentId } = useParams<{
    projectId: string
    serviceId: string
    deploymentId: string
  }>()
  const navigate = useNavigate()

  const { data: deployment, isLoading } = useQuery({
    queryKey: ['deployment', deploymentId],
    queryFn: () => deploymentsApi.get(projectId!, serviceId!, deploymentId!),
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'queued' || status === 'building' ? 3000 : false
    },
    enabled: !!projectId && !!serviceId && !!deploymentId,
  })

  const { lines, done } = useDeploymentLogs(projectId!, serviceId!, deploymentId!)

  const isActive =
    deployment?.status === 'queued' || deployment?.status === 'building'

  return (
    <div className="flex h-full flex-col">
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 w-fit gap-1.5 text-muted-foreground"
        onClick={() =>
          navigate(`/projects/${projectId}/services/${serviceId}/deployments`)
        }
      >
        <ChevronLeft className="h-3.5 w-3.5" /> All deployments
      </Button>

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-64" />
        </div>
      ) : (
        <>
          {/* Header */}
          <div className="mb-4 flex flex-wrap items-center gap-3">
            <DeploymentStatusBadge status={deployment!.status} />
            {deployment?.commit_sha && (
              <div className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
                <GitCommit className="h-3.5 w-3.5" />
                {deployment.commit_sha.slice(0, 7)}
              </div>
            )}
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Clock className="h-3.5 w-3.5" />
              {formatDate(deployment?.started_at ?? deployment?.created_at)}
            </div>
            <div className="ml-auto text-xs text-muted-foreground">
              Duration: {formatDuration(deployment?.started_at, deployment?.finished_at)}
            </div>
          </div>

          {isActive && (
            <Progress value={null} className="mb-4 h-1 animate-pulse" />
          )}

          {deployment?.error_message && (
            <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
              {deployment.error_message}
            </div>
          )}
        </>
      )}

      {/* Log viewer — fills available height */}
      <LogViewer
        lines={lines}
        done={done}
        className="flex-1 min-h-0 h-full"
      />
    </div>
  )
}
