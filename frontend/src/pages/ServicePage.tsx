import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Rocket, Clock, Search, Loader2, CheckCircle2 } from 'lucide-react'
import { toast } from 'sonner'
import { servicesApi, type DetectionResult } from '@/api/services'
import { deploymentsApi } from '@/api/deployments'
import { ServiceStatusBadge } from '@/components/ServiceStatusBadge'
import { DeploymentStatusBadge } from '@/components/DeploymentStatusBadge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter,
  DialogHeader, DialogTitle,
} from '@/components/ui/dialog'

function formatDuration(start?: string, end?: string) {
  if (!start) return '—'
  const ms = new Date(end ?? Date.now()).getTime() - new Date(start).getTime()
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

// ─── Detection confirmation modal ────────────────────────────────────────────

interface DetectModalProps {
  open: boolean
  detecting: boolean
  result: DetectionResult | null
  onEdit: (field: keyof DetectionResult, value: string) => void
  onConfirmDeploy: () => void
  onClose: () => void
}

function DetectModal({ open, detecting, result, onEdit, onConfirmDeploy, onClose }: DetectModalProps) {
  const frameworkLabel = result
    ? `${result.language.charAt(0).toUpperCase() + result.language.slice(1)} · ${result.framework}`
    : ''

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Confirm detected stack</DialogTitle>
          <DialogDescription>
            FeatherDeploy analysed your repository and detected the following configuration.
            Review and edit before your first deploy.
          </DialogDescription>
        </DialogHeader>

        {detecting ? (
          <div className="flex flex-col items-center gap-3 py-8 text-muted-foreground">
            <Loader2 className="h-6 w-6 animate-spin" />
            <p className="text-sm">Cloning repository and analysing stack…</p>
          </div>
        ) : result ? (
          <div className="space-y-4 py-2">
            {/* Detected badge */}
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">{frameworkLabel}</span>
              {result.version && (
                <Badge variant="secondary" className="font-mono text-xs">{result.version}</Badge>
              )}
              <Badge variant="outline" className="ml-auto font-mono text-[10px]">{result.base_image}</Badge>
            </div>

            <Separator />

            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="det-build" className="text-xs text-muted-foreground">Build command</Label>
                <Input
                  id="det-build"
                  className="font-mono text-xs h-8"
                  value={result.build_command}
                  onChange={(e) => onEdit('build_command', e.target.value)}
                  placeholder="e.g. npm ci && npm run build"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="det-start" className="text-xs text-muted-foreground">Start command</Label>
                <Input
                  id="det-start"
                  className="font-mono text-xs h-8"
                  value={result.start_command}
                  onChange={(e) => onEdit('start_command', e.target.value)}
                  placeholder="e.g. node dist/index.js"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="det-port" className="text-xs text-muted-foreground">App port</Label>
                <Input
                  id="det-port"
                  type="number"
                  className="font-mono text-xs h-8 w-28"
                  value={result.app_port}
                  onChange={(e) => onEdit('app_port', e.target.value)}
                />
              </div>
            </div>

            <p className="text-xs text-muted-foreground">
              These values will be saved to the service and used for all future deployments.
              You can change them later in the service settings.
            </p>
          </div>
        ) : null}

        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>Cancel</Button>
          <Button
            size="sm"
            onClick={onConfirmDeploy}
            disabled={detecting || !result}
            className="gap-1.5"
          >
            <Rocket className="h-3.5 w-3.5" />
            Confirm &amp; Deploy
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Main page ────────────────────────────────────────────────────────────────

export function ServicePage() {
  const { projectId, serviceId } = useParams<{ projectId: string; serviceId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()

  // Detection modal state
  const [detectOpen, setDetectOpen] = useState(false)
  const [detecting, setDetecting] = useState(false)
  const [detection, setDetection] = useState<DetectionResult | null>(null)

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

  const updateMutation = useMutation({
    mutationFn: (data: Parameters<typeof servicesApi.update>[2]) =>
      servicesApi.update(projectId!, serviceId!, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['service', projectId, serviceId] }),
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

  // needsDetection: true when deploy_type is git but framework/commands not set
  const needsDetection =
    service?.deploy_type === 'git' &&
    (!service?.framework || !service?.build_command || !service?.start_command)

  const handleDeployClick = async () => {
    if (!service) return
    if (needsDetection) {
      // Open modal and start detection
      setDetectOpen(true)
      setDetecting(true)
      setDetection(null)
      try {
        const result = await servicesApi.detect(projectId!, serviceId!)
        setDetection(result)
      } catch (e: unknown) {
        toast.error(e instanceof Error ? e.message : 'Stack detection failed.')
        setDetectOpen(false)
      } finally {
        setDetecting(false)
      }
    } else {
      deployMutation.mutate()
    }
  }

  const handleConfirmDeploy = async () => {
    if (!detection) return
    try {
      // Save detected values to service
      await updateMutation.mutateAsync({
        framework: detection.framework,
        build_command: detection.build_command,
        start_command: detection.start_command,
        app_port: detection.app_port,
      })
      setDetectOpen(false)
      deployMutation.mutate()
    } catch {
      toast.error('Failed to save detected configuration.')
    }
  }

  const handleEditDetection = (field: keyof DetectionResult, value: string) => {
    if (!detection) return
    setDetection({
      ...detection,
      [field]: field === 'app_port' ? parseInt(value, 10) || detection.app_port : value,
    })
  }

  // Manual re-detect button (for services that already have commands set)
  const handleReDetect = async () => {
    setDetectOpen(true)
    setDetecting(true)
    setDetection(null)
    try {
      const result = await servicesApi.detect(projectId!, serviceId!)
      setDetection(result)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Stack detection failed.')
      setDetectOpen(false)
    } finally {
      setDetecting(false)
    }
  }

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }

  if (!service) return null

  const isDeploying = deployMutation.isPending || service.status === 'deploying'

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
        <div className="flex items-center gap-2">
          {service.deploy_type === 'git' && service.repo_url && (
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-xs"
              onClick={handleReDetect}
              disabled={isDeploying}
            >
              <Search className="h-3.5 w-3.5" />
              Detect stack
            </Button>
          )}
          <Button
            className="gap-1.5"
            onClick={handleDeployClick}
            disabled={isDeploying}
          >
            {isDeploying ? (
              <><Loader2 className="h-4 w-4 animate-spin" /> Deploying…</>
            ) : (
              <><Rocket className="h-4 w-4" /> Deploy now</>
            )}
          </Button>
        </div>
      </div>

      {/* Detection confirmation modal */}
      <DetectModal
        open={detectOpen}
        detecting={detecting}
        result={detection}
        onEdit={handleEditDetection}
        onConfirmDeploy={handleConfirmDeploy}
        onClose={() => setDetectOpen(false)}
      />

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
            {needsDetection && (
              <div className="mb-3 flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
                <Search className="h-3.5 w-3.5 shrink-0" />
                Framework and commands not set — click <strong>Detect stack</strong> or <strong>Deploy now</strong> to auto-detect.
              </div>
            )}
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
