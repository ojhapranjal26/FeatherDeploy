import { useState, useRef, useEffect } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Plus, ChevronLeft, Rocket, Settings2, Trash2,
  ExternalLink, GitBranch, Terminal, Database,
  Globe, AlertTriangle, Users, UserMinus, Loader2,
  Play, Square, Download, Copy, BarChart2, RotateCcw,
} from 'lucide-react'
import { toast } from 'sonner'
import { projectsApi, usersApi, type ProjectMember } from '@/api/projects'
import { servicesApi } from '@/api/services'
import { databasesApi, type DatabaseRecord, type DatabaseType, type UpdateDatabasePayload } from '@/api/databases'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

function ServiceCard({ service, projectId, canEdit }: { service: Service; projectId: string; canEdit: boolean }) {
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
                {canEdit && (
                  <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/env`)}>
                    <Settings2 className="mr-2 h-3.5 w-3.5" /> Environment
                  </DropdownMenuItem>
                )}
                {canEdit && (
                  <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/domains`)}>
                    <Globe className="mr-2 h-3.5 w-3.5" /> Domains
                  </DropdownMenuItem>
                )}
                {canEdit && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      className="text-destructive focus:text-destructive"
                      onClick={() => setConfirmDelete(true)}
                    >
                      <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete service
                    </DropdownMenuItem>
                  </>
                )}
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
            {canEdit && (
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
            )}
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

function databaseTypeLabel(dbType: DatabaseType) {
  switch (dbType) {
    case 'postgres':
      return 'Postgres'
    case 'mysql':
      return 'MySQL'
    case 'sqlite':
      return 'SQLite'
  }
}

function defaultDatabaseVersion(dbType: DatabaseType) {
  switch (dbType) {
    case 'postgres':
      return '16'
    case 'mysql':
      return '8.4'
    case 'sqlite':
      return '3'
  }
}

function downloadBlob(blob: Blob, filename: string) {
  const url = window.URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  window.setTimeout(() => window.URL.revokeObjectURL(url), 1000)
}

function DatabaseCard({ database, projectId, canEdit }: { database: DatabaseRecord; projectId: string; canEdit: boolean }) {
  const qc = useQueryClient()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [logsOpen, setLogsOpen] = useState(false)
  const [statsOpen, setStatsOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [editVersion, setEditVersion] = useState(database.db_version)
  const [editPublic, setEditPublic] = useState(database.network_public)

  const copyToClipboard = (text: string, label: string) => {
    navigator.clipboard.writeText(text).then(
      () => toast.success(`${label} copied to clipboard.`),
      () => toast.error('Failed to copy to clipboard.'),
    )
  }

  const startMutation = useMutation({
    mutationFn: () => databasesApi.start(projectId, database.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      toast.success('Database start requested.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to start database.'),
  })

  const stopMutation = useMutation({
    mutationFn: () => databasesApi.stop(projectId, database.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      toast.success('Database stop requested.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to stop database.'),
  })

  const restartMutation = useMutation({
    mutationFn: () => databasesApi.restart(projectId, database.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      toast.success('Database restart requested — container will be recreated.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to restart database.'),
  })

  const deleteMutation = useMutation({
    mutationFn: () => databasesApi.delete(projectId, database.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      toast.success('Database deleted and its data volume was removed.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to delete database.'),
  })

  const backupMutation = useMutation({
    mutationFn: async () => {
      const download = await databasesApi.downloadBackup(projectId, database.id)
      downloadBlob(download.blob, download.filename)
    },
    onSuccess: () => toast.success('Database backup downloaded.'),
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to download backup.'),
  })

  const backupAndDeleteMutation = useMutation({
    mutationFn: async () => {
      const download = await databasesApi.downloadBackup(projectId, database.id)
      downloadBlob(download.blob, download.filename)
      await databasesApi.delete(projectId, database.id)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      toast.success('Backup downloaded and database deleted.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to back up and delete database.'),
  })

  const updateMutation = useMutation({
    mutationFn: (data: UpdateDatabasePayload) => databasesApi.update(projectId, database.id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      setEditOpen(false)
      toast.success('Database configuration updated. Restart the database for changes to take effect.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to update database.'),
  })

  const logsQuery = useQuery({
    queryKey: ['database-logs', database.id],
    queryFn: () => databasesApi.getLogs(projectId, database.id),
    enabled: logsOpen,
    refetchInterval: logsOpen ? 5000 : false,
  })

  const isSQLite = database.db_type === 'sqlite'
  const isBusy = startMutation.isPending || stopMutation.isPending || restartMutation.isPending || deleteMutation.isPending || backupMutation.isPending || backupAndDeleteMutation.isPending

  return (
    <>
      <Card className="group relative flex flex-col overflow-hidden transition-shadow hover:shadow-md">
        <div className={cn(
          'absolute inset-x-0 top-0 h-0.5',
          database.status === 'running' ? 'bg-emerald-500' :
          database.status === 'starting' ? 'bg-amber-400 animate-pulse' :
          database.status === 'error' ? 'bg-red-500' :
          'bg-border',
        )} />
        <CardHeader className="pb-2 pt-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <CardTitle className="text-sm font-semibold truncate">{database.name}</CardTitle>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                <Badge variant={database.status === 'running' ? 'default' : database.status === 'error' ? 'destructive' : 'secondary'} className="text-[10px] px-1.5 py-0 capitalize">
                  {database.status}
                </Badge>
                <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                  {databaseTypeLabel(database.db_type)}
                </Badge>
                {!isSQLite && (
                  <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                    {database.db_version}
                  </Badge>
                )}
              </div>
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" />}>
                <Settings2 className="h-3.5 w-3.5" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {!isSQLite && (
                  <DropdownMenuItem onClick={() => { setEditVersion(database.db_version); setEditPublic(database.network_public); setEditOpen(true) }}>
                    <Settings2 className="mr-2 h-3.5 w-3.5" /> Edit configuration
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={() => setLogsOpen(true)}>
                  <Terminal className="mr-2 h-3.5 w-3.5" /> View logs
                </DropdownMenuItem>
                {!isSQLite && database.status === 'running' && (
                  <DropdownMenuItem onClick={() => setStatsOpen(true)}>
                    <BarChart2 className="mr-2 h-3.5 w-3.5" /> View stats
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={() => backupMutation.mutate()} disabled={backupMutation.isPending || backupAndDeleteMutation.isPending}>
                  <Download className="mr-2 h-3.5 w-3.5" /> Download backup
                </DropdownMenuItem>
                {!isSQLite && database.status !== 'running' && (
                  <DropdownMenuItem onClick={() => startMutation.mutate()} disabled={isBusy}>
                    <Play className="mr-2 h-3.5 w-3.5" /> Start database
                  </DropdownMenuItem>
                )}
                {!isSQLite && database.status === 'running' && (
                  <DropdownMenuItem onClick={() => stopMutation.mutate()} disabled={isBusy}>
                    <Square className="mr-2 h-3.5 w-3.5" /> Stop database
                  </DropdownMenuItem>
                )}
                {!isSQLite && (
                  <DropdownMenuItem onClick={() => restartMutation.mutate()} disabled={isBusy}>
                    <RotateCcw className="mr-2 h-3.5 w-3.5" /> Restart database
                  </DropdownMenuItem>
                )}
                {canEdit && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      className="text-destructive focus:text-destructive"
                      onClick={() => setConfirmDelete(true)}
                    >
                      <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete database
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </CardHeader>

        <CardContent className="flex flex-col gap-3 flex-1 pb-4">
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Database className="h-3.5 w-3.5 shrink-0" />
            <span>{database.db_name}</span>
          </div>

          {database.env_var_name && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground overflow-hidden">
              <Terminal className="h-3 w-3 shrink-0" />
              <span className="truncate font-mono text-[11px]">{database.env_var_name}</span>
            </div>
          )}

          {database.status === 'error' && database.last_error && (
            <div className="rounded-md border border-red-200 bg-red-50/80 px-2 py-1.5 text-red-900">
              <p className="text-[10px] uppercase tracking-wide text-red-700 font-medium">Error</p>
              <p className="mt-1 break-words font-mono text-[11px]">{database.last_error}</p>
            </div>
          )}

          {database.connection_url && (
            <div className="rounded-md border bg-muted/50 px-2 py-1.5">
              <div className="flex items-center justify-between gap-1">
                <p className="text-[10px] uppercase tracking-wide text-muted-foreground">Connection (private)</p>
                <button
                  className="shrink-0 text-muted-foreground hover:text-foreground transition-colors"
                  title="Copy connection URL"
                  onClick={() => copyToClipboard(database.connection_url!, 'Private URL')}
                >
                  <Copy className="h-3 w-3" />
                </button>
              </div>
              <p className="mt-1 break-all font-mono text-[11px] text-foreground">{database.connection_url}</p>
            </div>
          )}

          {database.public_connection_url && (
            <div className="rounded-md border border-amber-200 bg-amber-50/80 px-2 py-1.5 text-amber-950">
              <div className="flex items-center justify-between gap-1">
                <p className="text-[10px] uppercase tracking-wide text-amber-800">Public access</p>
                <button
                  className="shrink-0 text-amber-700 hover:text-amber-900 transition-colors"
                  title="Copy public connection URL"
                  onClick={() => copyToClipboard(database.public_connection_url!, 'Public URL')}
                >
                  <Copy className="h-3 w-3" />
                </button>
              </div>
              <p className="mt-1 break-all font-mono text-[11px]">{database.public_connection_url}</p>
            </div>
          )}

          {isSQLite && (
            <p className="text-xs text-muted-foreground">
              SQLite is mounted into services on the next deployment rather than exposed as a network container.
            </p>
          )}
        </CardContent>
      </Card>

      {/* Logs dialog */}
      <Dialog open={logsOpen} onOpenChange={setLogsOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Terminal className="h-4 w-4" />
              Logs — {database.name}
            </DialogTitle>
            <DialogDescription>
              Container: <span className="font-mono">fd-db-{database.id}</span>
              {logsQuery.isFetching && <span className="ml-2 text-muted-foreground">(refreshing…)</span>}
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-md border bg-black/90 p-3 max-h-96 overflow-y-auto">
            {logsQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">Loading…</p>
            ) : logsQuery.data?.logs ? (
              <pre className="whitespace-pre-wrap font-mono text-[11px] text-green-300">
                {logsQuery.data.logs}
              </pre>
            ) : (
              <p className="text-xs text-muted-foreground">No logs available.</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setLogsOpen(false)}>Close</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit configuration dialog */}
      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Edit database configuration</DialogTitle>
            <DialogDescription>
              Changes take effect after restarting the database container.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="edit-db-version">Version tag</Label>
              <Input
                id="edit-db-version"
                value={editVersion}
                onChange={(e) => setEditVersion(e.target.value)}
                placeholder="e.g. 16, latest, 8.4"
                className="font-mono"
              />
            </div>
            <div className="flex items-center gap-3">
              <input
                id="edit-db-public"
                type="checkbox"
                checked={editPublic}
                onChange={(e) => setEditPublic(e.target.checked)}
                className="h-4 w-4 rounded border-input"
              />
              <Label htmlFor="edit-db-public" className="cursor-pointer">
                Expose on a host port (public access)
              </Label>
            </div>
          </div>
          <DialogFooter className="gap-2">
            <Button variant="ghost" size="sm" onClick={() => setEditOpen(false)} disabled={updateMutation.isPending}>Cancel</Button>
            <Button
              size="sm"
              disabled={updateMutation.isPending}
              onClick={() => updateMutation.mutate({ db_version: editVersion, network_public: editPublic })}
            >
              {updateMutation.isPending ? 'Saving…' : 'Save changes'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete database
            </DialogTitle>
            <DialogDescription>
              This removes <strong>{database.name}</strong> and purges its stored data volume. Download a backup first if you may need the data later.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2 sm:justify-between">
            <Button variant="ghost" size="sm" onClick={() => setConfirmDelete(false)} disabled={isBusy}>Cancel</Button>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={isBusy}
                onClick={() => {
                  setConfirmDelete(false)
                  backupAndDeleteMutation.mutate()
                }}
              >
                {backupAndDeleteMutation.isPending ? 'Backing up…' : 'Backup then delete'}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                disabled={isBusy}
                onClick={() => {
                  setConfirmDelete(false)
                  deleteMutation.mutate()
                }}
              >
                {deleteMutation.isPending ? 'Deleting…' : 'Delete now'}
              </Button>
            </div>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Stats dialog */}
      <Dialog open={statsOpen} onOpenChange={setStatsOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <BarChart2 className="h-4 w-4" />
              Stats — {database.name}
            </DialogTitle>
            <DialogDescription>
              Container: <span className="font-mono">fd-db-{database.id}</span>
            </DialogDescription>
          </DialogHeader>
          <DatabaseStatsPanel
            projectId={projectId}
            databaseId={String(database.id)}
            enabled={statsOpen}
          />
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setStatsOpen(false)}>Close</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// ─── Database stats panel ─────────────────────────────────────────────────────

function fmtBytes(b: number): string {
  if (b >= 1073741824) return `${(b / 1073741824).toFixed(2)} GB`
  if (b >= 1048576) return `${(b / 1048576).toFixed(1)} MB`
  if (b >= 1024) return `${(b / 1024).toFixed(0)} KB`
  return `${b} B`
}

function DatabaseStatsPanel({
  projectId,
  databaseId,
  enabled,
}: {
  projectId: string
  databaseId: string
  enabled: boolean
}) {
  const [state, setState] = useState<{
    latest: { cpuPct: number; memUsed: number; memTotal: number; memPct: number; netIn: number; netOut: number; blkIn: number; blkOut: number; pids: number } | null
    status: 'connecting' | 'running' | 'not_found' | 'error'
  }>({ latest: null, status: 'connecting' })
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!enabled || !projectId || !databaseId) return
    const token = localStorage.getItem('token')
    if (!token) return

    const url = `/api/projects/${projectId}/databases/${databaseId}/stats/stream?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef.current = es

    es.addEventListener('stats', (e: MessageEvent) => {
      try {
        const raw = JSON.parse(e.data)
        setState({
          status: raw.status === 'not_found' ? 'not_found' : 'running',
          latest: {
            cpuPct: raw.cpu_pct,
            memUsed: raw.mem_used,
            memTotal: raw.mem_total,
            memPct: raw.mem_pct,
            netIn: raw.net_in,
            netOut: raw.net_out,
            blkIn: raw.blk_in,
            blkOut: raw.blk_out,
            pids: raw.pids,
          },
        })
      } catch { /* ignore */ }
    })
    es.onerror = () => setState(prev => ({ ...prev, status: 'error' }))

    return () => {
      es.close()
      esRef.current = null
      setState({ latest: null, status: 'connecting' })
    }
  }, [enabled, projectId, databaseId])

  const { latest, status } = state

  if (status === 'not_found') {
    return <p className="text-sm text-muted-foreground py-4 text-center">Container is not running.</p>
  }
  if (status === 'error') {
    return <p className="text-sm text-destructive py-4 text-center">Failed to connect to stats stream. Is the container running?</p>
  }
  if (!latest) {
    return <p className="text-sm text-muted-foreground py-4 text-center animate-pulse">Connecting…</p>
  }

  return (
    <div className="grid grid-cols-2 gap-3 py-2">
      <div className="rounded-lg border p-3 space-y-0.5">
        <p className="text-[10px] uppercase tracking-wide text-muted-foreground">CPU</p>
        <p className="font-mono text-lg font-semibold">{latest.cpuPct.toFixed(1)}%</p>
      </div>
      <div className="rounded-lg border p-3 space-y-0.5">
        <p className="text-[10px] uppercase tracking-wide text-muted-foreground">Memory</p>
        <p className="font-mono text-lg font-semibold">{latest.memPct.toFixed(1)}%</p>
        <p className="text-[11px] text-muted-foreground font-mono">{fmtBytes(latest.memUsed)} / {fmtBytes(latest.memTotal)}</p>
      </div>
      <div className="rounded-lg border p-3 space-y-0.5">
        <p className="text-[10px] uppercase tracking-wide text-muted-foreground">Network I/O</p>
        <p className="text-xs font-mono">↓ {fmtBytes(latest.netIn)} &nbsp; ↑ {fmtBytes(latest.netOut)}</p>
      </div>
      <div className="rounded-lg border p-3 space-y-0.5">
        <p className="text-[10px] uppercase tracking-wide text-muted-foreground">Disk I/O</p>
        <p className="text-xs font-mono">R {fmtBytes(latest.blkIn)} &nbsp; W {fmtBytes(latest.blkOut)}</p>
      </div>
      <div className="rounded-lg border p-3 space-y-0.5">
        <p className="text-[10px] uppercase tracking-wide text-muted-foreground">PIDs</p>
        <p className="font-mono text-lg font-semibold">{latest.pids}</p>
      </div>
    </div>
  )
}

// ─── Main page ────────────────────────────────────────────────────────────────

export function ProjectPage() {
  const { projectId } = useParams<{ projectId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [newServiceOpen, setNewServiceOpen] = useState(false)
  const [newDatabaseOpen, setNewDatabaseOpen] = useState(false)
  const [confirmDeleteProject, setConfirmDeleteProject] = useState(false)
  const [membersOpen, setMembersOpen] = useState(false)
  const [addMemberEmail, setAddMemberEmail] = useState('')
  const [addMemberEmailDisplay, setAddMemberEmailDisplay] = useState('')
  const [addMemberSearchQ, setAddMemberSearchQ] = useState('')
  const [addMemberDropdownOpen, setAddMemberDropdownOpen] = useState(false)
  const [addMemberRole, setAddMemberRole] = useState<'owner' | 'editor' | 'viewer'>('editor')
  const [newDbName, setNewDbName] = useState('')
  const [newDbType, setNewDbType] = useState<DatabaseType>('postgres')
  const [newDbVersion, setNewDbVersion] = useState(defaultDatabaseVersion('postgres'))
  const [newDbDatabaseName, setNewDbDatabaseName] = useState('')
  const [newDbUser, setNewDbUser] = useState('')
  const [newDbPassword, setNewDbPassword] = useState('')
  const [newDbAccess, setNewDbAccess] = useState<'private' | 'public'>('private')
  const searchRef = useRef<HTMLDivElement>(null)

  const { data: project, isLoading: projLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => projectsApi.get(projectId!),
    enabled: !!projectId,
  })

  const canEditProject = project?.my_role === 'owner' || project?.my_role === 'editor'
  const isOwner = project?.my_role === 'owner'

  const { data: members } = useQuery({
    queryKey: ['project-members', projectId],
    queryFn: () => projectsApi.listMembers(projectId!),
    enabled: !!projectId && isOwner,
  })

  const { data: userSearchResults } = useQuery({
    queryKey: ['users-search', addMemberSearchQ],
    queryFn: () => usersApi.search(addMemberSearchQ),
    enabled: membersOpen && addMemberSearchQ.length >= 1,
    staleTime: 10_000,
  })

  const addMemberMutation = useMutation({
    mutationFn: async () => {
      const user = await usersApi.lookup(addMemberEmail.trim())
      return projectsApi.addMember(projectId!, user.id, addMemberRole)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      setAddMemberEmail('')
      setAddMemberEmailDisplay('')
      setAddMemberSearchQ('')
      toast.success('Member added.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to add member.'),
  })

  const updateMemberMutation = useMutation({
    mutationFn: ({ userId, role }: { userId: number; role: 'owner' | 'editor' | 'viewer' }) =>
      projectsApi.updateMember(projectId!, userId, role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      toast.success('Role updated.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to update role.'),
  })

  const removeMemberMutation = useMutation({
    mutationFn: (userId: number) => projectsApi.removeMember(projectId!, userId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      toast.success('Member removed.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to remove member.'),
  })

  const { data: services, isLoading: svcLoading } = useQuery({
    queryKey: ['services', projectId],
    queryFn: () => servicesApi.list(projectId!),
    enabled: !!projectId,
    refetchInterval: 5000,
  })

  const { data: databases, isLoading: dbLoading } = useQuery({
    queryKey: ['databases', projectId],
    queryFn: () => databasesApi.list(projectId!),
    enabled: !!projectId && canEditProject,
    refetchInterval: 5000,
  })

  const [newSvcName, setNewSvcName] = useState('')

  function resetNewDatabaseForm() {
    setNewDbName('')
    setNewDbType('postgres')
    setNewDbVersion(defaultDatabaseVersion('postgres'))
    setNewDbDatabaseName('')
    setNewDbUser('')
    setNewDbPassword('')
    setNewDbAccess('private')
  }

  const createSvcMutation = useMutation({
    mutationFn: () => servicesApi.create(projectId!, { name: newSvcName }),
    onSuccess: (service) => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      setNewServiceOpen(false)
      setNewSvcName('')
      toast.success('Service created — configure your source in the Settings tab.')
      navigate(`/projects/${projectId}/services/${service.id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to create service.'),
  })

  const createDatabaseMutation = useMutation({
    mutationFn: () => databasesApi.create(projectId!, {
      name: newDbName,
      db_type: newDbType,
      db_version: newDbType === 'sqlite' ? undefined : newDbVersion,
      db_name: newDbDatabaseName || undefined,
      db_user: newDbType === 'sqlite' ? undefined : (newDbUser || undefined),
      db_password: newDbType === 'sqlite' ? undefined : (newDbPassword || undefined),
      network_public: newDbType === 'sqlite' ? false : newDbAccess === 'public',
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['databases', projectId] })
      setNewDatabaseOpen(false)
      resetNewDatabaseForm()
      toast.success('Database created. It will become ready as soon as the runtime finishes provisioning it.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to create database.'),
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
  const hasDatabases = (databases?.length ?? 0) > 0
  const hasProjectResources = hasServices || hasDatabases

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
            <div className="flex items-center gap-2">
              <h1 className="text-2xl font-bold tracking-tight">{project?.name}</h1>
              {project?.my_role && (
                <Badge variant={project.my_role === 'owner' ? 'default' : 'secondary'} className="text-xs capitalize">
                  {project.my_role}
                </Badge>
              )}
            </div>
            {project?.description && (
              <p className="mt-1 text-sm text-muted-foreground">{project.description}</p>
            )}
            <p className="mt-1 text-xs text-muted-foreground">
              {services?.length ?? 0} service{(services?.length ?? 0) !== 1 ? 's' : ''}
              {canEditProject && (
                <>
                  {' '}
                  • {databases?.length ?? 0} database{(databases?.length ?? 0) !== 1 ? 's' : ''}
                </>
              )}
            </p>
          </div>
          <div className="flex gap-2">
            {isOwner && (
              <Button
                size="sm"
                variant="outline"
                className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10"
                onClick={() => setConfirmDeleteProject(true)}
              >
                <Trash2 className="h-3.5 w-3.5" /> Delete project
              </Button>
            )}
            {canEditProject && (
              <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setNewDatabaseOpen(true)}>
                <Database className="h-4 w-4" /> New database
              </Button>
            )}
            {canEditProject && (
              <Button size="sm" className="gap-1.5" onClick={() => setNewServiceOpen(true)}>
                <Plus className="h-4 w-4" /> New service
              </Button>
            )}
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
            <ServiceCard key={s.id} service={s} projectId={projectId!} canEdit={canEditProject} />
          ))}
        </div>
      )}

      {canEditProject && (
        <div className="mt-6">
          <Separator className="mb-6" />
          <div className="mb-4 flex items-center justify-between gap-3">
            <div>
              <h2 className="text-base font-semibold flex items-center gap-2">
                <Database className="h-4 w-4 text-muted-foreground" />
                Managed databases
                {databases && <Badge variant="secondary" className="ml-1">{databases.length}</Badge>}
              </h2>
              <p className="mt-1 text-xs text-muted-foreground">
                Postgres and MySQL run as reusable images. SQLite is mounted into services as a shared project volume.
              </p>
            </div>
            <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setNewDatabaseOpen(true)}>
              <Plus className="h-4 w-4" /> New database
            </Button>
          </div>

          {dbLoading ? (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {[...Array(2)].map((_, i) => (
                <Card key={i} className="h-44">
                  <CardHeader><Skeleton className="h-4 w-24" /></CardHeader>
                  <CardContent className="space-y-2">
                    <Skeleton className="h-8 w-full" />
                    <Skeleton className="h-12 w-full" />
                  </CardContent>
                </Card>
              ))}
            </div>
          ) : databases?.length === 0 ? (
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-16 text-center">
              <div className="mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-muted">
                <Database className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-base font-medium">No databases yet</p>
              <p className="mt-1 text-sm text-muted-foreground max-w-md">
                Add a managed Postgres, MySQL, or SQLite database and the connection URL will be injected into future service deployments automatically.
              </p>
              <Button size="sm" variant="outline" className="mt-5 gap-1.5" onClick={() => setNewDatabaseOpen(true)}>
                <Plus className="h-4 w-4" /> Create first database
              </Button>
            </div>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {(databases ?? []).map((database) => (
                <DatabaseCard key={database.id} database={database} projectId={projectId!} canEdit={canEditProject} />
              ))}
            </div>
          )}
        </div>
      )}

      {/* ── Project members (visible to owners only) ───────────────────── */}
      {isOwner && (
        <div className="mt-6">
          <Separator className="mb-6" />
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-base font-semibold flex items-center gap-2">
              <Users className="h-4 w-4 text-muted-foreground" />
              Project members
              {members && <Badge variant="secondary" className="ml-1">{members.length}</Badge>}
            </h2>
            <Button size="sm" variant="outline" className="gap-1.5 text-xs" onClick={() => setMembersOpen(true)}>
              <Plus className="h-3.5 w-3.5" /> Add member
            </Button>
          </div>

          <div className="rounded-lg border divide-y">
            {members?.map((m: ProjectMember) => (
              <div key={m.user_id} className="flex items-center justify-between gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium truncate">{m.name || m.email}</p>
                  <p className="text-xs text-muted-foreground truncate">{m.email}</p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <Select
                    value={m.role}
                    onValueChange={(role) =>
                      updateMemberMutation.mutate({ userId: m.user_id, role: role as 'owner' | 'editor' | 'viewer' })
                    }
                  >
                    <SelectTrigger className="h-7 w-24 text-xs">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="owner">Owner</SelectItem>
                      <SelectItem value="editor">Editor</SelectItem>
                      <SelectItem value="viewer">Viewer</SelectItem>
                    </SelectContent>
                  </Select>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    disabled={removeMemberMutation.isPending}
                    onClick={() => removeMemberMutation.mutate(m.user_id)}
                  >
                    <UserMinus className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
            ))}
            {members?.length === 0 && (
              <div className="px-4 py-6 text-center text-sm text-muted-foreground">No members yet.</div>
            )}
          </div>
        </div>
      )}

      {/* ── Add member dialog ──────────────────────────────────────────── */}
      <Dialog open={membersOpen} onOpenChange={(o) => {
        setMembersOpen(o)
        if (!o) {
          setAddMemberEmail('')
          setAddMemberEmailDisplay('')
          setAddMemberSearchQ('')
          setAddMemberDropdownOpen(false)
        }
      }}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Users className="h-4 w-4 text-primary" /> Add project member
            </DialogTitle>
            <DialogDescription>
              Search for a registered user and choose their role.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div>
              <Label htmlFor="member-email">User</Label>
              <div className="relative mt-1.5" ref={searchRef}>
                <Input
                  id="member-email"
                  type="text"
                  placeholder="Search by name or email…"
                  autoComplete="off"
                  value={addMemberEmailDisplay || addMemberSearchQ}
                  onChange={e => {
                    const v = e.target.value
                    setAddMemberEmailDisplay('')
                    setAddMemberEmail('')
                    setAddMemberSearchQ(v)
                    setAddMemberDropdownOpen(true)
                  }}
                  onFocus={() => {
                    if (!addMemberEmail) setAddMemberDropdownOpen(true)
                  }}
                  onBlur={(e) => {
                    // delay so click on list item fires first
                    if (!searchRef.current?.contains(e.relatedTarget as Node)) {
                      setTimeout(() => setAddMemberDropdownOpen(false), 150)
                    }
                  }}
                />
                {addMemberDropdownOpen && userSearchResults && userSearchResults.length > 0 && (
                  <div className="absolute z-50 mt-1 w-full rounded-md border border-border/60 bg-popover shadow-lg">
                    <ul className="max-h-44 overflow-y-auto py-1 text-sm">
                      {userSearchResults.map(u => (
                        <li
                          key={u.id}
                          tabIndex={0}
                          className="flex flex-col px-3 py-2 cursor-pointer hover:bg-accent focus:bg-accent outline-none"
                          onMouseDown={(e) => {
                            // prevent blur firing before click
                            e.preventDefault()
                            setAddMemberEmail(u.email)
                            setAddMemberEmailDisplay(u.name ? `${u.name} (${u.email})` : u.email)
                            setAddMemberSearchQ('')
                            setAddMemberDropdownOpen(false)
                          }}
                        >
                          <span className="font-medium truncate">{u.name || u.email}</span>
                          {u.name && <span className="text-xs text-muted-foreground truncate">{u.email}</span>}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {addMemberDropdownOpen && addMemberSearchQ.length >= 1 && (!userSearchResults || userSearchResults.length === 0) && (
                  <div className="absolute z-50 mt-1 w-full rounded-md border border-border/60 bg-popover shadow-sm">
                    <p className="px-3 py-2 text-xs text-muted-foreground">No users found</p>
                  </div>
                )}
              </div>
            </div>
            <div>
              <Label htmlFor="member-role">Role</Label>
              <Select value={addMemberRole} onValueChange={(v) => setAddMemberRole(v as 'owner' | 'editor' | 'viewer')}>
                <SelectTrigger id="member-role" className="mt-1.5">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="owner">Owner — full control</SelectItem>
                  <SelectItem value="editor">Editor — deploy &amp; configure</SelectItem>
                  <SelectItem value="viewer">Viewer — read-only</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => {
              setMembersOpen(false)
              setAddMemberEmail('')
              setAddMemberEmailDisplay('')
              setAddMemberSearchQ('')
              setAddMemberDropdownOpen(false)
            }}>Cancel</Button>
            <Button
              size="sm"
              disabled={!addMemberEmail || addMemberMutation.isPending}
              onClick={() => addMemberMutation.mutate()}
            >
              {addMemberMutation.isPending
                ? <><Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />Adding…</>
                : 'Add member'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── New service dialog ─────────────────────────────────────────── */}
      <Dialog open={newServiceOpen} onOpenChange={(o) => { setNewServiceOpen(o); if (!o) setNewSvcName('') }}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Rocket className="h-4 w-4 text-primary" /> New service
            </DialogTitle>
            <DialogDescription>
              Give your service a name. You can connect a repo and configure settings after creation.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <Label htmlFor="svc-name">Service name</Label>
            <Input
              id="svc-name"
              placeholder="e.g. web, api, worker"
              autoFocus
              value={newSvcName}
              onChange={e => setNewSvcName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
              onKeyDown={e => e.key === 'Enter' && newSvcName.length >= 2 && createSvcMutation.mutate()}
            />
            <p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens only.</p>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setNewServiceOpen(false)}>Cancel</Button>
            <Button
              size="sm"
              disabled={newSvcName.length < 2 || createSvcMutation.isPending}
              onClick={() => createSvcMutation.mutate()}
            >
              {createSvcMutation.isPending ? 'Creating…' : 'Create service'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={newDatabaseOpen} onOpenChange={(open) => {
        setNewDatabaseOpen(open)
        if (!open) {
          resetNewDatabaseForm()
        }
      }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Database className="h-4 w-4 text-primary" /> New database
            </DialogTitle>
            <DialogDescription>
              Create a managed Postgres, MySQL, or SQLite database for this project.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div>
              <Label htmlFor="db-name">Database resource name</Label>
              <Input
                id="db-name"
                className="mt-1.5"
                placeholder="e.g. app-db"
                autoFocus
                value={newDbName}
                onChange={(e) => setNewDbName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
              />
            </div>

            <div>
              <Label htmlFor="db-type">Engine</Label>
              <Select value={newDbType} onValueChange={(value) => {
                const nextType = value as DatabaseType
                setNewDbType(nextType)
                setNewDbVersion(defaultDatabaseVersion(nextType))
                if (nextType === 'sqlite') {
                  setNewDbAccess('private')
                  setNewDbUser('')
                  setNewDbPassword('')
                }
              }}>
                <SelectTrigger id="db-type" className="mt-1.5">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="postgres">Postgres</SelectItem>
                  <SelectItem value="mysql">MySQL</SelectItem>
                  <SelectItem value="sqlite">SQLite</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {newDbType !== 'sqlite' && (
              <div>
                <Label htmlFor="db-version">Image tag</Label>
                <Input
                  id="db-version"
                  className="mt-1.5"
                  placeholder={defaultDatabaseVersion(newDbType)}
                  value={newDbVersion}
                  onChange={(e) => setNewDbVersion(e.target.value)}
                />
              </div>
            )}

            <div>
              <Label htmlFor="db-dbname">Database name</Label>
              <Input
                id="db-dbname"
                className="mt-1.5"
                placeholder="Defaults to the resource name"
                value={newDbDatabaseName}
                onChange={(e) => setNewDbDatabaseName(e.target.value)}
              />
            </div>

            {newDbType !== 'sqlite' && (
              <>
                <div>
                  <Label htmlFor="db-user">Database user</Label>
                  <Input
                    id="db-user"
                    className="mt-1.5"
                    placeholder="Defaults to the resource name"
                    value={newDbUser}
                    onChange={(e) => setNewDbUser(e.target.value)}
                  />
                </div>

                <div>
                  <Label htmlFor="db-password">Database password</Label>
                  <Input
                    id="db-password"
                    type="password"
                    className="mt-1.5"
                    placeholder="Auto-generated if left blank"
                    value={newDbPassword}
                    onChange={(e) => setNewDbPassword(e.target.value)}
                  />
                </div>

                <div>
                  <Label htmlFor="db-access">Exposure</Label>
                  <Select value={newDbAccess} onValueChange={(value) => setNewDbAccess(value as 'private' | 'public')}>
                    <SelectTrigger id="db-access" className="mt-1.5">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="private">Private to project network</SelectItem>
                      <SelectItem value="public">Expose on a host port</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </>
            )}

            {newDbType === 'sqlite' && (
              <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
                SQLite is created as a persistent project volume. Services will see its path after their next deployment.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setNewDatabaseOpen(false)}>Cancel</Button>
            <Button
              size="sm"
              disabled={newDbName.length < 2 || createDatabaseMutation.isPending}
              onClick={() => createDatabaseMutation.mutate()}
            >
              {createDatabaseMutation.isPending ? 'Creating…' : 'Create database'}
            </Button>
          </DialogFooter>
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
              {hasProjectResources ? (
                <span className="text-destructive font-medium">
                  This project still has {services?.length ?? 0} service{(services?.length ?? 0) !== 1 ? 's' : ''}
                  {' '}and {databases?.length ?? 0} database{(databases?.length ?? 0) !== 1 ? 's' : ''}.
                  Delete them first so their containers and data volumes can be cleaned up safely.
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
              disabled={hasProjectResources || deleteProjectMutation.isPending}
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
