import { useEffect, useRef, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Rocket, Clock, Search, Loader2, CheckCircle2, XCircle, Circle, Plus, Trash2, Eye, EyeOff, Terminal, Code2, CircleDot, Cpu, MemoryStick, Network, HardDrive, Activity, Copy, Download, Upload, X, Lock, Globe, Pencil, Check, GitBranch, GitCommit, GitFork, Settings2, Unlink, RotateCcw } from 'lucide-react'
import {
  AreaChart, Area, BarChart, Bar, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid, ReferenceLine,
} from 'recharts'
import { toast } from 'sonner'
import { servicesApi, statsApi, monthlyStatsApi, type DetectionResult, type UpdateServicePayload, type StatsRange, type StatsHistoryResponse, type MonthlyHistoryResponse } from '@/api/services'
import { GitHubRepoSelector, type RepoSelection } from '@/components/GitHubRepoSelector'
import { useContainerLogs } from '@/hooks/useContainerLogs'
import { useContainerStatsSSE } from '@/hooks/useContainerStatsSSE'
import { deploymentsApi } from '@/api/deployments'
import { nodesApi } from '@/api/nodes'
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
import { Textarea } from '@/components/ui/textarea'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDate, formatDuration, formatTimestamp, formatTimestampFull } from '@/lib/dateFormat'

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
            {/* Detected badge row */}
            <div className="flex items-center flex-wrap gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500 shrink-0" />
              <span className="text-sm font-medium">{frameworkLabel}</span>
              {result.version && (
                <Badge variant="secondary" className="font-mono text-xs">{result.version}</Badge>
              )}
              {result.type && (
                <Badge variant="secondary" className="text-xs capitalize">{result.type}</Badge>
              )}
              {result.orm && (
                <Badge variant="outline" className="text-xs font-mono">{result.orm}</Badge>
              )}
              {result.is_monorepo && (
                <Badge variant="outline" className="text-xs">monorepo</Badge>
              )}
              <Badge variant="outline" className="ml-auto font-mono text-[10px]">{result.base_image}</Badge>
            </div>

            <Separator />

            <div className="space-y-3">
              {result.pre_build_command && (
                <div className="space-y-1.5">
                  <Label htmlFor="det-prebuild" className="text-xs text-muted-foreground">Pre-build command</Label>
                  <Input
                    id="det-prebuild"
                    className="font-mono text-xs h-8"
                    value={result.pre_build_command}
                    onChange={(e) => onEdit('pre_build_command', e.target.value)}
                    placeholder="e.g. npx prisma generate"
                  />
                </div>
              )}
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
  const { timezone } = useTimezone()

  // Detection modal state
  const [detectOpen, setDetectOpen] = useState(false)
  const [detecting, setDetecting] = useState(false)
  const [detection, setDetection] = useState<DetectionResult | null>(null)

  // Tab state — track active tab so the container-logs SSE only connects when visible
  const [activeTab, setActiveTab] = useState('overview')

  // Artifact upload dialog (for services with deploy_type='artifact')
  const [artifactDialogOpen, setArtifactDialogOpen] = useState(false)
  const [artifactFile, setArtifactFile] = useState<File | null>(null)

  // Target node state
  const [targetNodeID, setTargetNodeID] = useState<string>('auto')

  // Overview source connector (shown when no source has been configured yet)
  const [overviewSourceType, setOverviewSourceType] = useState<'git' | 'artifact'>('git')
  const [overviewRepo, setOverviewRepo] = useState<RepoSelection | null>(null)

  // Settings state
  const [settingsRepo, setSettingsRepo] = useState<RepoSelection | null>(null)
  const [settingsBuildCmd, setSettingsBuildCmd] = useState('')
  const [settingsStartCmd, setSettingsStartCmd] = useState('')
  const [settingsPort, setSettingsPort] = useState('')
  const [initSettings, setInitSettings] = useState(false)

  // Env state
  const [addEnvOpen, setAddEnvOpen] = useState(false)
  const [envKey, setEnvKey] = useState('')
  const [envValue, setEnvValue] = useState('')
  const [envIsSecret, setEnvIsSecret] = useState(true)
  const [revealedIds, setRevealedIds] = useState<Set<number>>(new Set())
  const [revealedValues, setRevealedValues] = useState<Record<number, string>>({})
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editKey, setEditKey] = useState('')
  const [editValue, setEditValue] = useState('')
  const [editIsSecret, setEditIsSecret] = useState(true)
  const [envBulkOpen, setEnvBulkOpen] = useState(false)
  const [envBulkText, setEnvBulkText] = useState('')
  const [envPreviewRows, setEnvPreviewRows] = useState<{ id: string; key: string; value: string; isSecret: boolean }[]>([])

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

  // Live ticker — always-running so depNow is never stale during
  // pending → running transitions.
  const [depNow, setDepNow] = useState(Date.now())
  useEffect(() => {
    const id = setInterval(() => setDepNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  const { data: envVars, isLoading: envLoading } = useQuery({
    queryKey: ['env', serviceId],
    queryFn: () => envApi.list(projectId!, serviceId!),
    enabled: !!projectId && !!serviceId,
  })

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => nodesApi.list(),
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

  const bulkEnvMutation = useMutation({
    mutationFn: (vars: UpsertEnvPayload[]) => envApi.bulkUpsert(projectId!, serviceId!, vars),
    onSuccess: (d) => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      setEnvBulkOpen(false)
      setEnvBulkText('')
      setEnvPreviewRows([])
      toast.success(`${d.upserted} variable(s) imported.`)
    },
    onError: () => toast.error('Bulk import failed.'),
  })

  const deployMutation = useMutation({
    mutationFn: () =>
      deploymentsApi.trigger(projectId!, serviceId!, {
        deploy_type: service!.deploy_type,
        repo_url: service?.repo_url,
        repo_branch: service?.repo_branch,
        branch: service?.repo_branch,
        target_node_id: targetNodeID,
      }),
    onSuccess: (data) => {
      toast.success('Deployment queued.')
      qc.invalidateQueries({ queryKey: ['deployments', serviceId] })
      navigate(
        `/projects/${projectId}/services/${serviceId}/deployments/${data.deployment_id}`
      )
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to trigger deployment.'),
  })

  // Artifact: upload file then trigger deployment
  const artifactDeployMutation = useMutation({
    mutationFn: async (file: File) => {
      const { artifact_path } = await deploymentsApi.uploadArtifact(projectId!, serviceId!, file)
      return deploymentsApi.trigger(projectId!, serviceId!, {
        deploy_type: 'artifact',
        artifact_path,
        target_node_id: targetNodeID,
      })
    },
    onSuccess: (data) => {
      setArtifactFile(null)
      setArtifactDialogOpen(false)
      toast.success('Artifact uploaded — deployment queued.')
      qc.invalidateQueries({ queryKey: ['deployments', serviceId] })
      navigate(`/projects/${projectId}/services/${serviceId}/deployments/${data.deployment_id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Artifact upload failed.'),
  })

  const restartMutation = useMutation({
    mutationFn: () => servicesApi.restart(projectId!, serviceId!),
    onSuccess: () => {
      toast.success('Container restarted.')
      qc.invalidateQueries({ queryKey: ['service', projectId, serviceId] })
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Restart failed.'),
  })

  // needsDetection: true when deploy_type is git but framework/commands not set
  const needsDetection =
    service?.deploy_type === 'git' &&
    (!service?.framework || !service?.build_command || !service?.start_command)

  const handleDeployClick = async () => {
    if (!service) return
    // Artifact-type services require a file to be uploaded first
    if (service.deploy_type === 'artifact') {
      setArtifactDialogOpen(true)
      return
    }
    if (needsDetection) {
      // Open modal and start detection
      setDetectOpen(true)
      setDetecting(true)
      setDetection(null)
      try {
        const result = await servicesApi.detect(projectId!, serviceId!)
        setDetection(result)
      } catch (e: unknown) {
        const msg = (e as any)?.response?.data?.error ?? (e instanceof Error ? e.message : 'Stack detection failed.')
        toast.error(msg)
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
      const msg = (e as any)?.response?.data?.error ?? (e instanceof Error ? e.message : 'Stack detection failed.')
      toast.error(msg)
      setDetectOpen(false)
    } finally {
      setDetecting(false)
    }
  }

  // ── Env helpers ──────────────────────────────────────────────────────────

  const toggleEnvReveal = async (id: number, key: string, isSecret: boolean) => {
    const isRevealed = revealedIds.has(id)
    setRevealedIds(prev => { const n = new Set(prev); n.has(id) ? n.delete(id) : n.add(id); return n })
    if (isRevealed || !isSecret) return
    if (revealedValues[id] !== undefined) return
    try {
      const value = await envApi.reveal(projectId!, serviceId!, key)
      setRevealedValues(prev => ({ ...prev, [id]: value }))
    } catch {
      toast.error('Failed to reveal secret.')
      setRevealedIds(prev => { const n = new Set(prev); n.delete(id); return n })
    }
  }

  const buildEnvText = (extra: Record<number, string> = {}) => {
    const r = { ...revealedValues, ...extra }
    return (envVars ?? []).map(v => `${v.key}=${v.is_secret ? (r[v.id] ?? '') : v.value}`).join('\n')
  }

  // Reveal all unrevealed secrets in parallel, return merged map
  const revealAll = async (): Promise<Record<number, string>> => {
    const secrets = (envVars ?? []).filter(v => v.is_secret)
    if (secrets.length === 0) return {}
    // Reveal any not yet cached
    const unrevealed = secrets.filter(v => revealedValues[v.id] === undefined)
    if (unrevealed.length > 0) {
      const results = await Promise.allSettled(
        unrevealed.map(v => envApi.reveal(projectId!, serviceId!, v.key).then(val => [v.id, val] as [number, string]))
      )
      const extra: Record<number, string> = {}
      for (const r of results) {
        if (r.status === 'fulfilled') {
          extra[r.value[0]] = r.value[1]
        }
      }
      setRevealedValues(prev => ({ ...prev, ...extra }))
      return { ...revealedValues, ...extra }
    }
    return { ...revealedValues }
  }

  const copyEnv = async () => {
    const revealed = await revealAll()
    try { await navigator.clipboard.writeText(buildEnvText(revealed)); toast.success('Copied to clipboard.') }
    catch { toast.error('Clipboard copy failed.') }
  }

  const downloadEnv = async () => {
    const revealed = await revealAll()
    const blob = new Blob([buildEnvText(revealed)], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url; a.download = '.env'; a.click()
    URL.revokeObjectURL(url)
  }

  const envParseLine = (l: string) => {
    const eq = l.indexOf('=')
    return { id: `${Math.random()}`, key: l.slice(0, eq).trim(), value: l.slice(eq + 1).trim().replace(/^["']|["']$/g, ''), isSecret: true }
  }

  const handleEnvBulkText = (text: string) => {
    setEnvBulkText(text)
    setEnvPreviewRows(text.split('\n').map(l => l.trim()).filter(l => l && !l.startsWith('#') && l.includes('=')).map(envParseLine))
  }

  const updateEnvPreviewRow = (id: string, field: 'key' | 'value' | 'isSecret', val: string | boolean) =>
    setEnvPreviewRows(rows => rows.map(r => r.id === id ? { ...r, [field]: val } : r))

  const removeEnvPreviewRow = (id: string) =>
    setEnvPreviewRows(rows => rows.filter(r => r.id !== id))

  const importEnvPreview = () => {
    const vars: UpsertEnvPayload[] = envPreviewRows.filter(r => r.key.trim()).map(r => ({ key: r.key.trim(), value: r.value, is_secret: r.isSecret }))
    bulkEnvMutation.mutate(vars)
  }

  const startEditEnv = (v: { id: number; key: string; value: string; is_secret: boolean }) => {
    setEditingId(v.id); setEditKey(v.key)
    setEditValue(v.is_secret ? (revealedValues[v.id] ?? '') : v.value)
    setEditIsSecret(v.is_secret)
  }

  const saveEditEnv = () => {
    if (!editKey.trim() || !editValue) { toast.error('Key and value are required.'); return }
    upsertEnvMutation.mutate({ key: editKey, value: editValue, is_secret: editIsSecret }, { onSuccess: () => setEditingId(null) })
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

  const isDeploying = deployMutation.isPending || artifactDeployMutation.isPending || service.status === 'deploying'

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

      <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3 min-w-0 flex-wrap">
          <h1 className="text-2xl font-semibold truncate">{service.name}</h1>
          <ServiceStatusBadge status={service.status} />
          {service.framework && (
            <Badge variant="secondary">{service.framework}</Badge>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0 flex-wrap">
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
          {service.status === 'running' && (
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-xs"
              onClick={() => restartMutation.mutate()}
              disabled={restartMutation.isPending || isDeploying}
            >
              {restartMutation.isPending
                ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Restarting…</>
                : <><RotateCcw className="h-3.5 w-3.5" /> Restart</>}
            </Button>
          )}

          {/* Node selection */}
          <div className="flex items-center gap-2 border rounded-md px-2 py-1 bg-muted/50">
            <Cpu className="h-3.5 w-3.5 text-muted-foreground" />
            <select
              className="bg-transparent border-none text-xs focus:ring-0 cursor-pointer pr-4"
              value={targetNodeID}
              onChange={(e) => setTargetNodeID(e.target.value)}
            >
              <option value="auto">Auto (Least loaded)</option>
              <option value="main">Main (Internal)</option>
              {nodes?.filter(n => n.status === 'connected').map(n => (
                <option key={n.id} value={n.node_id || n.hostname}>
                  {n.name} ({n.hostname})
                </option>
              ))}
            </select>
          </div>

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

      {/* Artifact upload dialog — for services with deploy_type='artifact' */}
      <Dialog open={artifactDialogOpen} onOpenChange={(o) => { setArtifactDialogOpen(o); if (!o) setArtifactFile(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Upload artifact &amp; deploy</DialogTitle>
            <DialogDescription>
              Upload a pre-built archive (<code>.zip</code>, <code>.tar.gz</code>, or <code>.tgz</code>).
              The archive will be extracted and a container image built from its contents.
            </DialogDescription>
          </DialogHeader>
          <div className="py-2 space-y-3">
            <label className="flex flex-col items-center gap-2 rounded-lg border-2 border-dashed p-6 cursor-pointer hover:border-primary/50 transition-colors">
              <Upload className="h-6 w-6 text-muted-foreground" />
              {artifactFile
                ? <span className="text-sm font-medium">{artifactFile.name}</span>
                : <span className="text-sm text-muted-foreground">Click to select archive file</span>}
              <input
                type="file"
                accept=".zip,.tar.gz,.tgz"
                className="hidden"
                onChange={(e) => setArtifactFile(e.target.files?.[0] ?? null)}
              />
            </label>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => { setArtifactDialogOpen(false); setArtifactFile(null) }}>Cancel</Button>
            <Button
              size="sm"
              className="gap-1.5"
              disabled={!artifactFile || artifactDeployMutation.isPending}
              onClick={() => artifactFile && artifactDeployMutation.mutate(artifactFile)}
            >
              {artifactDeployMutation.isPending
                ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Uploading…</>
                : <><Upload className="h-3.5 w-3.5" /> Upload &amp; Deploy</>}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList className="mb-6 w-full max-w-full overflow-x-auto justify-start">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="deployments">Deployments</TabsTrigger>
          <TabsTrigger value="settings" onClick={() => {
            if (!initSettings && service) {
              setSettingsRepo(service.repo_url ? { repo_url: service.repo_url, repo_branch: service.repo_branch ?? 'main', repo_folder: service.repo_folder ?? '' } : null)
              setSettingsBuildCmd(service.build_command ?? '')
              setSettingsStartCmd(service.start_command ?? '')
              setSettingsPort(service.app_port ? String(service.app_port) : '')
              setInitSettings(true)
            }
          }}>Settings</TabsTrigger>
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

          {/* Source connector — shown when no source is configured yet */}
          {!service.repo_url && service.deploy_type !== 'artifact' ? (
            <div className="rounded-lg border-2 border-dashed p-6 space-y-5">
              <div>
                <h2 className="font-medium">Connect a deployment source</h2>
                <p className="text-sm text-muted-foreground mt-1">
                  Choose how you want to deploy this service. You can change this later in Settings.
                </p>
              </div>
              {/* Source type selector */}
              <div className="grid grid-cols-1 xs:grid-cols-2 gap-3">
                <button
                  type="button"
                  onClick={() => setOverviewSourceType('git')}
                  className={`flex flex-col items-center gap-2 rounded-lg border-2 p-4 text-center transition-colors ${overviewSourceType === 'git' ? 'border-primary bg-primary/5' : 'border-border hover:border-muted-foreground/40'}`}
                >
                  <GitFork className="h-5 w-5" />
                  <span className="text-sm font-medium">Git repository</span>
                  <span className="text-xs text-muted-foreground">Connect a GitHub repo and auto-deploy on push</span>
                </button>
                <button
                  type="button"
                  onClick={() => setOverviewSourceType('artifact')}
                  className={`flex flex-col items-center gap-2 rounded-lg border-2 p-4 text-center transition-colors ${overviewSourceType === 'artifact' ? 'border-primary bg-primary/5' : 'border-border hover:border-muted-foreground/40'}`}
                >
                  <Upload className="h-5 w-5" />
                  <span className="text-sm font-medium">Upload artifact</span>
                  <span className="text-xs text-muted-foreground">Deploy a pre-built <code>.zip</code> or <code>.tar.gz</code></span>
                </button>
              </div>

              {/* Git path */}
              {overviewSourceType === 'git' && (
                <div className="space-y-3">
                  <GitHubRepoSelector
                    value={overviewRepo ?? { repo_url: '', repo_branch: 'main', repo_folder: '' }}
                    onChange={setOverviewRepo}
                  />
                  {overviewRepo?.repo_url && (
                    <div className="flex justify-end">
                      <Button
                        size="sm"
                        className="gap-1.5"
                        disabled={updateMutation.isPending}
                        onClick={() =>
                          updateMutation.mutate(
                            {
                              deploy_type: 'git',
                              repo_url: overviewRepo.repo_url,
                              repo_branch: overviewRepo.repo_branch,
                              repo_folder: overviewRepo.repo_folder,
                            },
                            { onSuccess: () => { toast.success('Repository connected.'); setOverviewRepo(null) } }
                          )
                        }
                      >
                        {updateMutation.isPending
                          ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Connecting…</>
                          : 'Connect repository'}
                      </Button>
                    </div>
                  )}
                </div>
              )}

              {/* Artifact path */}
              {overviewSourceType === 'artifact' && (
                <div className="space-y-3">
                  <label className="flex flex-col items-center gap-2 rounded-lg border-2 border-dashed p-6 cursor-pointer hover:border-primary/50 transition-colors">
                    <Upload className="h-6 w-6 text-muted-foreground" />
                    {artifactFile
                      ? <span className="text-sm font-medium">{artifactFile.name}</span>
                      : <span className="text-sm text-muted-foreground">Click to select <code>.zip</code>, <code>.tar.gz</code>, or <code>.tgz</code></span>}
                    <input
                      type="file"
                      accept=".zip,.tar.gz,.tgz"
                      className="hidden"
                      onChange={(e) => setArtifactFile(e.target.files?.[0] ?? null)}
                    />
                  </label>
                  {artifactFile && (
                    <div className="flex justify-end gap-2">
                      <Button variant="outline" size="sm" onClick={() => setArtifactFile(null)}>Clear</Button>
                      <Button
                        size="sm"
                        className="gap-1.5"
                        disabled={artifactDeployMutation.isPending || updateMutation.isPending}
                        onClick={() =>
                          updateMutation.mutate(
                            { deploy_type: 'artifact' },
                            { onSuccess: () => artifactDeployMutation.mutate(artifactFile!) }
                          )
                        }
                      >
                        {artifactDeployMutation.isPending
                          ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Uploading…</>
                          : <><Upload className="h-3.5 w-3.5" /> Upload &amp; Deploy</>}
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </div>
          ) : (
            /* Build configuration (when source is connected) */
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
                    {service.repo_folder && <p className="text-xs text-muted-foreground">Folder: <span className="font-mono">/{service.repo_folder}</span></p>}
                  </div>
                )}
                {service.deploy_type === 'artifact' && (
                  <div className="rounded-lg border bg-muted/30 p-3 space-y-0.5">
                    <p className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Deploy type</p>
                    <div className="flex items-center gap-2">
                      <Badge variant="secondary">artifact</Badge>
                      <Button variant="ghost" size="sm" className="h-6 text-xs gap-1" onClick={() => setArtifactDialogOpen(true)}>
                        <Upload className="h-3 w-3" /> Upload new
                      </Button>
                    </div>
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
          )}

          <Separator />

          {/* Recent deployments mini-list */}
          <div>
            <div className="mb-3 flex items-center justify-between">
              <h2 className="font-medium flex items-center gap-2"><Terminal className="h-4 w-4 text-muted-foreground" /> Recent deployments</h2>
              <Button variant="ghost" size="sm" className="text-xs text-muted-foreground"
                onClick={() => setActiveTab('deployments')}>
                View all →
              </Button>
            </div>
            {(deploymentsData?.deployments.length ?? 0) === 0 && (
              <p className="text-sm text-muted-foreground">No deployments yet. Click <strong>Deploy now</strong> to start.</p>
            )}
            <div className="rounded-xl border divide-y overflow-hidden">
              {[...(deploymentsData?.deployments ?? [])]
                .sort((a, b) => {
                  const tDiff = new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
                  return tDiff !== 0 ? tDiff : b.id - a.id
                })
                .slice(0, 5)
                .map((d, idx) => (
                  <div
                    key={d.id}
                    className="group relative flex items-center gap-3 px-4 py-3 cursor-pointer hover:bg-muted/40 transition-colors"
                    onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments/${d.id}`)}
                  >
                    <div className={cn(
                      'absolute inset-y-0 left-0 w-0.5',
                      d.status === 'success' ? 'bg-emerald-500' :
                      d.status === 'failed'  ? 'bg-red-500' :
                      d.status === 'running' ? 'bg-blue-500 animate-pulse' :
                      'bg-border',
                    )} />
                    {d.status === 'success' ? <CheckCircle2 className="h-4 w-4 text-emerald-500 shrink-0 ml-1" /> :
                     d.status === 'failed'  ? <XCircle className="h-4 w-4 text-red-500 shrink-0 ml-1" /> :
                     d.status === 'running' ? <Loader2 className="h-4 w-4 text-blue-500 animate-spin shrink-0 ml-1" /> :
                     <Circle className="h-4 w-4 text-muted-foreground/50 shrink-0 ml-1" />}
                    <DeploymentStatusBadge status={d.status} />
                    {idx === 0 && <Badge variant="outline" className="text-[10px] px-1.5 py-0 font-normal text-muted-foreground">latest</Badge>}
                    <span className="font-mono text-xs text-muted-foreground truncate">
                      {d.commit_sha?.slice(0, 7) ?? d.deploy_type}
                    </span>
                    {d.branch && (
                      <span className="hidden sm:flex items-center gap-0.5 text-[10px] text-muted-foreground font-mono shrink-0">
                        <GitBranch className="h-2.5 w-2.5" />{d.branch}
                      </span>
                    )}
                    <div className="ml-auto flex items-center gap-3 text-xs text-muted-foreground shrink-0">
                      <span className="hidden sm:inline">{formatDate(d.started_at ?? d.created_at, timezone)}</span>
                      <span className="flex items-center gap-1 font-mono tabular-nums">
                        <Clock className="h-3 w-3" />
                        {formatDuration(
                          d.started_at ?? d.created_at,
                          d.finished_at,
                          depNow,
                        )}
                        {(d.status === 'running' || d.status === 'pending') && (
                          <span className="inline-block w-1 bg-blue-500 animate-pulse rounded-sm align-middle" style={{ height: '8px' }} />
                        )}
                      </span>
                    </div>
                  </div>
                ))}
            </div>
          </div>
        </TabsContent>

        {/* ── Deployments ──────────────────────────────────────────────────── */}
        <TabsContent value="deployments" className="space-y-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="font-medium">All deployments</h2>
              <p className="text-xs text-muted-foreground mt-0.5">Newest first · times in {timezone}</p>
            </div>
            <Button size="sm" className="gap-1.5" onClick={handleDeployClick} disabled={isDeploying}>
              {isDeploying ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Deploying…</> : <><Rocket className="h-3.5 w-3.5" /> Deploy now</>}
            </Button>
          </div>
          {(deploymentsData?.deployments.length ?? 0) === 0 ? (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-14 text-center">
              <Rocket className="mb-2 h-8 w-8 text-muted-foreground" />
              <p className="text-sm font-medium">No deployments yet</p>
              <p className="text-xs text-muted-foreground mt-1">Trigger your first deployment above.</p>
            </div>
          ) : (
            <div className="rounded-xl border divide-y overflow-hidden">
              {[...(deploymentsData?.deployments ?? [])]
                .sort((a, b) => {
                  const tDiff = new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
                  return tDiff !== 0 ? tDiff : b.id - a.id
                })
                .map((d, idx) => {
                  const isLatest = idx === 0
                  return (
                    <div
                      key={d.id}
                      className={cn(
                        'group relative flex items-center gap-3 px-4 py-3.5 cursor-pointer transition-colors hover:bg-muted/40',
                        isLatest && 'bg-muted/20',
                      )}
                      onClick={() => navigate(`/projects/${projectId}/services/${serviceId}/deployments/${d.id}`)}
                    >
                      {/* Status stripe */}
                      <div className={cn(
                        'absolute inset-y-0 left-0 w-0.5',
                        d.status === 'success' ? 'bg-emerald-500' :
                        d.status === 'failed'  ? 'bg-red-500' :
                        d.status === 'running' ? 'bg-blue-500 animate-pulse' :
                        'bg-border',
                      )} />

                      {/* Status icon */}
                      <div className="shrink-0 ml-1">
                        {d.status === 'success' ? <CheckCircle2 className="h-4 w-4 text-emerald-500" /> :
                         d.status === 'failed'  ? <XCircle className="h-4 w-4 text-red-500" /> :
                         d.status === 'running' ? <Loader2 className="h-4 w-4 text-blue-500 animate-spin" /> :
                         <Circle className="h-4 w-4 text-muted-foreground/50" />}
                      </div>

                      {/* Badges */}
                      <div className="flex items-center gap-2 min-w-0">
                        <DeploymentStatusBadge status={d.status} />
                        {isLatest && (
                          <Badge variant="outline" className="text-[10px] px-1.5 py-0 font-normal text-muted-foreground shrink-0">
                            latest
                          </Badge>
                        )}
                      </div>

                      {/* Meta */}
                      <div className="ml-auto flex items-center gap-3 text-xs text-muted-foreground shrink-0">
                        {d.commit_sha ? (
                          <span className="hidden sm:flex items-center gap-1 font-mono">
                            <GitCommit className="h-3 w-3" />{d.commit_sha.slice(0, 7)}
                          </span>
                        ) : (
                          <span className="hidden sm:flex items-center gap-1 font-mono">
                            <GitBranch className="h-3 w-3" />{d.deploy_type}
                          </span>
                        )}
                        {d.branch && (
                          <span className="hidden md:flex items-center gap-0.5 font-mono">
                            <GitBranch className="h-2.5 w-2.5" />{d.branch}
                          </span>
                        )}
                        <span className="hidden sm:inline whitespace-nowrap">
                          {formatDate(d.started_at ?? d.created_at, timezone)}
                        </span>
                        <span className="flex items-center gap-1 font-mono tabular-nums">
                          <Clock className="h-3 w-3" />
                          {formatDuration(
                            d.started_at ?? d.created_at,
                            d.finished_at,
                            depNow,
                          )}
                          {(d.status === 'running' || d.status === 'pending') && (
                            <span className="inline-block w-1 bg-blue-500 animate-pulse rounded-sm align-middle" style={{ height: '8px' }} />
                          )}
                        </span>
                      </div>

                      {/* Arrow */}
                      <span className="text-xs text-muted-foreground shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">→</span>
                    </div>
                  )
                })}
            </div>
          )}
        </TabsContent>

        {/* ── Settings ─────────────────────────────────────────────────────── */}
        <TabsContent value="settings" className="space-y-6">

          {/* Repository section */}
          <Card>
            <CardHeader className="pb-4">
              <CardTitle className="flex items-center gap-2 text-base">
                <GitFork className="h-4 w-4 text-muted-foreground" />
                Repository
              </CardTitle>
            </CardHeader>
            <CardContent>

            {service.repo_url ? (
              <div className="rounded-lg border p-4 space-y-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="space-y-1 min-w-0">
                    <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Connected repository</p>
                    <p className="font-mono text-sm break-all">{service.repo_url}</p>
                    {service.repo_branch && (
                      <p className="flex items-center gap-1 text-xs text-muted-foreground">
                        <GitBranch className="h-3 w-3" /> {service.repo_branch}
                        {service.repo_folder ? ` · /${service.repo_folder}` : ''}
                      </p>
                    )}
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    className="shrink-0 gap-1.5 text-destructive border-destructive/30 hover:bg-destructive/10 hover:text-destructive"
                    disabled={updateMutation.isPending}
                    onClick={() => {
                      updateMutation.mutate({ clear_repo: true }, {
                        onSuccess: () => {
                          setSettingsRepo(null)
                          setInitSettings(false)
                          toast.success('Repository disconnected.')
                        },
                      })
                    }}
                  >
                    <Unlink className="h-3.5 w-3.5" /> Disconnect
                  </Button>
                </div>

                {/* Auto-deploy toggle */}
                <Separator />
                <div className="flex items-center justify-between gap-4">
                  <div>
                    <p className="text-sm font-medium">Auto-deploy on push</p>
                    <p className="text-xs text-muted-foreground">Automatically deploy when code is pushed to the connected branch via your GitHub App.</p>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={service.auto_deploy}
                    disabled={updateMutation.isPending}
                    onClick={() => updateMutation.mutate(
                      { auto_deploy: !service.auto_deploy },
                      { onSuccess: () => toast.success(service.auto_deploy ? 'Auto-deploy disabled.' : 'Auto-deploy enabled.') }
                    )}
                    className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${service.auto_deploy ? 'bg-primary' : 'bg-input'}`}
                  >
                    <span className={`pointer-events-none block h-5 w-5 rounded-full bg-background shadow-lg ring-0 transition-transform ${service.auto_deploy ? 'translate-x-5' : 'translate-x-0'}`} />
                  </button>
                </div>
              </div>
            ) : (
              <div className="rounded-lg border p-4 space-y-4">
                <p className="text-sm text-muted-foreground">No repository connected. Select one below to enable Git deployments.</p>
                <GitHubRepoSelector
                  value={settingsRepo ?? { repo_url: '', repo_branch: 'main', repo_folder: '' }}
                  onChange={setSettingsRepo}
                />
                {settingsRepo?.repo_url && (
                  <div className="flex justify-end">
                    <Button
                      size="sm"
                      className="gap-1.5"
                      disabled={updateMutation.isPending}
                      onClick={() => {
                        updateMutation.mutate(
                          {
                            deploy_type: 'git',
                            repo_url: settingsRepo.repo_url,
                            repo_branch: settingsRepo.repo_branch,
                            repo_folder: settingsRepo.repo_folder,
                          },
                          { onSuccess: () => toast.success('Repository connected.') }
                        )
                      }}
                    >
                      {updateMutation.isPending ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</> : 'Connect repository'}
                    </Button>
                  </div>
                )}
              </div>
            )}
            </CardContent>
          </Card>

          {/* Build configuration section */}
          <Card>
            <CardHeader className="pb-4">
              <CardTitle className="flex items-center gap-2 text-base">
                <Settings2 className="h-4 w-4 text-muted-foreground" />
                Build configuration
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="cfg-build" className="text-xs">Build command</Label>
                <Input
                  id="cfg-build"
                  className="font-mono text-xs"
                  placeholder="npm ci && npm run build"
                  value={settingsBuildCmd}
                  onChange={e => setSettingsBuildCmd(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="cfg-start" className="text-xs">Start command</Label>
                <Input
                  id="cfg-start"
                  className="font-mono text-xs"
                  placeholder="node dist/index.js"
                  value={settingsStartCmd}
                  onChange={e => setSettingsStartCmd(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="cfg-port" className="text-xs">App port</Label>
                <Input
                  id="cfg-port"
                  type="number"
                  className="font-mono text-xs w-32"
                  placeholder="8080"
                  value={settingsPort}
                  onChange={e => setSettingsPort(e.target.value)}
                />
              </div>
            </div>
            <div className="flex justify-end">
              <Button
                size="sm"
                className="gap-1.5"
                disabled={updateMutation.isPending}
                onClick={() => {
                  const payload: UpdateServicePayload = {}
                  if (settingsBuildCmd) payload.build_command = settingsBuildCmd
                  if (settingsStartCmd) payload.start_command = settingsStartCmd
                  if (settingsPort) payload.app_port = parseInt(settingsPort, 10)
                  updateMutation.mutate(payload, { onSuccess: () => toast.success('Build configuration saved.') })
                }}
              >
                {updateMutation.isPending ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</> : 'Save configuration'}
              </Button>
            </div>
            </CardContent>
          </Card>
        </TabsContent>

        {/* ── Environment ──────────────────────────────────────────────────── */}
        <TabsContent value="env" className="space-y-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <h2 className="font-medium">Environment variables</h2>
              <p className="text-xs text-muted-foreground mt-0.5">Applied to every deployment. Secrets are encrypted at rest.</p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" className="gap-1.5" onClick={copyEnv} disabled={!envVars?.length}>
                <Copy className="h-3.5 w-3.5" /> Copy .env
              </Button>
              <Button variant="outline" size="sm" className="gap-1.5" onClick={downloadEnv} disabled={!envVars?.length}>
                <Download className="h-3.5 w-3.5" /> Download .env
              </Button>
              <Button variant="outline" size="sm" className="gap-1.5" onClick={() => setEnvBulkOpen(true)}>
                <Upload className="h-3.5 w-3.5" /> Import .env
              </Button>
              <Button size="sm" className="gap-1.5" onClick={() => setAddEnvOpen(true)}>
                <Plus className="h-3.5 w-3.5" /> Add variable
              </Button>
            </div>
          </div>

          {envLoading ? (
            <div className="space-y-2">{[...Array(3)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
          ) : envVars?.length === 0 ? (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-12 text-center">
              <p className="text-sm font-medium">No variables yet</p>
              <p className="text-xs text-muted-foreground mt-1">Add environment variables to pass to your container.</p>
              <div className="flex gap-2 mt-3">
                <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setEnvBulkOpen(true)}>
                  <Upload className="h-3.5 w-3.5" /> Import .env
                </Button>
                <Button size="sm" className="gap-1.5" onClick={() => setAddEnvOpen(true)}>
                  <Plus className="h-3.5 w-3.5" /> Add variable
                </Button>
              </div>
            </div>
          ) : (
            <div className="rounded-lg border overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Key</TableHead>
                    <TableHead>Value</TableHead>
                    <TableHead className="w-24">Type</TableHead>
                    <TableHead className="w-28" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {envVars?.map((v) =>
                    editingId === v.id ? (
                      <TableRow key={v.id}>
                        <TableCell className="py-1.5">
                          <Input className="h-7 font-mono text-xs" value={editKey}
                            onChange={e => setEditKey(e.target.value)} />
                        </TableCell>
                        <TableCell className="py-1.5">
                          <Input className="h-7 font-mono text-xs"
                            type={editIsSecret ? 'password' : 'text'}
                            placeholder={editIsSecret && !editValue ? 'Enter new value' : ''}
                            value={editValue}
                            onChange={e => setEditValue(e.target.value)} />
                        </TableCell>
                        <TableCell className="py-1.5">
                          <button type="button"
                            className={`flex items-center gap-1 text-xs rounded-full px-2.5 py-0.5 font-medium transition-colors ${editIsSecret ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400' : 'bg-muted text-muted-foreground'}`}
                            onClick={() => setEditIsSecret(s => !s)}>
                            {editIsSecret ? <><Lock className="h-2.5 w-2.5" /> Secret</> : <><Globe className="h-2.5 w-2.5" /> Plain</>}
                          </button>
                        </TableCell>
                        <TableCell className="py-1.5">
                          <div className="flex justify-end gap-1">
                            <Button variant="ghost" size="icon" className="h-7 w-7 text-emerald-600 hover:text-emerald-600"
                              onClick={saveEditEnv} disabled={upsertEnvMutation.isPending}>
                              <Check className="h-3.5 w-3.5" />
                            </Button>
                            <Button variant="ghost" size="icon" className="h-7 w-7"
                              onClick={() => setEditingId(null)}>
                              <X className="h-3.5 w-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ) : (
                      <TableRow key={v.id}>
                        <TableCell className="font-mono text-sm py-2.5">{v.key}</TableCell>
                        <TableCell className="font-mono text-sm py-2.5 max-w-xs truncate text-muted-foreground">
                          {v.is_secret
                            ? revealedIds.has(v.id)
                              ? (revealedValues[v.id] ?? <span className="italic text-xs">loading…</span>)
                              : '••••••••'
                            : v.value}
                        </TableCell>
                        <TableCell className="py-2.5">
                          <span className={`inline-flex items-center gap-1 text-[10px] rounded-full px-2 py-0.5 font-medium ${v.is_secret ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400' : 'bg-muted text-muted-foreground'}`}>
                            {v.is_secret ? <><Lock className="h-2.5 w-2.5" /> Secret</> : <><Globe className="h-2.5 w-2.5" /> Plain</>}
                          </span>
                        </TableCell>
                        <TableCell className="py-2.5">
                          <div className="flex justify-end gap-1">
                            <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground"
                              onClick={() => startEditEnv(v)}>
                              <Pencil className="h-3 w-3" />
                            </Button>
                            {v.is_secret && (
                              <Button variant="ghost" size="icon" className="h-7 w-7"
                                onClick={() => toggleEnvReveal(v.id, v.key, v.is_secret)}>
                                {revealedIds.has(v.id) ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                              </Button>
                            )}
                            <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive hover:text-destructive"
                              onClick={() => confirm(`Remove "${v.key}"?`) && deleteEnvMutation.mutate(v.key)}>
                              <Trash2 className="h-3.5 w-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )
                  )}
                </TableBody>
              </Table>
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

          {/* Bulk import dialog */}
          <Dialog open={envBulkOpen} onOpenChange={(o) => { setEnvBulkOpen(o); if (!o) { setEnvBulkText(''); setEnvPreviewRows([]) } }}>
            <DialogContent className="max-w-2xl">
              <DialogHeader>
                <DialogTitle>Import .env file</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 pt-2">
                <Textarea rows={5} className="font-mono text-xs"
                  placeholder={'DATABASE_URL=postgres://user:pass@host/db\nAPI_KEY=supersecret\nDEBUG=false'}
                  value={envBulkText}
                  onChange={(e) => handleEnvBulkText(e.target.value)}
                  autoFocus />
                <label className="flex items-center gap-1.5 cursor-pointer text-xs text-muted-foreground hover:text-foreground transition-colors w-fit">
                  <Upload className="h-3.5 w-3.5" />
                  <span>Or upload a .env file</span>
                  <input type="file" accept=".env,text/plain" className="hidden"
                    onChange={(e) => {
                      const f = e.target.files?.[0]
                      if (!f) return
                      const reader = new FileReader()
                      reader.onload = (ev) => handleEnvBulkText(ev.target?.result as string ?? '')
                      reader.readAsText(f)
                      e.target.value = ''
                    }} />
                </label>
                {envPreviewRows.length > 0 && (
                  <div className="rounded-lg border overflow-hidden">
                    <div className="bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground">
                      {envPreviewRows.length} variable{envPreviewRows.length !== 1 ? 's' : ''} — review &amp; edit before importing
                    </div>
                    <div className="max-h-56 overflow-auto">
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>Key</TableHead>
                            <TableHead>Value</TableHead>
                            <TableHead className="w-24">Type</TableHead>
                            <TableHead className="w-8" />
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {envPreviewRows.map(row => (
                            <TableRow key={row.id}>
                              <TableCell className="py-1">
                                <Input className="h-7 font-mono text-xs" value={row.key}
                                  onChange={e => updateEnvPreviewRow(row.id, 'key', e.target.value)} />
                              </TableCell>
                              <TableCell className="py-1">
                                <Input className="h-7 font-mono text-xs"
                                  type={row.isSecret ? 'password' : 'text'}
                                  value={row.value}
                                  onChange={e => updateEnvPreviewRow(row.id, 'value', e.target.value)} />
                              </TableCell>
                              <TableCell className="py-1">
                                <button type="button"
                                  className={`flex items-center gap-1 text-xs rounded-full px-2.5 py-0.5 font-medium transition-colors ${row.isSecret ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400' : 'bg-muted text-muted-foreground'}`}
                                  onClick={() => updateEnvPreviewRow(row.id, 'isSecret', !row.isSecret)}>
                                  {row.isSecret ? <><Lock className="h-2.5 w-2.5" /> Secret</> : <><Globe className="h-2.5 w-2.5" /> Plain</>}
                                </button>
                              </TableCell>
                              <TableCell className="py-1">
                                <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground"
                                  onClick={() => removeEnvPreviewRow(row.id)}>
                                  <X className="h-3 w-3" />
                                </Button>
                              </TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </div>
                  </div>
                )}
                <div className="flex justify-end gap-2">
                  <Button variant="outline" onClick={() => setEnvBulkOpen(false)} disabled={bulkEnvMutation.isPending}>Cancel</Button>
                  <Button onClick={importEnvPreview}
                    disabled={!envPreviewRows.filter(r => r.key.trim()).length || bulkEnvMutation.isPending}
                    className="gap-1.5">
                    {bulkEnvMutation.isPending
                      ? <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Importing…</>
                      : envPreviewRows.filter(r => r.key.trim()).length > 0
                        ? `Import ${envPreviewRows.filter(r => r.key.trim()).length} variable${envPreviewRows.filter(r => r.key.trim()).length !== 1 ? 's' : ''}`
                        : 'Import'}
                  </Button>
                </div>
              </div>
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

// Convert a UTC hour (0–23) into a display label adjusted for the given IANA timezone.
// e.g. UTC hour 8 → "2PM" for Asia/Kolkata (UTC+5:30)
function utcHourToTzLabel(utcHour: number, tz: string): string {
  // Build a fake Date at that UTC hour on a reference day and convert to timezone
  const ref = new Date(Date.UTC(2000, 0, 1, utcHour, 0, 0))
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      hour: 'numeric',
      hour12: true,
    }).formatToParts(ref)
    const h = parts.find(p => p.type === 'hour')?.value ?? String(utcHour)
    const period = parts.find(p => p.type === 'dayPeriod')?.value?.toUpperCase() ?? ''
    return `${h}${period}`
  } catch {
    // fallback: plain UTC
    const suffix = utcHour < 12 ? 'AM' : 'PM'
    const display = utcHour % 12 === 0 ? 12 : utcHour % 12
    return `${display}${suffix}`
  }
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
  const { latest, history: liveHistory, status } = useContainerStatsSSE(projectId, serviceId, enabled)
  const [mode, setMode] = useState<'live' | 'history' | 'monthly'>('live')
  const [range, setRange] = useState<StatsRange>('24h')
  const { timezone } = useTimezone()
  const fmtTs = (ms: number) => formatTimestamp(ms, timezone)
  const fmtTsFull = (ms: number) => formatTimestampFull(ms, timezone)

  const { data: histData, isLoading: histLoading } = useQuery<StatsHistoryResponse>({
    queryKey: ['stats-history', projectId, serviceId, range],
    queryFn: () => statsApi.getHistory(projectId!, serviceId!, range),
    enabled: !!projectId && !!serviceId && mode === 'history',
    refetchInterval: 60_000,
  })

  const { data: monthData, isLoading: monthLoading } = useQuery<MonthlyHistoryResponse>({
    queryKey: ['stats-monthly', projectId, serviceId],
    queryFn: () => monthlyStatsApi.getMonthly(projectId!, serviceId!),
    enabled: !!projectId && !!serviceId && mode === 'monthly',
    refetchInterval: 5 * 60_000,
  })

  const cpuData = liveHistory.map(p => ({ t: p.ts, v: Number(p.cpuPct.toFixed(2)) }))
  const memData = liveHistory.map(p => ({ t: p.ts, v: Number(p.memPct.toFixed(2)) }))

  // History chart data
  const hCpuData  = (histData?.points ?? []).map(p => ({ t: p.ts, v: Number(p.cpu_pct.toFixed(2)) }))
  const hMemData  = (histData?.points ?? []).map(p => ({ t: p.ts, v: Number(p.mem_pct.toFixed(2)) }))
  const hNetData  = (histData?.points ?? []).map(p => ({ t: p.ts, vIn: p.net_in, vOut: p.net_out }))
  const hBlkData  = (histData?.points ?? []).map(p => ({ t: p.ts, vIn: p.blk_in, vOut: p.blk_out }))
  const hourlyData = (histData?.hourly_avg ?? []).map(h => ({
    hour: utcHourToTzLabel(h.hour, timezone),
    rawHour: h.hour,
    cpu: h.cpu_avg,
    mem: h.mem_avg,
    count: h.samples,
  }))

  // Find peak hour for the callout
  const peakCPUHour = hourlyData.length > 0
    ? hourlyData.reduce((a, b) => b.cpu > a.cpu ? b : a)
    : null

  const RANGES: StatsRange[] = ['1h', '6h', '24h', '7d']

  return (
    <div className="space-y-5">
      {/* ── Mode / range controls ──────────────────────────────────────────── */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div className="flex items-center gap-1.5 rounded-lg border border-border/60 p-0.5 bg-muted/40">
          <button
            className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${mode === 'live' ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
            onClick={() => setMode('live')}
          >Live</button>
          <button
            className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${mode === 'history' ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
            onClick={() => setMode('history')}
          >History</button>
          <button
            className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${mode === 'monthly' ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
            onClick={() => setMode('monthly')}
          >Monthly</button>
        </div>

        {mode === 'history' && (
          <div className="flex items-center gap-1 rounded-lg border border-border/60 p-0.5 bg-muted/40">
            {RANGES.map(r => (
              <button
                key={r}
                className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${range === r ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
                onClick={() => setRange(r)}
              >{r.toUpperCase()}</button>
            ))}
          </div>
        )}

        {mode === 'live' && (
          <div className="flex items-center gap-1.5 text-xs">
            <CircleDot className={`h-3 w-3 ${
              status === 'running'   ? 'text-emerald-500 animate-pulse' :
              status === 'not_found' ? 'text-amber-500' : 'text-muted-foreground'
            }`} />
            <span className="text-muted-foreground capitalize">{status}</span>
          </div>
        )}
      </div>

      {/* ══════════════════════════ LIVE MODE ══════════════════════════════ */}
      {mode === 'live' && (
        <>
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
        </>
      )}

      {/* ══════════════════════════ HISTORY MODE ═══════════════════════════ */}
      {mode === 'history' && (
        <>
          {histLoading && (
            <div className="grid gap-4 sm:grid-cols-2">
              {[...Array(6)].map((_, i) => (
                <div key={i} className="h-32 rounded-xl bg-muted animate-pulse" />
              ))}
            </div>
          )}

          {!histLoading && histData && (
            <>
              {/* ── Peak values ─────────────────────────────────────────── */}
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                {[
                  { label: 'Peak CPU', icon: Cpu, value: `${histData.peaks.cpu.value.toFixed(1)}%`, ts: histData.peaks.cpu.ts, color: 'text-violet-500' },
                  { label: 'Peak Memory', icon: MemoryStick, value: `${histData.peaks.mem.value.toFixed(1)}%`, ts: histData.peaks.mem.ts, color: 'text-emerald-500' },
                  { label: 'Peak Net In', icon: Network, value: fmtBytes(histData.peaks.net_in.value), ts: histData.peaks.net_in.ts, color: 'text-sky-500' },
                  { label: 'Peak Net Out', icon: Network, value: fmtBytes(histData.peaks.net_out.value), ts: histData.peaks.net_out.ts, color: 'text-orange-500' },
                ].map(({ label, icon: Icon, value, ts, color }) => (
                  <div key={label} className="rounded-xl border border-border/60 bg-card p-4">
                    <div className={`flex items-center gap-2 mb-1 text-xs font-medium uppercase tracking-wide ${color}`}>
                      <Icon className="h-3.5 w-3.5" /> {label}
                    </div>
                    <p className="text-2xl font-bold tabular-nums">{value}</p>
                    {ts > 0 && (
                      <p className="text-xs text-muted-foreground mt-1">{fmtTsFull(ts)}</p>
                    )}
                  </div>
                ))}
              </div>

              {histData.points.length === 0 ? (
                <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
                  No data yet for this range. Stats are sampled every 60 s while the container is running.
                </div>
              ) : (
                <>
                  {/* ── CPU over time ───────────────────────────────────── */}
                  <div className="rounded-xl border border-border/60 bg-card p-4">
                    <p className="text-sm font-medium mb-3 flex items-center gap-2">
                      <Cpu className="h-4 w-4 text-muted-foreground" /> CPU % — {range.toUpperCase()}
                    </p>
                    <ResponsiveContainer width="100%" height={160}>
                      <AreaChart data={hCpuData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                        <defs>
                          <linearGradient id="hCpuGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#6366f1" stopOpacity={0.35} />
                            <stop offset="100%" stopColor="#6366f1" stopOpacity={0.03} />
                          </linearGradient>
                        </defs>
                        <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                        <XAxis dataKey="t" tickFormatter={fmtTs} tick={{ fontSize: 9 }} interval="preserveStartEnd" />
                        <YAxis domain={[0, 100]} tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={36} />
                        <Tooltip
                          contentStyle={{ fontSize: 12, borderRadius: 8 }}
                          formatter={(v: unknown) => [`${Number(v).toFixed(2)}%`, 'CPU']}
                          labelFormatter={(l) => fmtTsFull(Number(l))}
                        />
                        <ReferenceLine y={histData.peaks.cpu.value} stroke="#6366f1" strokeDasharray="4 4" strokeOpacity={0.5} />
                        <Area type="monotone" dataKey="v" stroke="#6366f1" strokeWidth={2}
                          fill="url(#hCpuGrad)" dot={false} isAnimationActive={false} />
                      </AreaChart>
                    </ResponsiveContainer>
                  </div>

                  {/* ── Memory over time ─────────────────────────────────── */}
                  <div className="rounded-xl border border-border/60 bg-card p-4">
                    <p className="text-sm font-medium mb-3 flex items-center gap-2">
                      <MemoryStick className="h-4 w-4 text-muted-foreground" /> Memory % — {range.toUpperCase()}
                    </p>
                    <ResponsiveContainer width="100%" height={160}>
                      <AreaChart data={hMemData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                        <defs>
                          <linearGradient id="hMemGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#10b981" stopOpacity={0.35} />
                            <stop offset="100%" stopColor="#10b981" stopOpacity={0.03} />
                          </linearGradient>
                        </defs>
                        <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                        <XAxis dataKey="t" tickFormatter={fmtTs} tick={{ fontSize: 9 }} interval="preserveStartEnd" />
                        <YAxis domain={[0, 100]} tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={36} />
                        <Tooltip
                          contentStyle={{ fontSize: 12, borderRadius: 8 }}
                          formatter={(v: unknown) => [`${Number(v).toFixed(2)}%`, 'Mem']}
                          labelFormatter={(l) => fmtTsFull(Number(l))}
                        />
                        <ReferenceLine y={histData.peaks.mem.value} stroke="#10b981" strokeDasharray="4 4" strokeOpacity={0.5} />
                        <Area type="monotone" dataKey="v" stroke="#10b981" strokeWidth={2}
                          fill="url(#hMemGrad)" dot={false} isAnimationActive={false} />
                      </AreaChart>
                    </ResponsiveContainer>
                  </div>

                  {/* ── Network I/O over time ──────────────────────────── */}
                  <div className="rounded-xl border border-border/60 bg-card p-4">
                    <p className="text-sm font-medium mb-3 flex items-center gap-2">
                      <Network className="h-4 w-4 text-muted-foreground" /> Network I/O — {range.toUpperCase()}
                    </p>
                    <ResponsiveContainer width="100%" height={160}>
                      <AreaChart data={hNetData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                        <defs>
                          <linearGradient id="netInGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#0ea5e9" stopOpacity={0.3} />
                            <stop offset="100%" stopColor="#0ea5e9" stopOpacity={0.02} />
                          </linearGradient>
                          <linearGradient id="netOutGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#f97316" stopOpacity={0.3} />
                            <stop offset="100%" stopColor="#f97316" stopOpacity={0.02} />
                          </linearGradient>
                        </defs>
                        <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                        <XAxis dataKey="t" tickFormatter={fmtTs} tick={{ fontSize: 9 }} interval="preserveStartEnd" />
                        <YAxis tickFormatter={v => fmtBytes(v)} tick={{ fontSize: 9 }} width={52} />
                        <Tooltip
                          contentStyle={{ fontSize: 12, borderRadius: 8 }}
                          formatter={(v: unknown) => [fmtBytes(Number(v)), '']}
                          labelFormatter={(l) => fmtTsFull(Number(l))}
                        />
                        <Legend iconSize={8} wrapperStyle={{ fontSize: 11 }} />
                        <Area type="monotone" dataKey="vIn" name="Rx" stroke="#0ea5e9" strokeWidth={2}
                          fill="url(#netInGrad)" dot={false} isAnimationActive={false} />
                        <Area type="monotone" dataKey="vOut" name="Tx" stroke="#f97316" strokeWidth={2}
                          fill="url(#netOutGrad)" dot={false} isAnimationActive={false} />
                      </AreaChart>
                    </ResponsiveContainer>
                  </div>

                  {/* ── Block I/O over time ────────────────────────────── */}
                  <div className="rounded-xl border border-border/60 bg-card p-4">
                    <p className="text-sm font-medium mb-3 flex items-center gap-2">
                      <HardDrive className="h-4 w-4 text-muted-foreground" /> Block I/O — {range.toUpperCase()}
                    </p>
                    <ResponsiveContainer width="100%" height={160}>
                      <AreaChart data={hBlkData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                        <defs>
                          <linearGradient id="blkInGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#a855f7" stopOpacity={0.3} />
                            <stop offset="100%" stopColor="#a855f7" stopOpacity={0.02} />
                          </linearGradient>
                          <linearGradient id="blkOutGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="#ec4899" stopOpacity={0.3} />
                            <stop offset="100%" stopColor="#ec4899" stopOpacity={0.02} />
                          </linearGradient>
                        </defs>
                        <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.5} />
                        <XAxis dataKey="t" tickFormatter={fmtTs} tick={{ fontSize: 9 }} interval="preserveStartEnd" />
                        <YAxis tickFormatter={v => fmtBytes(v)} tick={{ fontSize: 9 }} width={52} />
                        <Tooltip
                          contentStyle={{ fontSize: 12, borderRadius: 8 }}
                          formatter={(v: unknown) => [fmtBytes(Number(v)), '']}
                          labelFormatter={(l) => fmtTsFull(Number(l))}
                        />
                        <Legend iconSize={8} wrapperStyle={{ fontSize: 11 }} />
                        <Area type="monotone" dataKey="vIn" name="Read" stroke="#a855f7" strokeWidth={2}
                          fill="url(#blkInGrad)" dot={false} isAnimationActive={false} />
                        <Area type="monotone" dataKey="vOut" name="Write" stroke="#ec4899" strokeWidth={2}
                          fill="url(#blkOutGrad)" dot={false} isAnimationActive={false} />
                      </AreaChart>
                    </ResponsiveContainer>
                  </div>
                </>
              )}

              {/* ── Busiest hours of the day ───────────────────────────── */}
              {hourlyData.length > 0 && (
                <div className="rounded-xl border border-border/60 bg-card p-4">
                  <div className="flex items-start justify-between mb-3">
                    <p className="text-sm font-medium flex items-center gap-2">
                      <Activity className="h-4 w-4 text-muted-foreground" /> Busiest hours of the day
                    </p>
                    {peakCPUHour && (
                      <p className="text-xs text-muted-foreground">
                        Peak at <span className="font-medium text-foreground">{peakCPUHour.hour}</span> — avg CPU <span className="font-medium text-violet-500">{peakCPUHour.cpu}%</span>
                      </p>
                    )}
                  </div>
                  <ResponsiveContainer width="100%" height={160}>
                    <BarChart data={hourlyData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }} barGap={2}>
                      <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.4} vertical={false} />
                      <XAxis dataKey="hour" tick={{ fontSize: 9 }} />
                      <YAxis tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={32} domain={[0, 100]} />
                      <Tooltip
                        contentStyle={{ fontSize: 12, borderRadius: 8 }}
                        formatter={(v: unknown, name) => [`${Number(v).toFixed(1)}%`, name === 'cpu' ? 'Avg CPU' : 'Avg Mem']}
                        labelFormatter={(l) => `Hour: ${l}`}
                      />
                      <Legend iconSize={8} wrapperStyle={{ fontSize: 11 }} />
                      <Bar dataKey="cpu" name="CPU" fill="#6366f1" radius={[2, 2, 0, 0]} maxBarSize={20} isAnimationActive={false} />
                      <Bar dataKey="mem" name="Memory" fill="#10b981" radius={[2, 2, 0, 0]} maxBarSize={20} isAnimationActive={false} />
                    </BarChart>
                  </ResponsiveContainer>
                </div>
              )}
            </>
          )}
        </>
      )}

      {/* ══════════════════════════ MONTHLY MODE ═══════════════════════════ */}
      {mode === 'monthly' && (
        <>
          {monthLoading && (
            <div className="grid gap-4 sm:grid-cols-2">
              {[...Array(4)].map((_, i) => (
                <div key={i} className="h-48 rounded-xl bg-muted animate-pulse" />
              ))}
            </div>
          )}

          {!monthLoading && monthData && monthData.months.length === 0 && (
            <div className="rounded-lg border border-dashed py-14 text-center text-sm text-muted-foreground">
              <Activity className="h-8 w-8 mx-auto mb-3 opacity-30" />
              <p className="font-medium">No monthly data yet</p>
              <p className="text-xs mt-1 max-w-xs mx-auto">
                Monthly summaries are created at each month rollover from raw data.
                Check back after the service has been running for at least one full month.
              </p>
            </div>
          )}

          {!monthLoading && monthData && monthData.months.map((m) => {
            const peakH = m.hourly.length > 0
              ? m.hourly.reduce((a, b) => b.cpu_avg > a.cpu_avg ? b : a)
              : null
            return (
              <div key={`${m.year}-${m.month}`} className="rounded-xl border border-border/60 bg-card p-4">
                <div className="flex items-start justify-between mb-3">
                  <p className="text-sm font-medium flex items-center gap-2">
                    <Activity className="h-4 w-4 text-muted-foreground" />
                    {m.label} — hourly averages
                  </p>
                  {peakH && (
                    <p className="text-xs text-muted-foreground">
                      Peak hour{' '}
                      <span className="font-medium text-foreground">{utcHourToTzLabel(peakH.hour, timezone)}</span>
                      {' '}— {peakH.cpu_avg.toFixed(1)}% CPU avg
                    </p>
                  )}
                </div>
                <ResponsiveContainer width="100%" height={160}>
                  <BarChart
                    data={m.hourly.map(h => ({ hour: utcHourToTzLabel(h.hour, timezone), cpu: h.cpu_avg, mem: h.mem_avg, samples: h.samples }))}
                    margin={{ top: 4, right: 8, bottom: 0, left: 0 }}
                    barGap={2}
                  >
                    <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" strokeOpacity={0.4} vertical={false} />
                    <XAxis dataKey="hour" tick={{ fontSize: 9 }} />
                    <YAxis tickFormatter={v => `${v}%`} tick={{ fontSize: 10 }} width={32} domain={[0, 100]} />
                    <Tooltip
                      contentStyle={{ fontSize: 12, borderRadius: 8 }}
                      formatter={(v: unknown, name) => [`${Number(v).toFixed(1)}%`, name === 'cpu' ? 'Avg CPU' : 'Avg Mem']}
                      labelFormatter={(l) => `${m.label} — ${l}`}
                    />
                    <Legend iconSize={8} wrapperStyle={{ fontSize: 11 }} />
                    <Bar dataKey="cpu" name="CPU" fill="#6366f1" radius={[2, 2, 0, 0]} maxBarSize={20} isAnimationActive={false} />
                    <Bar dataKey="mem" name="Memory" fill="#10b981" radius={[2, 2, 0, 0]} maxBarSize={20} isAnimationActive={false} />
                  </BarChart>
                </ResponsiveContainer>
                <p className="text-xs text-muted-foreground mt-2">
                  {m.hourly.reduce((s, h) => s + h.samples, 0).toLocaleString()} total samples — raw data rolled up at month end then deleted.
                </p>
              </div>
            )
          })}
        </>
      )}
    </div>
  )
}

