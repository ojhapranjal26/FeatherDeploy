import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Plus, ChevronLeft, Rocket, Settings2, Trash2,
  ExternalLink, GitBranch, Terminal, GitFork, Package, FileCode2, Info,
  Globe, AlertTriangle,
} from 'lucide-react'
import { toast } from 'sonner'
import { projectsApi } from '@/api/projects'
import { servicesApi } from '@/api/services'
import { deploymentsApi } from '@/api/deployments'
import type { Service } from '@/api/services'
import { ServiceStatusBadge } from '@/components/ServiceStatusBadge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const serviceSchema = z.object({
  name:        z.string().min(2).max(63).regex(/^[a-z0-9-]+$/, 'Lowercase, numbers, hyphens only'),
  deploy_type: z.enum(['git', 'artifact', 'dockerfile']),
  repo_url:    z.string().refine(
    v => !v || /^(https?:\/\/|git@|git:\/\/)/.test(v),
    { message: 'Must be a valid HTTPS or SSH git URL' },
  ).optional().or(z.literal('')),
  repo_branch: z.string().optional(),
  app_port:    z.number().int().min(1).max(65535).optional(),
})
type ServiceFormData = z.infer<typeof serviceSchema>

function ServiceCard({ service, projectId }: { service: Service; projectId: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [confirmDelete, setConfirmDelete] = useState(false)

  const deployMutation = useMutation({
    mutationFn: () =>
      deploymentsApi.trigger(projectId, service.id, {
        deploy_type: service.deploy_type,
        repo_url: service.repo_url,
        repo_branch: service.repo_branch,
      }),
    onSuccess: (data) => {
      toast.success('Deployment triggered.')
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      navigate(`/projects/${projectId}/services/${service.id}/deployments/${data.deployment_id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to trigger deployment.'),
  })

  const deleteMutation = useMutation({
    mutationFn: () => servicesApi.delete(projectId, service.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      toast.success('Service deleted. Container cleanup running in background.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to delete service.'),
  })

  const isDeploying = service.status === 'deploying'

  return (
    <>
      <Card className="group relative flex flex-col overflow-hidden transition-shadow hover:shadow-md">
        {/* Status strip */}
        <div className={cn(
          'absolute inset-x-0 top-0 h-0.5',
          service.status === 'running'   ? 'bg-emerald-500' :
          service.status === 'deploying' ? 'bg-amber-400 animate-pulse' :
          service.status === 'error'     ? 'bg-red-500' :
          'bg-border'
        )} />
        <CardHeader className="pb-2 pt-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <button
                className="text-left w-full"
                onClick={() => navigate(`/projects/${projectId}/services/${service.id}`)}
              >
                <CardTitle className="text-sm font-semibold truncate hover:text-primary transition-colors">
                  {service.name}
                </CardTitle>
              </button>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                <ServiceStatusBadge status={service.status} />
                {service.framework && (
                  <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                    {service.framework}
                  </Badge>
                )}
                <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                  {service.deploy_type}
                </Badge>
              </div>
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" />}>
                <Settings2 className="h-3.5 w-3.5" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}`)}>
                  <Terminal className="mr-2 h-3.5 w-3.5" /> View service
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/env`)}>
                  <Settings2 className="mr-2 h-3.5 w-3.5" /> Environment
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/domains`)}>
                  <Globe className="mr-2 h-3.5 w-3.5" /> Domains
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  onClick={() => setConfirmDelete(true)}
                >
                  <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete service
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </CardHeader>

        <CardContent className="flex flex-col gap-3 flex-1 pb-4">
          {service.repo_url && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground overflow-hidden">
              <GitBranch className="h-3 w-3 shrink-0" />
              <span className="truncate font-mono text-[11px]">{repoShort(service.repo_url)}</span>
              {service.repo_branch && (
                <span className="shrink-0 text-muted-foreground">@ {service.repo_branch}</span>
              )}
            </div>
          )}

          {service.host_port ? (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <ExternalLink className="h-3 w-3 shrink-0" />
              <span>Port <span className="font-mono text-foreground">{service.host_port}</span></span>
            </div>
          ) : null}

          <div className="mt-auto flex gap-2">
            <Button
              size="sm"
              className="flex-1 gap-1.5 text-xs h-8"
              onClick={() => deployMutation.mutate()}
              disabled={deployMutation.isPending || isDeploying}
            >
              {isDeploying ? (
                <span className="flex items-center gap-1.5">
                  <span className="h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
                  Deploying…
                </span>
              ) : (
                <><Rocket className="h-3 w-3" /> Deploy</>
              )}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-xs h-8 px-3"
              onClick={() => navigate(`/projects/${projectId}/services/${service.id}/deployments`)}
            >
              History
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Confirm delete dialog */}
      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete service
            </DialogTitle>
            <DialogDescription>
              This will permanently delete <strong>{service.name}</strong>, stop its container,
              remove all images, domains, deployments and environment variables.
              This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="ghost" size="sm" onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button
              variant="destructive" size="sm"
              disabled={deleteMutation.isPending}
              onClick={() => { setConfirmDelete(false); deleteMutation.mutate() }}
            >
              {deleteMutation.isPending ? 'Deleting…' : 'Delete service'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function repoShort(url: string) {
  try {
    // https://github.com/user/repo.git → user/repo
    const m = url.match(/[:/]([^:/]+\/[^/]+?)(\.git)?$/)
    return m ? m[1] : url
  } catch {
    return url
  }
}

export function ProjectPage() {
  const { projectId } = useParams<{ projectId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [newServiceOpen, setNewServiceOpen] = useState(false)
  const [confirmDeleteProject, setConfirmDeleteProject] = useState(false)

  const { data: project, isLoading: projLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => projectsApi.get(projectId!),
    enabled: !!projectId,
  })

  const { data: services, isLoading: svcLoading } = useQuery({
    queryKey: ['services', projectId],
    queryFn: () => servicesApi.list(projectId!),
    enabled: !!projectId,
    refetchInterval: 5000,
  })

  const {
    register,
    handleSubmit,
    watch,
    setValue,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ServiceFormData>({
    resolver: zodResolver(serviceSchema),
    defaultValues: { deploy_type: 'git', repo_branch: 'main' },
  })

  const deployType = watch('deploy_type')

  const createSvcMutation = useMutation({
    mutationFn: (data: ServiceFormData) =>
      servicesApi.create(projectId!, {
        ...data,
        repo_url: data.repo_url || undefined,
      }),
    onSuccess: (service) => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      setNewServiceOpen(false)
      reset()
      toast.success('Service created — click Deploy to start your first deployment.')
      navigate(`/projects/${projectId}/services/${service.id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to create service.'),
  })

  const deleteProjectMutation = useMutation({
    mutationFn: () => projectsApi.delete(projectId!),
    onSuccess: () => {
      navigate('/dashboard')
      toast.success('Project deleted.')
    },
    onError: (err: unknown) => {
      setConfirmDeleteProject(false)
      toast.error((err as any)?.response?.data?.error ?? 'Failed to delete project.')
    },
  })

  const hasServices = (services?.length ?? 0) > 0

  return (
    <div className="space-y-6 pb-8">
      <Button
        variant="ghost"
        size="sm"
        className="gap-1.5 text-muted-foreground"
        onClick={() => navigate('/dashboard')}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> All projects
      </Button>

      {projLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-72" />
        </div>
      ) : (
        <div className="flex items-start justify-between gap-4 flex-wrap">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">{project?.name}</h1>
            {project?.description && (
              <p className="mt-1 text-sm text-muted-foreground">{project.description}</p>
            )}
            <p className="mt-1 text-xs text-muted-foreground">
              {services?.length ?? 0} service{(services?.length ?? 0) !== 1 ? 's' : ''}
            </p>
          </div>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10"
              onClick={() => setConfirmDeleteProject(true)}
            >
              <Trash2 className="h-3.5 w-3.5" /> Delete project
            </Button>
            <Button size="sm" className="gap-1.5" onClick={() => setNewServiceOpen(true)}>
              <Plus className="h-4 w-4" /> New service
            </Button>
          </div>
        </div>
      )}

      <Separator />

      {/* Services grid */}
      {svcLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Card key={i} className="h-36">
              <CardHeader><Skeleton className="h-4 w-24" /></CardHeader>
              <CardContent><Skeleton className="h-8 w-full" /></CardContent>
            </Card>
          ))}
        </div>
      ) : services?.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-24 text-center">
          <div className="mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-muted">
            <Rocket className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-base font-medium">No services yet</p>
          <p className="mt-1 text-sm text-muted-foreground max-w-xs">
            Create a service to start deploying your applications as containers.
          </p>
          <Button size="sm" className="mt-5 gap-1.5" onClick={() => setNewServiceOpen(true)}>
            <Plus className="h-4 w-4" /> Create first service
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {services?.map((s) => (
            <ServiceCard key={s.id} service={s} projectId={projectId!} />
          ))}
        </div>
      )}

      {/* ── New service dialog ─────────────────────────────────────────── */}
      <Dialog open={newServiceOpen} onOpenChange={(o) => { setNewServiceOpen(o); if (!o) reset() }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Rocket className="h-4 w-4 text-primary" /> New service
            </DialogTitle>
            <DialogDescription>
              Define your service source and configure the build. Domains and
              environment variables can be added after creation.
            </DialogDescription>
          </DialogHeader>

          <form
            onSubmit={handleSubmit((d) => createSvcMutation.mutate(d))}
            className="space-y-4 pt-1"
          >
            {/* Service name */}
            <div className="space-y-1.5">
              <Label htmlFor="svc-name">
                Service name <span className="text-destructive">*</span>
              </Label>
              <Input
                id="svc-name"
                placeholder="e.g. web, api, worker"
                autoFocus
                {...register('name')}
              />
              <p className="text-xs text-muted-foreground">
                Lowercase letters, numbers and hyphens only. Becomes the container name.
              </p>
              {errors.name && <p className="text-xs text-destructive">{errors.name.message}</p>}
            </div>

            {/* Deploy type */}
            <div className="space-y-2">
              <Label>Deploy from</Label>
              <div className="grid grid-cols-3 gap-2">
                {([
                  { value: 'git',        icon: GitFork,   label: 'Git repo',   desc: 'Clone & build' },
                  { value: 'dockerfile', icon: FileCode2, label: 'Dockerfile', desc: 'Custom image' },
                  { value: 'artifact',   icon: Package,   label: 'Artifact',   desc: 'Upload binary' },
                ] as const).map(({ value, icon: Icon, label, desc }) => (
                  <button
                    key={value}
                    type="button"
                    onClick={() => setValue('deploy_type', value)}
                    className={cn(
                      'flex flex-col items-center gap-1.5 rounded-lg border p-3 text-center transition-all',
                      deployType === value
                        ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
                        : 'border-border hover:border-muted-foreground/40',
                    )}
                  >
                    <Icon className={cn('h-5 w-5', deployType === value ? 'text-primary' : 'text-muted-foreground')} />
                    <span className="text-xs font-medium leading-none">{label}</span>
                    <span className="text-[10px] text-muted-foreground leading-none">{desc}</span>
                  </button>
                ))}
              </div>
            </div>

            {/* Git-specific fields */}
            {deployType === 'git' && (
              <div className="rounded-lg border bg-muted/30 p-3.5 space-y-3">
                <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Repository</p>
                <div className="space-y-1.5">
                  <Label htmlFor="svc-repo-url">Repository URL</Label>
                  <Input
                    id="svc-repo-url"
                    placeholder="https://github.com/you/repo  or  git@github.com:you/repo.git"
                    {...register('repo_url')}
                  />
                  <p className="text-xs text-muted-foreground flex items-start gap-1">
                    <Info className="h-3 w-3 mt-0.5 shrink-0" />
                    For private repos, add an SSH key in{' '}
                    <strong className="text-foreground">Settings → SSH Keys</strong>{' '}
                    and use an SSH URL.
                  </p>
                  {errors.repo_url && <p className="text-xs text-destructive">{errors.repo_url.message}</p>}
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="svc-branch">Branch</Label>
                  <Input id="svc-branch" placeholder="main" {...register('repo_branch')} />
                </div>
              </div>
            )}

            {deployType === 'dockerfile' && (
              <div className="rounded-lg border bg-muted/30 p-3.5 space-y-1.5">
                <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Note</p>
                <p className="text-xs text-muted-foreground">
                  The Dockerfile at the root of your repository will be used. Provide the
                  repo URL above after switching to a Git source, or deploy after creation.
                </p>
              </div>
            )}

            {/* App port */}
            <div className="space-y-1.5">
              <Label htmlFor="svc-port">
                App port{' '}
                <span className="text-muted-foreground font-normal">(optional)</span>
              </Label>
              <Input
                id="svc-port"
                type="number"
                placeholder="3000  —  defaults to 8080 if left blank"
                {...register('app_port', { valueAsNumber: true })}
              />
              <p className="text-xs text-muted-foreground">
                The port your app listens on <em>inside</em> the container.
              </p>
            </div>

            <div className="flex justify-end gap-2 pt-2">
              <Button type="button" variant="outline" size="sm" onClick={() => { setNewServiceOpen(false); reset() }}>
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={isSubmitting || createSvcMutation.isPending} className="gap-1.5">
                <Rocket className="h-3.5 w-3.5" />
                {createSvcMutation.isPending ? 'Creating…' : 'Create service'}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>

      {/* ── Confirm delete project ─────────────────────────────────────── */}
      <Dialog open={confirmDeleteProject} onOpenChange={setConfirmDeleteProject}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete project
            </DialogTitle>
            <DialogDescription>
              {hasServices ? (
                <span className="text-destructive font-medium">
                  This project has {services?.length} active service{services?.length !== 1 ? 's' : ''}.
                  Delete all services first to clean up their containers before deleting the project.
                </span>
              ) : (
                <>Permanently delete <strong>{project?.name}</strong>? This cannot be undone.</>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="ghost" size="sm" onClick={() => setConfirmDeleteProject(false)}>Cancel</Button>
            <Button
              variant="destructive" size="sm"
              disabled={hasServices || deleteProjectMutation.isPending}
              onClick={() => deleteProjectMutation.mutate()}
            >
              {deleteProjectMutation.isPending ? 'Deleting…' : 'Delete project'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
