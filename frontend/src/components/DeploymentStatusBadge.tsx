import type { DeploymentStatus } from '@/api/deployments'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const config: Record<DeploymentStatus, { label: string; className: string }> = {
  queued:   { label: 'Queued',   className: 'bg-muted text-muted-foreground' },
  building: { label: 'Building', className: 'bg-blue-500/15 text-blue-700 border-blue-200 animate-pulse' },
  running:  { label: 'Running',  className: 'bg-blue-500/15 text-blue-700 border-blue-200 animate-pulse' },
  success:  { label: 'Success',  className: 'bg-green-500/15 text-green-700 border-green-200' },
  failed:   { label: 'Failed',   className: 'bg-red-500/15 text-red-700 border-red-200' },
}

export function DeploymentStatusBadge({ status }: { status: DeploymentStatus }) {
  const cfg = config[status] ?? config.queued
  return (
    <Badge variant="outline" className={cn('text-xs font-medium', cfg.className)}>
      {cfg.label}
    </Badge>
  )
}
