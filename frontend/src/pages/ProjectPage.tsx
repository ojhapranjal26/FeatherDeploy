import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import {
  Plus, ChevronLeft, Rocket, Settings2, Trash2,
  ExternalLink, GitBranch, Terminal,
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
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const serviceSchema = z.object({
  name:        z.string().min(2).max(63).regex(/^[a-z0-9-]+$/, 'Lowercase, numbers, hyphens'),
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
      navigate(
        `/projects/${projectId}/services/${service.id}/deployments/${data.deployment_id}`
      )
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to trigger deployment.'),
  })

  const deleteMutation = useMutation({
    mutationFn: () => servicesApi.delete(projectId, service.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      toast.success('Service deleted.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to delete service.'),
  })

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <CardTitle className="text-base truncate">{service.name}</CardTitle>
              <ServiceStatusBadge status={service.status} />
            </div>
            {service.framework && (
              <Badge variant="secondary" className="mt-1 text-xs">
                {service.framework}
              </Badge>
            )}
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-6 w-6 shrink-0" />}>
              <Settings2 className="h-3.5 w-3.5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onClick={() =>
                  navigate(`/projects/${projectId}/services/${service.id}`)
                }
              >
                <Terminal className="mr-2 h-3.5 w-3.5" /> View service
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() =>
                  navigate(`/projects/${projectId}/services/${service.id}/env`)
                }
              >
                <Settings2 className="mr-2 h-3.5 w-3.5" /> Environment
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() =>
                  navigate(`/projects/${projectId}/services/${service.id}/domains`)
                }
              >
                <ExternalLink className="mr-2 h-3.5 w-3.5" /> Domains
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                className="text-destructive"
                onClick={() => {
                  if (confirm(`Delete service "${service.name}"?`)) {
                    deleteMutation.mutate()
                  }
                }}
              >
                <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {service.repo_url && (
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground truncate">
            <GitBranch className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{service.repo_url}</span>
            <span className="shrink-0">@{service.repo_branch}</span>
          </div>
        )}
        {service.host_port && (
          <div className="text-xs text-muted-foreground">
            Port:{' '}
            <span className="font-mono text-foreground">{service.host_port}</span>
          </div>
        )}
        <div className="flex gap-2">
          <Button
            size="sm"
            className="flex-1 gap-1.5"
            onClick={() => deployMutation.mutate()}
            disabled={deployMutation.isPending || service.status === 'deploying'}
          >
            <Rocket className="h-3.5 w-3.5" />
            Deploy
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              navigate(
                `/projects/${projectId}/services/${service.id}/deployments`
              )
            }
          >
            History
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

export function ProjectPage() {
  const { projectId } = useParams<{ projectId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [newServiceOpen, setNewServiceOpen] = useState(false)

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
    defaultValues: { deploy_type: 'git', repo_branch: 'main', app_port: 3000 },
  })

  const deployType = watch('deploy_type')

  const createSvcMutation = useMutation({
    mutationFn: (data: ServiceFormData) =>
      servicesApi.create(projectId!, {
        ...data,
        repo_url: data.repo_url || undefined,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      setNewServiceOpen(false)
      reset()
      toast.success('Service created.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to create service.'),
  })

  const deleteProjectMutation = useMutation({
    mutationFn: () => projectsApi.delete(projectId!),
    onSuccess: () => {
      navigate('/dashboard')
      toast.success('Project deleted.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to delete project.'),
  })

  return (
    <div className="space-y-6">
      {/* Back */}
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 gap-1.5 text-muted-foreground"
        onClick={() => navigate('/dashboard')}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> All projects
      </Button>

      {/* Project header */}
      {projLoading ? (
        <Skeleton className="h-8 w-48 mb-6" />
      ) : (
        <div className="mb-6 flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold">{project?.name}</h1>
            {project?.description && (
              <p className="text-sm text-muted-foreground">{project.description}</p>
            )}
          </div>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              className="gap-1.5"
              onClick={() => {
                if (confirm(`Delete project "${project?.name}" and all its services?`)) {
                  deleteProjectMutation.mutate()
                }
              }}
            >
              <Trash2 className="h-3.5 w-3.5" />
              Delete project
            </Button>
            <Button
              size="sm"
              className="gap-1.5"
              onClick={() => setNewServiceOpen(true)}
            >
              <Plus className="h-4 w-4" /> New service
            </Button>
          </div>
        </div>
      )}

      <Separator className="mb-6" />

      {/* Services grid */}
      {svcLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-4 w-28" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-8 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : services?.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-center">
          <Rocket className="mb-3 h-10 w-10 text-muted-foreground" />
          <p className="font-medium">No services yet</p>
          <p className="text-sm text-muted-foreground">
            Add a service to start deploying.
          </p>
          <Button
            size="sm"
            className="mt-4 gap-1.5"
            onClick={() => setNewServiceOpen(true)}
          >
            <Plus className="h-4 w-4" /> Add service
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {services?.map((s) => (
            <ServiceCard key={s.id} service={s} projectId={projectId!} />
          ))}
        </div>
      )}

      {/* New service dialog */}
      <Dialog open={newServiceOpen} onOpenChange={setNewServiceOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add service</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={handleSubmit((d) => createSvcMutation.mutate(d))}
            className="space-y-4 pt-2"
          >
            <div className="space-y-1.5">
              <Label>Name</Label>
              <Input placeholder="web" {...register('name')} />
              {errors.name && (
                <p className="text-xs text-destructive">{errors.name.message}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label>Deploy type</Label>
              <Select
                defaultValue="git"
                onValueChange={(v) =>
                  setValue('deploy_type', v as ServiceFormData['deploy_type'])
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="git">Git repository</SelectItem>
                  <SelectItem value="artifact">Artifact upload</SelectItem>
                  <SelectItem value="dockerfile">Dockerfile</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {deployType === 'git' && (
              <>
                <div className="space-y-1.5">
                  <Label>Repository URL</Label>
                  <Input
                    placeholder="https://github.com/you/repo"
                    {...register('repo_url')}
                  />
                  {errors.repo_url && (
                    <p className="text-xs text-destructive">{errors.repo_url.message}</p>
                  )}
                </div>
                <div className="space-y-1.5">
                  <Label>Branch</Label>
                  <Input placeholder="main" {...register('repo_branch')} />
                </div>
              </>
            )}

            <div className="space-y-1.5">
              <Label>App port (inside container)</Label>
              <Input type="number" placeholder="3000" {...register('app_port', { valueAsNumber: true })} />
            </div>

            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setNewServiceOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={isSubmitting || createSvcMutation.isPending}>
                Create service
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
