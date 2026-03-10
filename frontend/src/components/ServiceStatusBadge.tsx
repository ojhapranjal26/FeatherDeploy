import type { ServiceStatus } from '@/api/services'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const statusConfig: Record<ServiceStatus, { label: string; className: string }> = {
  running:   { label: 'Running',   className: 'bg-green-500/15 text-green-700 border-green-200' },
  deploying: { label: 'Deploying', className: 'bg-blue-500/15 text-blue-700 border-blue-200 animate-pulse' },
  error:     { label: 'Error',     className: 'bg-red-500/15 text-red-700 border-red-200' },
  stopped:   { label: 'Stopped',   className: 'bg-yellow-500/15 text-yellow-700 border-yellow-200' },
  inactive:  { label: 'Inactive',  className: 'bg-muted text-muted-foreground' },
}

export function ServiceStatusBadge({ status }: { status: ServiceStatus }) {
  const cfg = statusConfig[status] ?? statusConfig.inactive
  return (
    <Badge
      variant="outline"
      className={cn('text-xs font-medium', cfg.className)}
    >
      {cfg.label}
    </Badge>
  )
}
