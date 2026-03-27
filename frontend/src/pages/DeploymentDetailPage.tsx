import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  ChevronLeft, Clock, GitCommit,
  CheckCircle2, Circle, Loader2, XCircle, AlertTriangle,
  Key, GitBranch, Wrench, Box, Layers, Rocket, CheckCheck,
  StopCircle,
} from 'lucide-react'
import { deploymentsApi } from '@/api/deployments'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { LogViewer } from '@/components/LogViewer'
import { useDeploymentLogs } from '@/hooks/useDeploymentLogs'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

// ─── Pipeline step definitions ────────────────────────────────────────────────
const PIPELINE_STEPS = [
  { prefix: '[ssh]',                   label: 'Set up SSH key',         icon: Key,         optional: true  },
  { prefix: '[clone]',                 label: 'Clone repository',       icon: GitBranch,   optional: false },
  { prefix: '[build]',                 label: 'Run build command',      icon: Wrench,      optional: true  },
  { prefix: '[dockerfile]',            label: 'Prepare Dockerfile',     icon: Layers,      optional: false },
  { prefix: '[podman] building',       label: 'Build container image',  icon: Box,         optional: false },
  { prefix: '[podman] stopping',       label: 'Stop old container',     icon: StopCircle,  optional: true  },
  { prefix: '[podman] podman run',     label: 'Start container',        icon: Rocket,      optional: false },
  { prefix: '[deploy] deployment suc', label: 'Deployment complete',    icon: CheckCheck,  optional: false },
]

// ─── Step row with animated pulse / check / error ────────────────────────────
function StepRow({
  icon: Icon, label, state, isLast,
}: {
  icon: React.ElementType
  label: string
  state: 'done' | 'active' | 'error' | 'pending' | 'skip'
  isLast: boolean
}) {
  return (
    <div className="flex items-stretch gap-3">
      {/* Spine */}
      <div className="flex flex-col items-center w-7 shrink-0">
        {/* Node */}
        <div className={cn(
          'flex h-7 w-7 items-center justify-center rounded-full border-2 transition-all duration-500 shrink-0',
          state === 'done'    && 'border-emerald-500 bg-emerald-500/10 text-emerald-400',
          state === 'active'  && 'border-primary bg-primary/10 text-primary animate-pulse',
          state === 'error'   && 'border-destructive bg-destructive/10 text-destructive',
          state === 'pending' && 'border-muted-foreground/30 bg-transparent text-muted-foreground/40',
          state === 'skip'    && 'hidden',
        )}>
          {state === 'active'  && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {state === 'done'    && <CheckCircle2 className="h-3.5 w-3.5" />}
          {state === 'error'   && <XCircle className="h-3.5 w-3.5" />}
          {state === 'pending' && <Circle className="h-3.5 w-3.5" />}
        </div>
        {/* Connector line */}
        {!isLast && state !== 'skip' && (
          <div className={cn(
            'w-0.5 flex-1 mt-1 mb-0.5 rounded-full transition-colors duration-500',
            state === 'done' ? 'bg-emerald-500/50' : 'bg-muted-foreground/15',
          )} />
        )}
      </div>

      {/* Content */}
      {state !== 'skip' && (
        <div className={cn(
          'flex items-center gap-2 pb-4 flex-1 min-w-0',
          isLast && 'pb-0',
        )}>
          <div className={cn(
            'flex h-7 w-7 items-center justify-center rounded-md shrink-0',
            state === 'done'    && 'bg-emerald-500/10 text-emerald-400',
            state === 'active'  && 'bg-primary/10 text-primary',
            state === 'error'   && 'bg-destructive/10 text-destructive',
            state === 'pending' && 'bg-muted text-muted-foreground/40',
          )}>
            <Icon className="h-3.5 w-3.5" />
          </div>
          <span className={cn(
            'text-sm font-medium transition-colors duration-300',
            state === 'done'    && 'text-emerald-400',
            state === 'active'  && 'text-foreground',
            state === 'error'   && 'text-destructive',
            state === 'pending' && 'text-muted-foreground/50',
          )}>
            {label}
          </span>
          {state === 'active' && (
            <span className="ml-auto text-[10px] text-primary font-mono animate-pulse shrink-0">
              running…
            </span>
          )}
        </div>
      )}
    </div>
  )
}

// ─── Skeleton loading placeholder ────────────────────────────────────────────
function StepsSkeleton() {
  return (
    <div className="space-y-1">
      {[1, 2, 3, 4, 5].map((i) => (
        <div key={i} className="flex items-center gap-3">
          <div className="h-7 w-7 rounded-full bg-muted animate-pulse shrink-0" />
          <div className={cn('h-3 rounded bg-muted animate-pulse', i % 2 === 0 ? 'w-32' : 'w-44')} />
        </div>
      ))}
    </div>
  )
}

// ─── Full pipeline component ──────────────────────────────────────────────────
function DeploymentPipeline({
  lines, done, failed,
}: {
  lines: { line?: string }[]
  done: boolean
  failed: boolean
}) {
  const logText = lines.map(l => (l.line ?? '').toLowerCase()).join('\n')
  const stepReached = PIPELINE_STEPS.map(s => logText.includes(s.prefix.toLowerCase()))

  // Visible steps: mandatory steps always shown; optional only when they appear in logs
  const visible = PIPELINE_STEPS.filter((s, i) => !s.optional || stepReached[i])

  // Index of the first visible step that hasn't been reached yet
  const firstPendingVisIdx = visible.findIndex(s => !stepReached[PIPELINE_STEPS.indexOf(s)])

  // Show skeleton if not yet started
  if (lines.length === 0 && !done) {
    return (
      <div className="space-y-1.5">
        <p className="text-xs text-muted-foreground mb-3 flex items-center gap-1.5">
          <Loader2 className="h-3 w-3 animate-spin" />
          Waiting for deployment to start…
        </p>
        <StepsSkeleton />
      </div>
    )
  }

  return (
    <div>
      {visible.map((step, visIdx) => {
        const globalIdx = PIPELINE_STEPS.indexOf(step)
        const reached = stepReached[globalIdx]
        const isCurrent = !done && !failed && visIdx === firstPendingVisIdx
        const isErrorStep = failed && visIdx === firstPendingVisIdx
        const isLastVisible = visIdx === visible.length - 1

        let state: 'done' | 'active' | 'error' | 'pending' | 'skip' = 'pending'
        if (reached) state = 'done'
        else if (isCurrent) state = 'active'
        else if (isErrorStep) state = 'error'

        return (
          <StepRow
            key={step.label}
            icon={step.icon}
            label={step.label}
            state={state}
            isLast={isLastVisible}
          />
        )
      })}
    </div>
  )
}

// ─── Result banner ────────────────────────────────────────────────────────────
function ResultBanner({ status }: { status?: string }) {
  if (status === 'success') {
    return (
      <div className="flex items-center gap-2.5 rounded-xl border border-emerald-500/30 bg-emerald-500/8 px-4 py-3">
        <CheckCircle2 className="h-5 w-5 text-emerald-400 shrink-0" />
        <div>
          <p className="text-sm font-semibold text-emerald-400">Deployment successful</p>
          <p className="text-xs text-emerald-400/70 mt-0.5">Your service is live and running.</p>
        </div>
      </div>
    )
  }
  if (status === 'failed') {
    return (
      <div className="flex items-center gap-2.5 rounded-xl border border-destructive/30 bg-destructive/8 px-4 py-3">
        <AlertTriangle className="h-5 w-5 text-destructive shrink-0" />
        <div>
          <p className="text-sm font-semibold text-destructive">Deployment failed</p>
          <p className="text-xs text-destructive/70 mt-0.5">Check the logs below for details.</p>
        </div>
      </div>
    )
  }
  return null
}

// ─── Helpers ──────────────────────────────────────────────────────────────────
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

// ─── Page ─────────────────────────────────────────────────────────────────────
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
      return s === 'pending' || s === 'running' ? 2000 : false
    },
    enabled: !!projectId && !!serviceId && !!deploymentId,
  })

  const { lines, done } = useDeploymentLogs(projectId!, serviceId!, deploymentId!)

  const isActive = deployment?.status === 'pending' || deployment?.status === 'running'
  const isFailed = deployment?.status === 'failed'
  const isSuccess = deployment?.status === 'success'

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
        <div className="grid gap-4 lg:grid-cols-[300px_1fr]">
          <div className="rounded-xl border bg-card p-5">
            <Skeleton className="h-4 w-24 mb-4" />
            <StepsSkeleton />
          </div>
          <Skeleton className="rounded-xl h-[600px]" />
        </div>
      ) : (
        <div className="grid gap-4 lg:grid-cols-[300px_1fr]">
          {/* ── Left panel: pipeline ──────────────────────────────────────── */}
          <div className="space-y-4">
            {/* Meta card */}
            <div className="rounded-xl border bg-card p-4 space-y-3">
              <div className="flex items-center gap-2 flex-wrap">
                <DeploymentStatusBadge status={deployment!.status} />
                {deployment?.commit_sha && (
                  <div className="flex items-center gap-1 font-mono text-[11px] text-muted-foreground bg-muted px-2 py-0.5 rounded">
                    <GitCommit className="h-3 w-3" />
                    {deployment.commit_sha.slice(0, 7)}
                  </div>
                )}
              </div>
              <div className="grid grid-cols-2 gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                <div className="flex items-center gap-1">
                  <Clock className="h-3 w-3 shrink-0" />
                  <span className="truncate">{formatDate(deployment?.started_at ?? deployment?.created_at)}</span>
                </div>
                <div className="text-right font-mono">
                  {formatDuration(deployment?.started_at, deployment?.finished_at)}
                </div>
              </div>
            </div>

            {/* Pipeline steps card */}
            <div className="rounded-xl border bg-card p-4">
              <p className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground mb-3">
                Deployment pipeline
              </p>
              <DeploymentPipeline
                lines={lines}
                done={done && !isFailed}
                failed={isFailed}
              />
            </div>

            {/* Result banner */}
            {(isSuccess || isFailed) && <ResultBanner status={deployment?.status} />}
          </div>

          {/* ── Right panel: logs ─────────────────────────────────────────── */}
          <div className="flex flex-col gap-2 min-h-0">
            <div className="flex items-center justify-between">
              <p className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                Deployment logs
              </p>
              {isActive && (
                <span className="flex items-center gap-1 text-[10px] text-primary animate-pulse">
                  <span className="h-1.5 w-1.5 rounded-full bg-primary inline-block" />
                  Live
                </span>
              )}
            </div>
            <LogViewer lines={lines} done={done} status={deployment?.status} className="flex-1 h-[580px]" />
          </div>
        </div>
      )}
    </div>
  )
}
