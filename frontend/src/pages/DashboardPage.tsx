import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  type LucideIcon,
  Plus,
  FolderGit2,
  Activity,
  AlertTriangle,
  Rocket,
  ChevronRight,
  CheckCircle2,
  XCircle,
  Clock,
  Crown,
  Cpu,
  MemoryStick,
  HardDrive,
  Server,
  Wifi,
  WifiOff,
} from 'lucide-react'
import { toast } from 'sonner'
import { projectsApi } from '@/api/projects'
import type { Project } from '@/api/projects'
import client from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
import { useStatsSSE } from '@/hooks/useStatsSSE'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

// â”€â”€ Types â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
type SvcStatus = 'inactive' | 'deploying' | 'running' | 'error' | 'stopped'
type DashSvc = { id: number; project_id: number; name: string; status: SvcStatus }
type DashDep = {
  id: number
  service_id: number
  service_name: string
  status: 'success' | 'failed' | 'running' | 'pending'
  commit_sha?: string
  deploy_type: string
  created_at: string
}
type DashStats = {
  total_projects: number
  total_services: number
  running_services: number
  total_deployments: number
  failed_deployments: number
  services: DashSvc[]
  recent_deployments: DashDep[]
}

// â”€â”€ Helpers â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
function relativeTime(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

function svcStatusCls(status: SvcStatus) {
  const map: Record<string, string> = {
    running:   'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-0',
    error:     'bg-red-500/15 text-red-700 dark:text-red-400 border-0',
    stopped:   'bg-amber-500/15 text-amber-700 dark:text-amber-400 border-0',
    deploying: 'bg-blue-500/15 text-blue-700 dark:text-blue-400 border-0',
    inactive:  'bg-muted text-muted-foreground border-0',
  }
  return map[status] ?? 'bg-muted text-muted-foreground border-0'
}

function svcDotCls(status: SvcStatus) {
  const map: Record<string, string> = {
    running:   'bg-emerald-500',
    error:     'bg-red-500',
    stopped:   'bg-amber-500',
    deploying: 'bg-blue-500 animate-pulse',
    inactive:  'bg-muted-foreground/30',
  }
  return map[status] ?? 'bg-muted-foreground/30'
}

function depStatusCls(status: DashDep['status']) {
  const map: Record<string, string> = {
    success: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-0',
    failed:  'bg-red-500/15 text-red-700 dark:text-red-400 border-0',
    running: 'bg-blue-500/15 text-blue-700 dark:text-blue-400 border-0',
    pending: 'bg-muted text-muted-foreground border-0',
  }
  return map[status] ?? 'bg-muted text-muted-foreground border-0'
}

// â”€â”€ Schema â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
const schema = z.object({
  name:        z.string().min(2).max(63).regex(/^[a-z0-9-]+$/, 'Lowercase letters, numbers and hyphens only'),
  description: z.string().max(256).optional(),
})
type FormData = z.infer<typeof schema>

// â”€â”€ Sub-components â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
function StatCard({
  title,
  value,
  icon: Icon,
  iconCls,
  bgCls,
  loading,
}: {
  title: string
  value: number
  icon: LucideIcon
  iconCls: string
  bgCls: string
  loading?: boolean
}) {
  return (
    <Card className="border-border/60">
      <CardContent className="flex items-center gap-4 p-5">
        <div className={cn('flex h-10 w-10 shrink-0 items-center justify-center rounded-lg', bgCls)}>
          <Icon className={cn('h-5 w-5', iconCls)} />
        </div>
        <div>
          {loading ? (
            <Skeleton className="h-8 w-10 mb-1" />
          ) : (
            <p className="text-2xl font-bold tabular-nums">{value}</p>
          )}
          <p className="text-xs text-muted-foreground">{title}</p>
        </div>
      </CardContent>
    </Card>
  )
}

function NodeCard({ node }: { node: import('@/hooks/useStatsSSE').NodeStats }) {
  const isConn = node.status === 'connected'
  const stale = node.last_stats_at
    ? Date.now() - new Date(node.last_stats_at).getTime() > 30_000
    : true
  const gb = (v: number) => (v / 1024 / 1024 / 1024).toFixed(1)
  const pct = (u: number, t: number) => t > 0 ? Math.round((u / t) * 100) : 0
  const barCls = (p: number) =>
    p > 85 ? '[&>div]:bg-red-500' : p > 65 ? '[&>div]:bg-amber-500' : '[&>div]:bg-emerald-500'
  return (
    <div className="rounded-lg border border-border/60 bg-card/50 p-3 space-y-2">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1.5 min-w-0">
          <Server className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
          <span className="text-xs font-medium truncate">{node.name}</span>
        </div>
        <span className={cn(
          'inline-flex items-center gap-0.5 rounded-full px-1.5 py-0.5 text-[10px] font-medium border shrink-0',
          isConn
            ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-300/20'
            : 'bg-muted text-muted-foreground border-border',
        )}>
          <span className={cn('h-1.5 w-1.5 rounded-full', isConn ? 'bg-emerald-500' : 'bg-muted-foreground/40')} />
          {node.status}
        </span>
      </div>
      {!stale && isConn ? (
        <div className="space-y-1.5">
          <div className="flex items-center gap-2">
            <Cpu className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            <div className="flex-1">
              <div className="flex justify-between mb-0.5">
                <span className="text-xs text-muted-foreground">CPU</span>
                <span className="text-xs tabular-nums">{Math.round(node.cpu_usage)}%</span>
              </div>
              <Progress value={node.cpu_usage} className={cn('h-1', barCls(node.cpu_usage))} />
            </div>
          </div>
          {node.ram_total > 0 && (
            <div className="flex items-center gap-2">
              <MemoryStick className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <div className="flex-1">
                <div className="flex justify-between mb-0.5">
                  <span className="text-xs text-muted-foreground">RAM</span>
                  <span className="text-xs tabular-nums">{gb(node.ram_used)} / {gb(node.ram_total)} GB</span>
                </div>
                <Progress value={pct(node.ram_used, node.ram_total)} className={cn('h-1', barCls(pct(node.ram_used, node.ram_total)))} />
              </div>
            </div>
          )}
          {node.disk_total > 0 && (
            <div className="flex items-center gap-2">
              <HardDrive className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              <div className="flex-1">
                <div className="flex justify-between mb-0.5">
                  <span className="text-xs text-muted-foreground">Disk</span>
                  <span className="text-xs tabular-nums">{gb(node.disk_used)} / {gb(node.disk_total)} GB</span>
                </div>
                <Progress value={pct(node.disk_used, node.disk_total)} className={cn('h-1', barCls(pct(node.disk_used, node.disk_total)))} />
              </div>
            </div>
          )}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          {isConn ? 'Collecting stats…' : 'No stats available'}
        </p>
      )}
    </div>
  )
}

function ProjectCard({
  project,
  svcList,
}: {
  project: Project
  svcList: DashSvc[]
}) {
  const navigate = useNavigate()

  return (
    <Card
      className="group cursor-pointer border-border/60 transition-all duration-200 hover:border-primary/30 hover:shadow-md hover:-translate-y-px"
      onClick={() => navigate(`/projects/${project.id}`)}
    >
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <FolderGit2 className="h-4 w-4" />
            </div>
            <CardTitle className="text-sm font-semibold leading-tight">
              {project.name}
            </CardTitle>
          </div>
          <ChevronRight className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground/40 transition-colors group-hover:text-primary" />
        </div>
        {project.description && (
          <CardDescription className="ml-[2.625rem] text-xs line-clamp-2">
            {project.description}
          </CardDescription>
        )}
      </CardHeader>
      <CardContent className="pb-4 pt-0">
        <div className="ml-[2.625rem] flex flex-wrap gap-1.5">
          {svcList.length === 0 ? (
            <span className="text-xs text-muted-foreground">No services yet</span>
          ) : (
            svcList.map((svc) => (
              <Badge
                key={svc.id}
                variant="secondary"
                className={cn('gap-1 px-1.5 py-0 text-[10px]', svcStatusCls(svc.status))}
              >
                <span className={cn('h-1.5 w-1.5 rounded-full', svcDotCls(svc.status))} />
                {svc.name}
              </Badge>
            ))
          )}
        </div>
        <p className="ml-[2.625rem] mt-2.5 text-xs text-muted-foreground">
          Updated {relativeTime((project as Project & { updated_at: string }).updated_at)}
        </p>
      </CardContent>
    </Card>
  )
}

// â”€â”€ Page â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
export function DashboardPage() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)

  const { data: projects, isLoading: projectsLoading } = useQuery({
    queryKey: ['projects'],
    queryFn: projectsApi.list,
  })

  const { data: dash, isLoading: dashLoading } = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => client.get<DashStats>('/dashboard').then((r) => r.data),
    refetchInterval: 30_000,
  })

  const { brain, nodes: liveNodes, connected } = useStatsSSE()

  const createMutation = useMutation({
    mutationFn: projectsApi.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] })
      qc.invalidateQueries({ queryKey: ['dashboard'] })
      setOpen(false)
      reset()
      toast.success('Project created.')
    },
    onError: () => toast.error('Failed to create project.'),
  })

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormData>({ resolver: zodResolver(schema) })

  const services = dash?.services ?? []
  const svcByProject = services.reduce<Record<number, DashSvc[]>>((acc, svc) => {
    acc[svc.project_id] = [...(acc[svc.project_id] ?? []), svc]
    return acc
  }, {})

  const healthPct =
    services.length > 0
      ? Math.round(((dash?.running_services ?? 0) / services.length) * 100)
      : 0

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="text-sm text-muted-foreground">
            Monitor your projects, services, and deployments.
          </p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger render={<Button size="sm" className="gap-1.5" />}>
            <Plus className="h-4 w-4" /> New project
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create project</DialogTitle>
            </DialogHeader>
            <form
              onSubmit={handleSubmit((d) => createMutation.mutate(d))}
              className="space-y-4 pt-2"
            >
              <div className="space-y-1.5">
                <Label htmlFor="proj-name">Name</Label>
                <Input id="proj-name" placeholder="my-app" {...register('name')} />
                {errors.name && (
                  <p className="text-xs text-destructive">{errors.name.message}</p>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="proj-desc">Description (optional)</Label>
                <Textarea
                  id="proj-desc"
                  placeholder="Short description..."
                  rows={3}
                  {...register('description')}
                />
              </div>
              <div className="flex justify-end gap-2">
                <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                  Cancel
                </Button>
                <Button type="submit" disabled={isSubmitting || createMutation.isPending}>
                  Create
                </Button>
              </div>
            </form>
          </DialogContent>
        </Dialog>
      </div>

      {/* Stats row */}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard
          title="Total Projects"
          value={dash?.total_projects ?? 0}
          icon={FolderGit2}
          iconCls="text-indigo-600 dark:text-indigo-400"
          bgCls="bg-indigo-500/10"
          loading={dashLoading}
        />
        <StatCard
          title="Running Services"
          value={dash?.running_services ?? 0}
          icon={Activity}
          iconCls="text-emerald-600 dark:text-emerald-400"
          bgCls="bg-emerald-500/10"
          loading={dashLoading}
        />
        <StatCard
          title="Total Deployments"
          value={dash?.total_deployments ?? 0}
          icon={Rocket}
          iconCls="text-blue-600 dark:text-blue-400"
          bgCls="bg-blue-500/10"
          loading={dashLoading}
        />
        <StatCard
          title="Failed Deployments"
          value={dash?.failed_deployments ?? 0}
          icon={AlertTriangle}
          iconCls="text-red-600 dark:text-red-400"
          bgCls="bg-red-500/10"
          loading={dashLoading}
        />
      </div>

      {/* Main grid */}
      <div className="grid gap-6 lg:grid-cols-3">

        {/* Projects (spans 2 cols) */}
        <div className="space-y-3 lg:col-span-2">
          <div className="flex items-center justify-between">
            <h2 className="text-base font-semibold">Projects</h2>
            <span className="text-xs text-muted-foreground">
              {projects?.length ?? 0} total
            </span>
          </div>

          {projectsLoading ? (
            <div className="grid gap-3 sm:grid-cols-2">
              {[...Array(3)].map((_, i) => (
                <Card key={i} className="border-border/60">
                  <CardHeader className="pb-3">
                    <div className="flex items-center gap-2.5">
                      <Skeleton className="h-8 w-8 rounded-lg" />
                      <Skeleton className="h-4 w-28" />
                    </div>
                    <Skeleton className="ml-[2.625rem] h-3 w-40 mt-1" />
                  </CardHeader>
                  <CardContent className="pb-4 pt-0">
                    <div className="ml-[2.625rem] flex gap-1.5">
                      <Skeleton className="h-4 w-14 rounded-full" />
                      <Skeleton className="h-4 w-16 rounded-full" />
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>
          ) : projects?.length === 0 ? (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-16 text-center">
              <FolderGit2 className="mb-3 h-10 w-10 text-muted-foreground" />
              <p className="font-medium">No projects yet</p>
              <p className="mt-1 text-sm text-muted-foreground">
                Create your first project to get started.
              </p>
              <Button size="sm" className="mt-4 gap-1.5" onClick={() => setOpen(true)}>
                <Plus className="h-3.5 w-3.5" /> New project
              </Button>
            </div>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              {projects?.map((p) => (
                <ProjectCard
                  key={p.id}
                  project={p}
                  svcList={svcByProject[p.id as unknown as number] ?? []}
                />
              ))}
            </div>
          )}
        </div>

        {/* Right panel */}
        <div className="space-y-4">

          {/* Cluster Health */}
          {brain && (
            <Card className="border-border/60">
              <CardHeader className="pb-2">
                <div className="flex items-center justify-between">
                  <CardTitle className="text-sm font-semibold flex items-center gap-1.5">
                    <Server className="h-4 w-4" />
                    Cluster Health
                    {connected
                      ? <Wifi className="h-3 w-3 text-emerald-500 ml-1" title="Live" />
                      : <WifiOff className="h-3 w-3 text-muted-foreground ml-1" title="Reconnecting…" />
                    }
                  </CardTitle>
                  <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium border ${
                    brain.Alive
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-300/30'
                      : 'bg-red-500/15 text-red-600 dark:text-red-400 border-red-300/30'
                  }`}>
                    {brain.Alive ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
                    {brain.Alive ? 'Healthy' : 'Degraded'}
                  </span>
                </div>
                <CardDescription className="text-xs flex items-center gap-1">
                  <Crown className="h-3 w-3 text-amber-500" />
                  Brain: <span className="font-mono">{brain.BrainID || 'main'}</span>
                  {brain.LastHeartbeat && (
                    <span className="ml-1 text-muted-foreground">
                      · last beat {relativeTime(brain.LastHeartbeat)}
                    </span>
                  )}
                </CardDescription>
              </CardHeader>
              <CardContent className="pb-4 space-y-2">
                {/* CPU */}
                <div className="flex items-center gap-2">
                  <Cpu className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                  <div className="flex-1">
                    <div className="flex justify-between mb-0.5">
                      <span className="text-xs text-muted-foreground">CPU</span>
                      <span className="text-xs tabular-nums">{Math.round(brain.CPU ?? 0)}%</span>
                    </div>
                    <Progress value={brain.CPU ?? 0} className="h-1" />
                  </div>
                </div>
                {/* RAM */}
                {brain.RAMTotal > 0 && (
                  <div className="flex items-center gap-2">
                    <MemoryStick className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    <div className="flex-1">
                      <div className="flex justify-between mb-0.5">
                        <span className="text-xs text-muted-foreground">RAM</span>
                        <span className="text-xs tabular-nums">
                          {(brain.RAMUsed / 1024 / 1024 / 1024).toFixed(1)}
                          {' / '}
                          {(brain.RAMTotal / 1024 / 1024 / 1024).toFixed(1)} GB
                        </span>
                      </div>
                      <Progress value={Math.round((brain.RAMUsed / brain.RAMTotal) * 100)} className="h-1" />
                    </div>
                  </div>
                )}
                {/* Disk */}
                {brain.DiskTotal > 0 && (
                  <div className="flex items-center gap-2">
                    <HardDrive className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    <div className="flex-1">
                      <div className="flex justify-between mb-0.5">
                        <span className="text-xs text-muted-foreground">Disk</span>
                        <span className="text-xs tabular-nums">
                          {(brain.DiskUsed / 1024 / 1024 / 1024).toFixed(1)}
                          {' / '}
                          {(brain.DiskTotal / 1024 / 1024 / 1024).toFixed(1)} GB
                        </span>
                      </div>
                      <Progress value={Math.round((brain.DiskUsed / brain.DiskTotal) * 100)} className="h-1" />
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {/* Worker Nodes */}
          {liveNodes.length > 0 && (
            <Card className="border-border/60">
              <CardHeader className="pb-2">
                <CardTitle className="text-sm font-semibold flex items-center gap-1.5">
                  <Server className="h-4 w-4" />
                  Worker Nodes
                </CardTitle>
                <CardDescription className="text-xs">
                  {liveNodes.filter(n => n.status === 'connected').length} of {liveNodes.length} connected
                </CardDescription>
              </CardHeader>
              <CardContent className="pb-4 space-y-2">
                {liveNodes.map(n => <NodeCard key={n.id} node={n} />)}
              </CardContent>
            </Card>
          )}

          {/* Service health */}
          <Card className="border-border/60">
            <CardHeader className="pb-2">
              <CardTitle className="text-sm font-semibold">Service Health</CardTitle>
              <CardDescription className="text-xs">
                {dash?.running_services ?? 0} of {dash?.total_services ?? 0} services running
              </CardDescription>
            </CardHeader>
            <CardContent className="pb-4">
              <div className="mb-1 flex items-center justify-between">
                <span className="text-xs text-muted-foreground">Overall health</span>
                <span className="text-xs font-medium tabular-nums">{healthPct}%</span>
              </div>
              <Progress value={healthPct} className="h-1.5 mb-3" />
              <Separator className="mb-3" />
              <div className="space-y-2">
                {dashLoading && [...Array(3)].map((_, i) => (
                  <div key={i} className="flex items-center justify-between">
                    <Skeleton className="h-4 w-28" />
                    <Skeleton className="h-4 w-16 rounded-full" />
                  </div>
                ))}
                {services.map((svc) => (
                  <div key={svc.id} className="flex items-center justify-between">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className={cn('h-2 w-2 shrink-0 rounded-full', svcDotCls(svc.status))} />
                      <span className="truncate text-xs">{svc.name}</span>
                    </div>
                    <Badge
                      variant="secondary"
                      className={cn('shrink-0 px-1.5 py-0 text-[10px]', svcStatusCls(svc.status))}
                    >
                      {svc.status}
                    </Badge>
                  </div>
                ))}
                {!dashLoading && services.length === 0 && (
                  <p className="text-xs text-muted-foreground">No services yet</p>
                )}
              </div>
            </CardContent>
          </Card>

          {/* Recent deployments */}
          <Card className="border-border/60">
            <CardHeader className="pb-2">
              <CardTitle className="text-sm font-semibold">Recent Deployments</CardTitle>
            </CardHeader>
            <CardContent className="p-0 pb-1">
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="pl-4 text-[11px]">Service</TableHead>
                    <TableHead className="text-[11px]">Status</TableHead>
                    <TableHead className="pr-4 text-right text-[11px]">When</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(dash?.recent_deployments ?? []).slice(0, 5).map((dep) => (
                    <TableRow key={dep.id} className="hover:bg-muted/40">
                      <TableCell className="pl-4 py-2">
                        <div className="flex items-center gap-1.5">
                          {dep.status === 'success' ? (
                            <CheckCircle2 className="h-3 w-3 shrink-0 text-emerald-500" />
                          ) : dep.status === 'failed' ? (
                            <XCircle className="h-3 w-3 shrink-0 text-red-500" />
                          ) : (
                            <Clock className="h-3 w-3 shrink-0 text-muted-foreground" />
                          )}
                          <span className="max-w-[80px] truncate text-xs font-medium">
                            {dep.service_name || `svc-${dep.service_id}`}
                          </span>
                        </div>
                        {dep.commit_sha && (
                          <p className="mt-0.5 pl-[18px] font-mono text-[10px] text-muted-foreground">
                            {dep.commit_sha.slice(0, 7)}
                          </p>
                        )}
                      </TableCell>
                      <TableCell className="py-2">
                        <Badge
                          variant="secondary"
                          className={cn('px-1.5 py-0 text-[10px]', depStatusCls(dep.status))}
                        >
                          {dep.status}
                        </Badge>
                      </TableCell>
                      <TableCell className="py-2 pr-4 text-right text-[11px] text-muted-foreground">
                        {relativeTime(dep.created_at)}
                      </TableCell>
                    </TableRow>
                  ))}
                  {!dashLoading && (dash?.recent_deployments ?? []).length === 0 && (
                    <TableRow>
                      <TableCell colSpan={3} className="py-6 text-center text-xs text-muted-foreground">
                        No deployments yet
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

        </div>
      </div>
    </div>
  )
}
