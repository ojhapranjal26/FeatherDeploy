import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, Clock, GitCommit, CheckCircle2, Circle, Loader2, XCircle } from 'lucide-react'
import { deploymentsApi } from '@/api/deployments'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { LogViewer } from '@/components/LogViewer'
import { useDeploymentLogs } from '@/hooks/useDeploymentLogs'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

// Deployment pipeline steps — detected from log line prefixes
const PIPELINE_STEPS = [
  { prefix: '[ssh]',                    label: 'Set up SSH key' },
  { prefix: '[clone]',                  label: 'Clone repository' },
  { prefix: '[build]',                  label: 'Run build command' },
  { prefix: '[dockerfile]',             label: 'Prepare Dockerfile' },
  { prefix: '[podman] building',        label: 'Build container image' },
  { prefix: '[podman] stopping',        label: 'Stop existing container' },
  { prefix: '[podman] podman run',      label: 'Start container' },
  { prefix: '[deploy] deployment suc',  label: 'Deployment complete' },
]

function DeploymentSteps({ lines, done, failed }: { lines: { line?: string }[]; done: boolean; failed: boolean }) {
  const logText = lines.map(l => (l.line ?? '').toLowerCase()).join('\n')

  // Determine which steps have been reached
  const reachedIdx = PIPELINE_STEPS.reduce((max, step, i) =>
    logText.includes(step.prefix.toLowerCase()) ? i : max, -1)

  // Filter: only show steps that are relevant (skip SSH if not in logs at all and not reached)
  const visibleSteps = PIPELINE_STEPS.filter((step) => {
    if (step.prefix === '[ssh]') return logText.includes('[ssh]')
    return true
  })

  if (lines.length === 0 && !done) return null

  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-0 gap-y-2">
      {visibleSteps.map((step, i) => {
        const globalIdx = PIPELINE_STEPS.indexOf(step)
        const isReached = globalIdx <= reachedIdx
        const isCurrent = globalIdx === reachedIdx + 1 && !done
        const isFailed = failed && globalIdx === reachedIdx + 1

        return (
          <div key={step.label} className="flex items-center">
            <div className={`flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs transition-all ${
              isReached && done && !failed ? 'text-emerald-600 dark:text-emerald-400' :
              isReached && !isCurrent ? 'text-emerald-600 dark:text-emerald-400' :
              isCurrent ? 'text-primary' :
              isFailed ? 'text-destructive' :
              'text-muted-foreground'
            }`}>
              {isCurrent ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin shrink-0" />
              ) : isFailed ? (
                <XCircle className="h-3.5 w-3.5 shrink-0" />
              ) : isReached ? (
                <CheckCircle2 className="h-3.5 w-3.5 shrink-0" />
              ) : (
                <Circle className="h-3.5 w-3.5 shrink-0" />
              )}
              <span className="hidden sm:inline">{step.label}</span>
            </div>
            {i < visibleSteps.length - 1 && (
              <div className={`h-px w-4 mx-0.5 ${globalIdx < reachedIdx ? 'bg-emerald-500' : 'bg-border'}`} />
            )}
          </div>
        )
      })}
    </div>
  )
}

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
      const s = query.state.data?.status
      return s === 'running' || s === 'queued' || s === 'building' ? 2000 : false
    },
    enabled: !!projectId && !!serviceId && !!deploymentId,
  })

  const { lines, done } = useDeploymentLogs(projectId!, serviceId!, deploymentId!)

  const isActive = deployment?.status === 'running' || deployment?.status === 'queued' || deployment?.status === 'building'
  const isFailed = deployment?.status === 'failed'

  return (
    <div>
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 w-fit gap-1.5 text-muted-foreground"
        onClick={() => navigate(`/projects/${projectId}/services/${serviceId}`)}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> Back to service
      </Button>

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-64" />
        </div>
      ) : (
        <>
          {/* Header */}
          <div className="mb-3 flex flex-wrap items-center gap-3">
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

          {/* Animated pipeline steps */}
          <DeploymentSteps lines={lines} done={done && !isFailed} failed={isFailed} />

          {deployment?.error_message && (
            <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
              {deployment.error_message}
            </div>
          )}

          {isActive && lines.length === 0 && (
            <div className="mb-3 flex items-center gap-2 text-xs text-muted-foreground animate-pulse">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Waiting for deployment to start…
            </div>
          )}
        </>
      )}

      {/* Log viewer — fixed height with internal scrolling */}
      <LogViewer
        lines={lines}
        done={done}
        className="h-[520px]"
      />
    </div>
  )
}
