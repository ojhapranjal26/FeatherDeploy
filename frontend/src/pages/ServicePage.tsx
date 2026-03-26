import { useEffect, useRef, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Rocket, Clock, Search, Loader2, CheckCircle2, Plus, Trash2, Eye, EyeOff, ExternalLink, Terminal, Code2, CircleDot, Cpu, MemoryStick, Network, HardDrive, Activity } from 'lucide-react'
import {
  AreaChart, Area, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid,
} from 'recharts'
import { toast } from 'sonner'
import { servicesApi, type DetectionResult } from '@/api/services'
import { useContainerLogs } from '@/hooks/useContainerLogs'
import { useContainerStatsSSE } from '@/hooks/useContainerStatsSSE'
import { deploymentsApi } from '@/api/deployments'
import { envApi, type UpsertEnvPayload } from '@/api/env'
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

  // Tab state — track active tab so the container-logs SSE only connects when visible
  const [activeTab, setActiveTab] = useState('overview')

  // Env state
  const [addEnvOpen, setAddEnvOpen] = useState(false)
  const [envKey, setEnvKey] = useState('')
  const [envValue, setEnvValue] = useState('')
  const [envIsSecret, setEnvIsSecret] = useState(true)
  const [revealedIds, setRevealedIds] = useState<Set<number>>(new Set())

  const { data: service, isLoading } = useQuery({
    queryKey: ['service', projectId, serviceId],
    queryFn: () => servicesApi.get(projectId!, serviceId!),
    refetchInterval: 5000,
    enabled: !!projectId && !!serviceId,
  })

  const { data: deploymentsData } = useQuery({
    queryKey: ['deployments', serviceId],
    queryFn: () => deploymentsApi.list(projectId!, serviceId!, { limit: 20 }),
    refetchInterval: 5000,
    enabled: !!projectId && !!serviceId,
  })

  const { data: envVars, isLoading: envLoading } = useQuery({
    queryKey: ['env', serviceId],
    queryFn: () => envApi.list(projectId!, serviceId!),
    enabled: !!projectId && !!serviceId,
  })

  const updateMutation = useMutation({
    mutationFn: (data: Parameters<typeof servicesApi.update>[2]) =>
      servicesApi.update(projectId!, serviceId!, data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['service', projectId, serviceId] }),
  })

  const upsertEnvMutation = useMutation({
    mutationFn: (d: UpsertEnvPayload) => envApi.upsert(projectId!, serviceId!, d),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      setEnvKey('')
      setEnvValue('')
      setAddEnvOpen(false)
      toast.success('Variable saved.')
    },
    onError: () => toast.error('Failed to save variable.'),
  })

  const deleteEnvMutation = useMutation({
    mutationFn: (key: string) => envApi.delete(projectId!, serviceId!, key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      toast.success('Variable removed.')
    },
    onError: () => toast.error('Failed to remove variable.'),
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
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to trigger deployment.'),
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

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList className="mb-6">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="deployments">Deployments</TabsTrigger>
          <TabsTrigger value="env">Environment</TabsTrigger>
          <TabsTrigger value="logs">Live Logs</TabsTrigger>
          <TabsTrigger value="stats">Stats</TabsTrigger>
          <TabsTrigger value="domains" onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/domains`)}>
            Domains ↗
          </TabsTrigger>
        </TabsList>

        {/* ── Overview ─────────────────────────────────────────────────────── */}
        <TabsContent value="overview" className="space-y-6">
          {/* Info cards */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Status</CardTitle>
              </CardHeader>
              <CardContent><ServiceStatusBadge status={service.status} /></CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Host port</CardTitle>
              </CardHeader>
              <CardContent>
                <span className="font-mono text-lg">{service.host_port || '—'}</span>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Deploy type</CardTitle>
              </CardHeader>
              <CardContent><Badge variant="outline">{service.deploy_type}</Badge></CardContent>
            </Card>
          </div>

          <Separator />

          {/* Build configuration */}
          <div>
            <h2 className="mb-4 font-medium flex items-center gap-2">
              <Code2 className="h-4 w-4 text-muted-foreground" /> Build configuration
            </h2>
            {needsDetection && (
              <div className="mb-4 flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2.5 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
                <Search className="h-3.5 w-3.5 shrink-0" />
                Framework not detected yet — click <strong className="mx-1">Detect stack</strong> or <strong className="mx-1">Deploy now</strong> to auto-analyse your repo.
              </div>
            )}
            <div className="grid gap-3 sm:grid-cols-2">
              {service.repo_url && (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Repository</p>
                  <p className="font-mono text-xs break-all">{service.repo_url}</p>
                  {service.repo_branch && <p className="text-xs text-muted-foreground">Branch: <span className="font-mono">{service.repo_branch}</span></p>}
                </div>
              )}
              {service.framework && (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Detected stack</p>
                  <Badge variant="secondary" className="text-xs">{service.framework}</Badge>
                </div>
              )}
              {service.build_command && (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Build command</p>
                  <pre className="font-mono text-xs whitespace-pre-wrap break-all text-foreground">{service.build_command}</pre>
                </div>
              )}
              {service.start_command && (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Start command</p>
                  <pre className="font-mono text-xs whitespace-pre-wrap break-all text-foreground">{service.start_command}</pre>
                </div>
              )}
              {service.app_port ? (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">App port (inside container)</p>
                  <p className="font-mono text-sm">{service.app_port}</p>
                </div>
              ) : null}
              {service.container_id && (
                <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                  <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Container ID</p>
                  <p className="font-mono text-xs break-all">{service.container_id}</p>
                </div>
              )}
            </div>
          </div>

          <Separator />

          {/* Recent deployments mini-list */}
          <div>
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-medium flex items-center gap-2"><Terminal className="h-4 w-4 text-muted-foreground" /> Recent deployments</h2>
            </div>
            {deploymentsData?.deployments.length === 0 && (
              <p className="text-sm text-muted-foreground">No deployments yet. Click <strong>Deploy now</strong> to start.</p>
            )}
            {deploymentsData?.deployments.slice(0, 5).map((d) => (
              <div
                key={d.id}
                className="flex items-center gap-3 py-2.5 border-b last:border-0 cursor-pointer hover:bg-muted/40 -mx-2 px-2 rounded"
                onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments/${d.id}`)}
              >
                <DeploymentStatusBadge status={d.status} />
                <span className="font-mono text-xs text-muted-foreground">{d.commit_sha?.slice(0, 7) ?? d.deploy_type}</span>
                <div className="ml-auto flex items-center gap-1 text-xs text-muted-foreground">
                  <Clock className="h-3 w-3" />
                  {formatDuration(d.started_at, d.finished_at)}
                </div>
              </div>
            ))}
          </div>
        </TabsContent>

        {/* ── Deployments ──────────────────────────────────────────────────── */}
        <TabsContent value="deployments" className="space-y-4">
          <div className="flex items-center justify-between">
            <h2 className="font-medium">All deployments</h2>
            <Button size="sm" className="gap-1.5" onClick={handleDeployClick} disabled={isDeploying}>
              {isDeploying ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Deploying…</> : <><Rocket className="h-3.5 w-3.5" /> Deploy now</>}
            </Button>
          </div>
          {deploymentsData?.deployments.length === 0 && (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-14 text-center">
              <Rocket className="mb-2 h-8 w-8 text-muted-foreground" />
              <p className="text-sm font-medium">No deployments yet</p>
              <p className="text-xs text-muted-foreground mt-1">Trigger your first deployment above.</p>
            </div>
          )}
          <div className="divide-y rounded-lg border">
            {deploymentsData?.deployments.map((d) => (
              <div
                key={d.id}
                className="flex items-center gap-3 px-4 py-3 cursor-pointer hover:bg-muted/40 transition-colors"
                onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments/${d.id}`)}
              >
                <DeploymentStatusBadge status={d.status} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-xs">{d.commit_sha?.slice(0, 7) ?? `#${d.id}`}</span>
                    <Badge variant="outline" className="text-[10px]">{d.deploy_type}</Badge>
                  </div>
                  {d.started_at && (
                    <p className="text-xs text-muted-foreground">{new Date(d.started_at).toLocaleString()}</p>
                  )}
                </div>
                <div className="flex items-center gap-1 text-xs text-muted-foreground shrink-0">
                  <Clock className="h-3 w-3" />
                  {formatDuration(d.started_at, d.finished_at)}
                </div>
                <ExternalLink className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
              </div>
            ))}
          </div>
        </TabsContent>

        {/* ── Environment ──────────────────────────────────────────────────── */}
        <TabsContent value="env" className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="font-medium">Environment variables</h2>
              <p className="text-xs text-muted-foreground mt-0.5">Applied to every deployment. Secrets are encrypted at rest.</p>
            </div>
            <Button size="sm" className="gap-1.5" onClick={() => setAddEnvOpen(true)}>
              <Plus className="h-3.5 w-3.5" /> Add variable
            </Button>
          </div>

          {envLoading ? (
            <div className="space-y-2">{[...Array(3)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
          ) : envVars?.length === 0 ? (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-12 text-center">
              <p className="text-sm font-medium">No variables yet</p>
              <p className="text-xs text-muted-foreground mt-1">Add environment variables to pass to your container.</p>
              <Button size="sm" className="mt-3 gap-1.5" onClick={() => setAddEnvOpen(true)}>
                <Plus className="h-3.5 w-3.5" /> Add variable
              </Button>
            </div>
          ) : (
            <div className="rounded-lg border divide-y">
              {envVars?.map((v) => (
                <div key={v.id} className="flex items-center gap-3 px-4 py-2.5">
                  <span className="font-mono text-sm font-medium min-w-[140px] shrink-0">{v.key}</span>
                  <span className="font-mono text-sm text-muted-foreground flex-1 truncate">
                    {v.is_secret
                      ? revealedIds.has(v.id) ? (v.value || '••••••••') : '••••••••'
                      : v.value}
                  </span>
                  <Badge variant={v.is_secret ? 'secondary' : 'outline'} className="text-[10px] shrink-0">
                    {v.is_secret ? 'Secret' : 'Plain'}
                  </Badge>
                  <div className="flex items-center gap-1 shrink-0">
                    {v.is_secret && (
                      <Button variant="ghost" size="icon" className="h-7 w-7"
                        onClick={() => setRevealedIds(prev => {
                          const n = new Set(prev); n.has(v.id) ? n.delete(v.id) : n.add(v.id); return n
                        })}>
                        {revealedIds.has(v.id) ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                      </Button>
                    )}
                    <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive hover:text-destructive"
                      onClick={() => confirm(`Remove "${v.key}"?`) && deleteEnvMutation.mutate(v.key)}>
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Add variable dialog */}
          <Dialog open={addEnvOpen} onOpenChange={setAddEnvOpen}>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>Add environment variable</DialogTitle>
              </DialogHeader>
              <div className="space-y-3 py-2">
                <div className="space-y-1.5">
                  <Label htmlFor="env-key">Key</Label>
                  <Input id="env-key" placeholder="DATABASE_URL" value={envKey}
                    onChange={(e) => setEnvKey(e.target.value)} className="font-mono" />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="env-val">Value</Label>
                  <Input id="env-val" placeholder="Value" value={envValue}
                    onChange={(e) => setEnvValue(e.target.value)} className="font-mono"
                    type={envIsSecret ? 'password' : 'text'} />
                </div>
                <div className="flex items-center gap-2">
                  <input type="checkbox" id="env-secret" checked={envIsSecret}
                    onChange={(e) => setEnvIsSecret(e.target.checked)}
                    className="h-4 w-4 rounded border-border accent-primary cursor-pointer" />
                  <Label htmlFor="env-secret" className="text-sm cursor-pointer">Mark as secret (encrypted at rest)</Label>
                </div>
              </div>
              <DialogFooter>
                <Button variant="ghost" size="sm" onClick={() => setAddEnvOpen(false)}>Cancel</Button>
                <Button size="sm" disabled={!envKey || !envValue || upsertEnvMutation.isPending}
                  onClick={() => upsertEnvMutation.mutate({ key: envKey, value: envValue, is_secret: envIsSecret })}>
                  {upsertEnvMutation.isPending ? 'Saving…' : 'Save'}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </TabsContent>

        {/* ── Live Logs ─────────────────────────────────────────────────────── */}
        <TabsContent value="logs">
          <ContainerLogsPanel
            projectId={projectId}
            serviceId={serviceId}
            enabled={activeTab === 'logs'}
          />
        </TabsContent>

        {/* ── Stats ────────────────────────────────────────────────────────── */}
        <TabsContent value="stats">
          <ContainerStatsPanel
            projectId={projectId}
            serviceId={serviceId}
            enabled={activeTab === 'stats'}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}

// ─── Container logs panel ─────────────────────────────────────────────────────

function ContainerLogsPanel({
  projectId,
  serviceId,
  enabled,
}: {
  projectId: string | undefined
  serviceId: string | undefined
  enabled: boolean
}) {
  const { lines, connected } = useContainerLogs(projectId, serviceId, enabled)
  const bottomRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom when new lines arrive
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lines])

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-medium flex items-center gap-2">
            <Terminal className="h-4 w-4 text-muted-foreground" /> Container logs
          </h2>
          <p className="text-xs text-muted-foreground mt-0.5">Live stdout + stderr from the running container.</p>
        </div>
        <div className="flex items-center gap-1.5 text-xs">
          <CircleDot className={`h-3 w-3 ${connected ? 'text-emerald-500 animate-pulse' : 'text-muted-foreground'}`} />
          <span className="text-muted-foreground">{connected ? 'Connected' : 'Disconnected'}</span>
        </div>
      </div>

      <div className="rounded-lg border bg-zinc-950 dark:bg-black font-mono text-xs text-zinc-300 h-[480px] overflow-y-auto p-4 space-y-0.5">
        {lines.length === 0 ? (
          <p className="text-zinc-500 italic">
            {enabled ? 'Waiting for container output…' : 'Open this tab to start streaming.'}
          </p>
        ) : (
          lines.map((line, i) => (
            <p key={i} className="whitespace-pre-wrap break-all leading-5">{line}</p>
          ))
        )}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}

// ─── Container stats panel ────────────────────────────────────────────────────

const fmtBytes = (b: number) => {
  if (b >= 1073741824) return `${(b / 1073741824).toFixed(2)} GB`
  if (b >= 1048576)    return `${(b / 1048576).toFixed(1)} MB`
  if (b >= 1024)       return `${(b / 1024).toFixed(0)} KB`
  return `${b} B`
}

function ContainerStatsPanel({
  projectId,
  serviceId,
  enabled,
}: {
  projectId: string | undefined
  serviceId: string | undefined
  enabled: boolean
}) {
  const { latest, history, status } = useContainerStatsSSE(projectId, serviceId, enabled)

  const cpuData  = history.map(p => ({ t: p.ts, v: Number(p.cpuPct.toFixed(2)) }))
  const memData  = history.map(p => ({ t: p.ts, v: Number(p.memPct.toFixed(2)) }))

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-medium flex items-center gap-2">
            <Activity className="h-4 w-4 text-muted-foreground" /> Container Resources
          </h2>
          <p className="text-xs text-muted-foreground mt-0.5">Live stats from the running container, updated every 2 s.</p>
        </div>
        <div className="flex items-center gap-1.5 text-xs">
          <CircleDot className={`h-3 w-3 ${
            status === 'running' ? 'text-emerald-500 animate-pulse' :
            status === 'not_found' ? 'text-amber-500' : 'text-muted-foreground'
          }`} />
          <span className="text-muted-foreground capitalize">{status}</span>
        </div>
      </div>

      {status === 'not_found' && (
        <div className="rounded-lg border border-dashed border-border py-10 text-center text-sm text-muted-foreground">
          Container is not running — stats unavailable.
        </div>
      )}

      {(status === 'connecting' && !latest) && (
        <div className="grid gap-4 sm:grid-cols-2">
          {[...Array(4)].map((_, i) => (
            <div key={i} className="h-32 rounded-xl bg-muted animate-pulse" />
          ))}
        </div>
      )}

      {latest && (
        <>
          {/* Summary cards */}
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <div className="rounded-xl border border-border/60 bg-card p-4">
              <div className="flex items-center gap-2 mb-2 text-muted-foreground text-xs font-medium uppercase tracking-wide">
                <Cpu className="h-3.5 w-3.5" /> CPU
              </div>
              <p className="text-3xl font-bold tabular-nums">{latest.cpuPct.toFixed(1)}<span className="text-lg font-normal text-muted-foreground">%</span></p>
            </div>
            <div className="rounded-xl border border-border/60 bg-card p-4">
              <div className="flex items-center gap-2 mb-2 text-muted-foreground text-xs font-medium uppercase tracking-wide">
                <MemoryStick className="h-3.5 w-3.5" /> Memory
              </div>
              <p className="text-3xl font-bold tabular-nums">{latest.memPct.toFixed(1)}<span className="text-lg font-normal text-muted-foreground">%</span></p>
              <p className="text-xs text-muted-foreground mt-1">{fmtBytes(latest.memUsed)} / {fmtBytes(latest.memTotal)}</p>
            </div>
            <div className="rounded-xl border border-border/60 bg-card p-4">
              <div className="flex items-center gap-2 mb-2 text-muted-foreground text-xs font-medium uppercase tracking-wide">
                <Network className="h-3.5 w-3.5" /> Network I/O
              </div>
              <p className="text-sm font-medium tabular-nums">↑ {fmtBytes(latest.netOut)}</p>
              <p className="text-sm font-medium tabular-nums mt-0.5">↓ {fmtBytes(latest.netIn)}</p>
            </div>
            <div className="rounded-xl border border-border/60 bg-card p-4">
              <div className="flex items-center gap-2 mb-2 text-muted-foreground text-xs font-medium uppercase tracking-wide">
                <HardDrive className="h-3.5 w-3.5" /> Block I/O
              </div>
              <p className="text-sm font-medium tabular-nums">↑ {fmtBytes(latest.blkOut)}</p>
              <p className="text-sm font-medium tabular-nums mt-0.5">↓ {fmtBytes(latest.blkIn)}</p>
              <p className="text-xs text-muted-foreground mt-1">{latest.pids} PIDs</p>
            </div>
          </div>

          {/* CPU chart */}
          <div className="rounded-xl border border-border/60 bg-card p-4">
            <p className="text-sm font-medium mb-3 flex items-center gap-2">
              <Cpu className="h-4 w-4 text-muted-foreground" /> CPU Usage
            </p>
            <ResponsiveContainer width="100%" height={160}>
              <AreaChart data={cpuData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                <defs>
                  <linearGradient id="cpuGrad" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="#6366f1" stopOpacity={0.35} />
                    <stop offset="100%" stopColor="#6366f1" stopOpacity={0.03} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                <XAxis dataKey="t" hide />
                <YAxis domain={[0, 100]} tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={36} />
                <Tooltip
                  contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  formatter={(v: unknown) => [`${Number(v).toFixed(2)}%`, 'CPU']}
                  labelFormatter={() => ''}
                />
                <Area type="monotone" dataKey="v" stroke="#6366f1" strokeWidth={2}
                  fill="url(#cpuGrad)" dot={false} isAnimationActive={false} />
              </AreaChart>
            </ResponsiveContainer>
          </div>

          {/* Memory chart */}
          <div className="rounded-xl border border-border/60 bg-card p-4">
            <p className="text-sm font-medium mb-3 flex items-center gap-2">
              <MemoryStick className="h-4 w-4 text-muted-foreground" /> Memory Usage
            </p>
            <ResponsiveContainer width="100%" height={160}>
              <AreaChart data={memData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                <defs>
                  <linearGradient id="memGrad" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="#10b981" stopOpacity={0.35} />
                    <stop offset="100%" stopColor="#10b981" stopOpacity={0.03} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                <XAxis dataKey="t" hide />
                <YAxis domain={[0, 100]} tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={36} />
                <Tooltip
                  contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  formatter={(v: unknown) => [`${Number(v).toFixed(2)}%`, 'Memory']}
                  labelFormatter={() => ''}
                />
                <Area type="monotone" dataKey="v" stroke="#10b981" strokeWidth={2}
                  fill="url(#memGrad)" dot={false} isAnimationActive={false} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </>
      )}
    </div>
  )
}
