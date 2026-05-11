import { useQuery } from '@tanstack/react-query'
import { databasesApi } from '@/api/databases'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { 
  CheckCircle2, 
  Circle, 
  Loader2, 
  XCircle, 
  Download, 
  RefreshCcw,
  ArrowRightLeft
} from 'lucide-react'
import { formatDistanceToNow } from 'date-fns'
import { Button } from './ui/button'

interface DatabaseTasksPanelProps {
  projectId: string | number
  databaseId: string | number
  enabled: boolean
  compact?: boolean
}

export function DatabaseTasksPanel({ projectId, databaseId, enabled, compact }: DatabaseTasksPanelProps) {
  const { data: allTasks, isLoading } = useQuery({
    queryKey: ['database-tasks', databaseId],
    queryFn: () => databasesApi.listTasks(projectId, databaseId),
    enabled,
    refetchInterval: (query) => {
      const hasActive = query.state.data?.some((t: any) => t.status === 'pending' || t.status === 'running')
      return hasActive ? 3000 : 10000
    }
  })

  const tasks = compact 
    ? allTasks?.filter((t: any) => t.status === 'pending' || t.status === 'running').slice(0, 1)
    : allTasks

  if (isLoading && !compact) {
    return <div className="flex justify-center p-4"><Loader2 className="h-6 w-6 animate-spin text-muted-foreground" /></div>
  }

  if (!tasks || tasks.length === 0) {
    return <p className="text-center text-sm text-muted-foreground py-4">No recent database tasks.</p>
  }

  return (
    <div className="space-y-3">
      {tasks.map((task: any) => (
        <div key={task.id} className="rounded-lg border bg-card p-3 text-card-foreground shadow-sm">
          <div className="flex items-start justify-between gap-3">
            <div className="flex items-start gap-2.5">
              <div className="mt-0.5">
                {task.task_type === 'backup' && <Download className="h-4 w-4 text-blue-500" />}
                {task.task_type === 'restore' && <RefreshCcw className="h-4 w-4 text-orange-500" />}
                {task.task_type === 'migrate' && <ArrowRightLeft className="h-4 w-4 text-purple-500" />}
              </div>
              <div>
                <p className="text-sm font-medium leading-none capitalize">
                  {task.task_type} {task.status === 'running' ? 'in progress...' : ''}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {task.started_at ? formatDistanceToNow(new Date(task.started_at + 'Z'), { addSuffix: true }) : 'Pending'}
                </p>
              </div>
            </div>
            <Badge 
              variant={
                task.status === 'completed' ? 'default' : 
                task.status === 'failed' ? 'destructive' : 
                'secondary'
              }
              className="capitalize text-[10px] px-1.5 py-0"
            >
              {task.status === 'running' && <Loader2 className="mr-1 h-2 w-2 animate-spin" />}
              {task.status}
            </Badge>
          </div>

          {(task.status === 'running' || task.status === 'completed') && (
            <div className="mt-3 space-y-1.5">
              <div className="flex justify-between text-[10px] text-muted-foreground font-medium">
                <span>Progress</span>
                <span>{Math.round(task.progress)}%</span>
              </div>
              <Progress value={task.progress} className="h-1.5" />
            </div>
          )}

          {task.error_message && (
            <p className="mt-2 text-[11px] text-destructive bg-destructive/5 rounded p-1.5 font-mono">
              {task.error_message}
            </p>
          )}

          {task.status === 'completed' && task.task_type === 'backup' && (
            <div className="mt-3">
              <Button 
                variant="outline" 
                size="sm" 
                className="w-full h-8 gap-2 text-xs"
                onClick={() => {
                  window.location.href = `/api/projects/${projectId}/databases/${databaseId}/tasks/${task.id}/download`
                }}
              >
                <Download className="h-3 w-3" /> Download Result
              </Button>
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
