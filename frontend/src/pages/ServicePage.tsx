import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Rocket, Clock } from 'lucide-react'
import { toast } from 'sonner'
import { servicesApi } from '@/api/services'
import { deploymentsApi } from '@/api/deployments'
import { ServiceStatusBadge } from '@/components/ServiceStatusBadge'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'

function formatDuration(start?: string, end?: string) {
  if (!start) return '—'
  const ms = new Date(end ?? Date.now()).getTime() - new Date(start).getTime()
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

export function ServicePage() {
  const { projectId, serviceId } = useParams<{ projectId: string; serviceId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const { data: service, isLoading } = useQuery({
    queryKey: ['service', projectId, serviceId],
    queryFn: () => servicesApi.get(projectId!, serviceId!),
    refetchInterval: 5000,
    enabled: !!projectId && !!serviceId,
  })

  const { data: deploymentsData } = useQuery({
    queryKey: ['deployments', serviceId],
    queryFn: () => deploymentsApi.list(projectId!, serviceId!, { limit: 5 }),
    refetchInterval: 5000,
    enabled: !!projectId && !!serviceId,
  })

  const deployMutation = useMutation({
    mutationFn: () =>
      deploymentsApi.trigger(projectId!, serviceId!, {
        deploy_type: service!.deploy_type,
        repo_url: service?.repo_url,
        repo_branch: service?.repo_branch,
      }),
    onSuccess: (data) => {
      toast.success('Deployment triggered.')
      qc.invalidateQueries({ queryKey: ['deployments', serviceId] })
      navigate(
        `/projects/${projectId}/services/${serviceId}/deployments/${data.deployment_id}`
      )
    },
    onError: () => toast.error('Failed to trigger deployment.'),
  })

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }

  if (!service) return null

  return (
    <div className="space-y-0">
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 gap-1.5 text-muted-foreground"
        onClick={() => navigate(`/projects/${projectId}`)}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> Back to project
      </Button>

      <div className="mb-6 flex items-center justify-between">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-semibold">{service.name}</h1>
          <ServiceStatusBadge status={service.status} />
          {service.framework && (
            <Badge variant="secondary">{service.framework}</Badge>
          )}
        </div>
        <Button
          className="gap-1.5"
          onClick={() => deployMutation.mutate()}
          disabled={deployMutation.isPending || service.status === 'deploying'}
        >
          <Rocket className="h-4 w-4" />
          Deploy now
        </Button>
      </div>

      <Tabs defaultValue="overview">
        <TabsList className="mb-6">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="deployments" onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments`)}>
              Deployments
          </TabsTrigger>
          <TabsTrigger value="env" onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/env`)}>
              Environment
          </TabsTrigger>
          <TabsTrigger value="domains" onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/domains`)}>
              Domains
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-6">
          {/* Info cards */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Status
                </CardTitle>
              </CardHeader>
              <CardContent>
                <ServiceStatusBadge status={service.status} />
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Host port
                </CardTitle>
              </CardHeader>
              <CardContent>
                <span className="font-mono text-lg">
                  {service.host_port ?? '—'}
                </span>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Deploy type
                </CardTitle>
              </CardHeader>
              <CardContent>
                <Badge variant="outline">{service.deploy_type}</Badge>
              </CardContent>
            </Card>
          </div>

          <Separator />

          {/* Configuration */}
          <div>
            <h2 className="mb-3 font-medium">Configuration</h2>
            <dl className="grid gap-y-2 text-sm sm:grid-cols-2">
              {[
                ['Repository', service.repo_url],
                ['Branch', service.repo_branch],
                ['Framework', service.framework],
                ['Build command', service.build_command],
                ['Start command', service.start_command],
                ['App port', service.app_port],
                ['Container ID', service.container_id],
              ].map(([label, val]) =>
                val ? (
                  <div key={String(label)} className="flex flex-col gap-0.5">
                    <dt className="text-xs text-muted-foreground">{label}</dt>
                    <dd className="font-mono text-xs break-all">{String(val)}</dd>
                  </div>
                ) : null
              )}
            </dl>
          </div>

          <Separator />

          {/* Recent deployments mini-list */}
          <div>
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-medium">Recent deployments</h2>
              <Button
                variant="link"
                size="sm"
                onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments`)}
              >
                View all
              </Button>
            </div>
            {deploymentsData?.deployments.slice(0, 5).map((d) => (
              <div
                key={d.id}
                className="flex items-center gap-3 py-2 border-b last:border-0 cursor-pointer hover:bg-muted/40 -mx-2 px-2 rounded"
                onClick={() =>
                  navigate(
                    `/projects/${projectId}/services/${serviceId}/deployments/${d.id}`
                  )
                }
              >
                <DeploymentStatusBadge status={d.status} />
                <span className="font-mono text-xs text-muted-foreground">
                  {d.commit_sha?.slice(0, 7) ?? d.deploy_type}
                </span>
                <div className="ml-auto flex items-center gap-1 text-xs text-muted-foreground">
                  <Clock className="h-3 w-3" />
                  {formatDuration(d.started_at, d.finished_at)}
                </div>
              </div>
            ))}
          </div>
        </TabsContent>
      </Tabs>
    </div>
  )
}
