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
} from 'lucide-react'
import { toast } from 'sonner'
import { projectsApi } from '@/api/projects'
import type { Project } from '@/api/projects'
import { allServices, deployments as mockDeployments } from '@/api/_mock'
import type { MockService } from '@/api/_mock'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
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

// ── Types ───────────────────────────────────────────────────────────────────
type DepRecord = {
  id: string
  service_id: string
  status: 'success' | 'failed' | 'running' | 'pending'
  commit_sha?: string
  created_at: string
  deploy_type: string
}

// ── Static computations (mock data, no API call needed) ─────────────────────
const svcs = allServices()
const svcById = Object.fromEntries(svcs.map((s) => [s.id, s]))
const svcByProject = svcs.reduce<Record<string, MockService[]>>((acc, svc) => {
  acc[svc.project_id] = [...(acc[svc.project_id] ?? []), svc]
  return acc
}, {})

const allDeps = (Object.values(mockDeployments).flat() as DepRecord[]).sort(
  (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
)

const STATS = {
  totalProjects: 3,
  runningServices: svcs.filter((s) => s.status === 'running').length,
  totalDeployments: allDeps.length,
  failedDeployments: allDeps.filter((d) => d.status === 'failed').length,
}

// ── Helpers ─────────────────────────────────────────────────────────────────
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

function svcStatusCls(status: MockService['status']) {
  const map: Record<string, string> = {
    running:   'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-0',
    error:     'bg-red-500/15 text-red-700 dark:text-red-400 border-0',
    stopped:   'bg-amber-500/15 text-amber-700 dark:text-amber-400 border-0',
    deploying: 'bg-blue-500/15 text-blue-700 dark:text-blue-400 border-0',
    inactive:  'bg-muted text-muted-foreground border-0',
  }
  return map[status] ?? 'bg-muted text-muted-foreground border-0'
}

function svcDotCls(status: MockService['status']) {
  const map: Record<string, string> = {
    running:   'bg-emerald-500',
    error:     'bg-red-500',
    stopped:   'bg-amber-500',
    deploying: 'bg-blue-500 animate-pulse',
    inactive:  'bg-muted-foreground/30',
  }
  return map[status] ?? 'bg-muted-foreground/30'
}

function depStatusCls(status: DepRecord['status']) {
  const map: Record<string, string> = {
    success: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 border-0',
    failed:  'bg-red-500/15 text-red-700 dark:text-red-400 border-0',
    running: 'bg-blue-500/15 text-blue-700 dark:text-blue-400 border-0',
    pending: 'bg-muted text-muted-foreground border-0',
  }
  return map[status] ?? 'bg-muted text-muted-foreground border-0'
}

// ── Schema ───────────────────────────────────────────────────────────────────
const schema = z.object({
  name:        z.string().min(2).max(63).regex(/^[a-z0-9-]+$/, 'Lowercase letters, numbers and hyphens only'),
  description: z.string().max(256).optional(),
})
type FormData = z.infer<typeof schema>

// ── Sub-components ───────────────────────────────────────────────────────────
function StatCard({
  title,
  value,
  icon: Icon,
  iconCls,
  bgCls,
}: {
  title: string
  value: number
  icon: LucideIcon
  iconCls: string
  bgCls: string
}) {
  return (
    <Card className="border-border/60">
      <CardContent className="flex items-center gap-4 p-5">
        <div className={cn('flex h-10 w-10 shrink-0 items-center justify-center rounded-lg', bgCls)}>
          <Icon className={cn('h-5 w-5', iconCls)} />
        </div>
        <div>
          <p className="text-2xl font-bold tabular-nums">{value}</p>
          <p className="text-xs text-muted-foreground">{title}</p>
        </div>
      </CardContent>
    </Card>
  )
}

function ProjectCard({ project }: { project: Project }) {
  const navigate = useNavigate()
  const svcList = svcByProject[project.id] ?? []

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

// ── Page ─────────────────────────────────────────────────────────────────────
export function DashboardPage() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)

  const { data: projects, isLoading } = useQuery({
    queryKey: ['projects'],
    queryFn: projectsApi.list,
  })

  const createMutation = useMutation({
    mutationFn: projectsApi.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] })
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

  const healthPct = svcs.length > 0
    ? Math.round((STATS.runningServices / svcs.length) * 100)
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
          value={STATS.totalProjects}
          icon={FolderGit2}
          iconCls="text-indigo-600 dark:text-indigo-400"
          bgCls="bg-indigo-500/10"
        />
        <StatCard
          title="Running Services"
          value={STATS.runningServices}
          icon={Activity}
          iconCls="text-emerald-600 dark:text-emerald-400"
          bgCls="bg-emerald-500/10"
        />
        <StatCard
          title="Total Deployments"
          value={STATS.totalDeployments}
          icon={Rocket}
          iconCls="text-blue-600 dark:text-blue-400"
          bgCls="bg-blue-500/10"
        />
        <StatCard
          title="Failed Deployments"
          value={STATS.failedDeployments}
          icon={AlertTriangle}
          iconCls="text-red-600 dark:text-red-400"
          bgCls="bg-red-500/10"
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

          {isLoading ? (
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
              {projects?.map((p) => <ProjectCard key={p.id} project={p} />)}
            </div>
          )}
        </div>

        {/* Right panel */}
        <div className="space-y-4">

          {/* Service health */}
          <Card className="border-border/60">
            <CardHeader className="pb-2">
              <CardTitle className="text-sm font-semibold">Service Health</CardTitle>
              <CardDescription className="text-xs">
                {STATS.runningServices} of {svcs.length} services running
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
                {svcs.map((svc) => (
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
                  {allDeps.slice(0, 5).map((dep) => {
                    const svc = svcById[dep.service_id]
                    return (
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
                              {svc?.name ?? dep.service_id}
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
                    )
                  })}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

        </div>
      </div>
    </div>
  )
}

