import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, Clock, GitCommit, CheckCircle2, Circle, Loader2, XCircle } from 'lucide-react'
import { deploymentsApi } from '@/api/deployments'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { LogViewer } from '@/components/LogViewer'
import { useDeploymentLogs } from '@/hooks/useDeploymentLogs'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

// Deployment pipeline steps — detected from log line prefixes.
// Steps marked optional=true are only shown when their prefix actually appears
// in the log (so skipped steps don't pollute the UI with false "completed" marks).
const PIPELINE_STEPS = [
  { prefix: '[ssh]',                    label: 'Set up SSH key',         optional: true  },
  { prefix: '[clone]',                  label: 'Clone repository',       optional: false },
  { prefix: '[build]',                  label: 'Run build command',      optional: true  },
  { prefix: '[dockerfile]',             label: 'Prepare Dockerfile',     optional: false },
  { prefix: '[podman] building',        label: 'Build container image',  optional: false },
  { prefix: '[podman] stopping',        label: 'Stop existing container',optional: true  },
  { prefix: '[podman] podman run',      label: 'Start container',        optional: false },
  { prefix: '[deploy] deployment suc',  label: 'Deployment complete',    optional: false },
]

function DeploymentSteps({ lines, done, failed }: { lines: { line?: string }[]; done: boolean; failed: boolean }) {
  const logText = lines.map(l => (l.line ?? '').toLowerCase()).join('\n')

  // Per-step: did this prefix actually appear in the log text?
  const stepReached = PIPELINE_STEPS.map(s => logText.includes(s.prefix.toLowerCase()))

  // Visible steps: mandatory ones always show; optional ones only when logged
  const visibleSteps = PIPELINE_STEPS.filter((s, i) => !s.optional || stepReached[i])

  // First visible step index (in visibleSteps array) that hasn't been reached
  const firstUnreachedVisIdx = visibleSteps.findIndex(
    (s) => !stepReached[PIPELINE_STEPS.indexOf(s)]
  )

  if (lines.length === 0 && !done) return null

  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-0 gap-y-2">
      {visibleSteps.map((step, visIdx) => {
        const globalIdx = PIPELINE_STEPS.indexOf(step)
        const isReached = stepReached[globalIdx]
        const isCurrent = !done && !failed && visIdx === firstUnreachedVisIdx
        const isFailed  = failed && visIdx === firstUnreachedVisIdx

        return (
          <div key={step.label} className="flex items-center">
            <div className={`flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs transition-all ${
              isReached  ? 'text-emerald-600 dark:text-emerald-400' :
              isCurrent  ? 'text-primary' :
              isFailed   ? 'text-destructive' :
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
            {visIdx < visibleSteps.length - 1 && (
              <div className={`h-px w-4 mx-0.5 transition-colors ${isReached ? 'bg-emerald-500' : 'bg-border'}`} />
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
    <div className="space-y-4 pb-8">
      <Button
        variant="ghost"
        size="sm"
        className="gap-1.5 text-muted-foreground"
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
          {/* Header card */}
          <div className="rounded-xl border bg-card p-4">
            <div className="flex flex-wrap items-center gap-3 mb-3">
              <DeploymentStatusBadge status={deployment!.status} />
              {deployment?.commit_sha && (
                <div className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground bg-muted px-2 py-1 rounded">
                  <GitCommit className="h-3 w-3" />
                  {deployment.commit_sha.slice(0, 7)}
                </div>
              )}
              <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <Clock className="h-3.5 w-3.5" />
                {formatDate(deployment?.started_at ?? deployment?.created_at)}
              </div>
              <div className="ml-auto text-xs font-medium text-muted-foreground">
                {formatDuration(deployment?.started_at, deployment?.finished_at)}
              </div>
            </div>

            {/* Pipeline steps */}
            <DeploymentSteps lines={lines} done={done && !isFailed} failed={isFailed} />

            {deployment?.error_message && (
              <div className="mt-3 rounded-lg border border-destructive/30 bg-destructive/8 px-3 py-2.5 text-sm text-destructive">
                {deployment.error_message}
              </div>
            )}

            {isActive && lines.length === 0 && (
              <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Connecting to deployment log stream…
              </div>
            )}
          </div>
        </>
      )}

      {/* Log viewer */}
      <LogViewer lines={lines} done={done} className="h-[520px]" />
    </div>
  )
}
