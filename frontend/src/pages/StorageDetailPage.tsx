import { useState, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  HardDrive,
  Key,
  Trash2,
  Plus,
  RefreshCw,
  Eye,
  EyeOff,
  Copy,
  Check,
  ChevronLeft,
  ChevronRight,
  Loader2,
  Database,
  Lock,
  Unlink,
  AlertTriangle,
  FolderOpen,
  FileIcon,
  BarChart2,
  Upload,
  Home,
  Shield,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
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
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'
import { toast } from 'sonner'
import { storageApi, type StorageAccess, type ObjectEntry, type BandwidthEntry } from '@/api/storage'
import { projectsApi } from '@/api/projects'
import { servicesApi } from '@/api/services'
import type { Service } from '@/api/services'
import { formatDate } from '@/lib/dateFormat'
import { useTimezone } from '@/context/TimezoneContext'
import { cn } from '@/lib/utils'

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function CopyButton({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <button
      onClick={copy}
      className={cn('text-muted-foreground hover:text-foreground transition-colors', className)}
      title="Copy"
    >
      {copied ? <Check className="h-3.5 w-3.5 text-green-500" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  )
}

function SecretKeyReveal({ value, className }: { value: string; className?: string }) {
  const [shown, setShown] = useState(false)
  return (
    <div className={cn('flex items-center gap-2 font-mono text-sm bg-muted rounded-lg px-3 py-2.5', className)}>
      <span className="flex-1 break-all select-all">
        {shown ? value : 'x'.repeat(Math.min(value.length, 48))}
      </span>
      <button
        onClick={() => setShown((s) => !s)}
        className="text-muted-foreground hover:text-foreground transition-colors shrink-0"
        title={shown ? 'Hide' : 'Reveal'}
      >
        {shown ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
      <CopyButton text={value} />
    </div>
  )
}

function FileBrowser({ storageId }: { storageId: number }) {
  const qc = useQueryClient()
  const [prefix, setPrefix] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['storage-browse', storageId, prefix],
    queryFn: () => storageApi.browse(storageId, prefix || undefined),
  })
  const objects = Array.isArray(data) ? data : []

  const deleteObjectMutation = useMutation({
    mutationFn: (path: string) => storageApi.adminDeleteObject(storageId, path),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storage-browse', storageId] })
      qc.invalidateQueries({ queryKey: ['storage', storageId] })
      toast.success('Object deleted.')
    },
    onError: () => toast.error('Failed to delete object.'),
  })

  const { folders, files } = (() => {
    const folderSet = new Set<string>()
    const fileList: ObjectEntry[] = []
    for (const obj of objects) {
      const rel = obj.path.startsWith(prefix) ? obj.path.slice(prefix.length) : obj.path
      const slashIdx = rel.indexOf('/')
      if (slashIdx !== -1) {
        folderSet.add(prefix + rel.slice(0, slashIdx + 1))
      } else {
        fileList.push(obj)
      }
    }
    return { folders: Array.from(folderSet), files: fileList }
  })()

  const breadcrumbs = prefix
    ? prefix.replace(/\/$/, '').split('/').reduce<{ label: string; path: string }[]>((acc, part, i, arr) => {
        acc.push({ label: part, path: arr.slice(0, i + 1).join('/') + '/' })
        return acc
      }, [])
    : []

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <div className="flex items-center gap-1 text-sm min-w-0 flex-1">
          <button
            onClick={() => setPrefix('')}
            className={cn(
              'flex items-center gap-1 text-muted-foreground hover:text-foreground transition-colors',
              !prefix && 'text-foreground font-medium',
            )}
          >
            <Home className="h-3.5 w-3.5" />
          </button>
          {breadcrumbs.map((bc, i) => (
            <div key={bc.path} className="flex items-center gap-1">
              <ChevronRight className="h-3 w-3 text-muted-foreground/50" />
              <button
                onClick={() => setPrefix(bc.path)}
                className={cn(
                  'text-muted-foreground hover:text-foreground transition-colors truncate max-w-[120px]',
                  i === breadcrumbs.length - 1 && 'text-foreground font-medium',
                )}
              >
                {bc.label}
              </button>
            </div>
          ))}
        </div>
        <Button variant="outline" size="sm" className="gap-1.5" onClick={() => refetch()}>
          <RefreshCw className="h-3.5 w-3.5" /> Refresh
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => toast.info('Upload via PUT /api/storage/{id}/objects/{path} with X-Storage-Key from your service')}
        >
          <Upload className="h-3.5 w-3.5" /> Upload
        </Button>
        <input ref={fileInputRef} type="file" className="hidden" />
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-10 rounded-lg" />)}
        </div>
      ) : folders.length === 0 && files.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground border border-dashed rounded-xl">
          <FolderOpen className="h-10 w-10 mb-2 opacity-30" />
          <p className="text-sm font-medium">No objects here</p>
          <p className="text-xs mt-1">Upload files from your service using the storage API.</p>
        </div>
      ) : (
        <div className="rounded-xl border overflow-hidden divide-y">
          {prefix && (
            <button
              onClick={() => {
                const parts = prefix.replace(/\/$/, '').split('/')
                parts.pop()
                setPrefix(parts.length ? parts.join('/') + '/' : '')
              }}
              className="flex items-center gap-3 w-full px-4 py-3 text-sm text-muted-foreground hover:bg-muted/40 transition-colors"
            >
              <ChevronLeft className="h-4 w-4" />
              ..
            </button>
          )}
          {folders.map((folder) => (
            <button
              key={folder}
              onClick={() => setPrefix(folder)}
              className="flex items-center justify-between gap-3 w-full px-4 py-3 hover:bg-muted/40 transition-colors"
            >
              <div className="flex items-center gap-3 min-w-0">
                <FolderOpen className="h-4 w-4 shrink-0 text-amber-500" />
                <span className="text-sm font-medium truncate">
                  {folder.replace(prefix, '').replace(/\/$/, '')}
                </span>
              </div>
              <ChevronRight className="h-4 w-4 text-muted-foreground/50 shrink-0" />
            </button>
          ))}
          {files.map((obj) => (
            <div key={obj.path} className="flex items-center justify-between gap-3 px-4 py-3 hover:bg-muted/40 transition-colors">
              <div className="flex items-center gap-3 min-w-0">
                <FileIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0">
                  <p className="text-sm font-medium truncate">{obj.path.replace(prefix, '')}</p>
                  <p className="text-xs text-muted-foreground">{formatBytes(obj.size)}</p>
                </div>
              </div>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground hover:text-destructive shrink-0"
                title="Delete"
                onClick={() => deleteObjectMutation.mutate(obj.path)}
                disabled={deleteObjectMutation.isPending}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function ServicesTab({ storageId }: { storageId: number }) {
  const qc = useQueryClient()
  const { timezone } = useTimezone()

  const { data: access = [], isLoading } = useQuery({
    queryKey: ['storage-access', storageId],
    queryFn: () => storageApi.listAccess(storageId),
  })

  const [addOpen, setAddOpen] = useState(false)
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  const [selectedServiceId, setSelectedServiceId] = useState<string | null>(null)
  const [canRead, setCanRead] = useState(true)
  const [canWrite, setCanWrite] = useState(true)
  const [newKey, setNewKey] = useState<string | null>(null)
  const [keyDialogOpen, setKeyDialogOpen] = useState(false)

  const { data: projects = [] } = useQuery({
    queryKey: ['projects'],
    queryFn: projectsApi.list,
    enabled: addOpen,
  })

  const { data: projectServices = [] } = useQuery({
    queryKey: ['project-services', selectedProjectId],
    queryFn: () => selectedProjectId ? servicesApi.list(selectedProjectId) : Promise.resolve([] as Service[]),
    enabled: !!selectedProjectId,
  })

  const alreadyGranted = new Set(access.map((a: StorageAccess) => a.service_id))

  const grantMutation = useMutation({
    mutationFn: () => storageApi.grantAccess(storageId, {
      service_id: Number(selectedServiceId),
      can_read: canRead,
      can_write: canWrite,
    }),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['storage-access', storageId] })
      qc.invalidateQueries({ queryKey: ['storages'] })
      setAddOpen(false)
      setSelectedProjectId(null)
      setSelectedServiceId(null)
      setNewKey(data.service_key)
      setKeyDialogOpen(true)
    },
    onError: (err: any) => toast.error(err?.response?.data?.error ?? 'Failed to grant access.'),
  })

  const revokeMutation = useMutation({
    mutationFn: (serviceId: number) => storageApi.revokeAccess(storageId, serviceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storage-access', storageId] })
      qc.invalidateQueries({ queryKey: ['storages'] })
      toast.success('Access revoked.')
    },
    onError: () => toast.error('Failed to revoke access.'),
  })

  const [rotateTarget, setRotateTarget] = useState<StorageAccess | null>(null)

  const rotateMutation = useMutation({
    mutationFn: (serviceId: number) => storageApi.rotateServiceKey(storageId, serviceId),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['storage-access', storageId] })
      setRotateTarget(null)
      setNewKey(data.service_key)
      setKeyDialogOpen(true)
    },
    onError: () => toast.error('Failed to rotate key.'),
  })

  const updateMutation = useMutation({
    mutationFn: ({ serviceId, canRead, canWrite }: { serviceId: number; canRead: boolean; canWrite: boolean }) =>
      storageApi.updateAccess(storageId, serviceId, { can_read: canRead, can_write: canWrite }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage-access', storageId] }),
    onError: () => toast.error('Failed to update permissions.'),
  })

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          Each service gets its own encrypted API key, auto-injected as{' '}
          <code className="bg-muted px-1 rounded text-xs">STORAGE_{'{NAME}'}_KEY</code> at deploy time.
        </p>
        <Button size="sm" className="gap-1.5 shrink-0" onClick={() => setAddOpen(true)}>
          <Plus className="h-4 w-4" /> Add Service
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(2)].map((_, i) => <Skeleton key={i} className="h-16 rounded-xl" />)}
        </div>
      ) : access.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 border border-dashed rounded-xl text-muted-foreground">
          <Lock className="h-10 w-10 mb-2 opacity-30" />
          <p className="text-sm font-medium">No services have access</p>
          <p className="text-xs mt-1">Grant a service access to generate its API key.</p>
        </div>
      ) : (
        <div className="rounded-xl border overflow-hidden divide-y">
          {access.map((a: StorageAccess) => (
            <div key={a.id} className="px-4 py-3 hover:bg-muted/20 transition-colors">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Database className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="font-medium text-sm">{a.service_name}</span>
                    <Badge variant="outline" className="text-xs py-0 h-5">
                      {a.can_read && a.can_write ? 'Read + Write' : a.can_read ? 'Read only' : 'Write only'}
                    </Badge>
                  </div>
                  <div className="flex items-center gap-2 mt-1.5">
                    <span className="font-mono text-xs text-muted-foreground bg-muted px-2 py-0.5 rounded">
                      {a.service_key_preview}....
                    </span>
                    <span className="text-xs text-muted-foreground/60">
                      Granted {formatDate(a.granted_at, timezone)}
                    </span>
                  </div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Button
                    variant="ghost"
                    size="sm"
                    className={cn('h-7 text-xs gap-1', a.can_read ? 'text-primary' : 'text-muted-foreground')}
                    title="Toggle read access"
                    onClick={() => updateMutation.mutate({ serviceId: a.service_id, canRead: !a.can_read, canWrite: a.can_write })}
                    disabled={updateMutation.isPending}
                  >
                    <Shield className="h-3 w-3" /> R
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className={cn('h-7 text-xs gap-1', a.can_write ? 'text-primary' : 'text-muted-foreground')}
                    title="Toggle write access"
                    onClick={() => updateMutation.mutate({ serviceId: a.service_id, canRead: a.can_read, canWrite: !a.can_write })}
                    disabled={updateMutation.isPending}
                  >
                    <Shield className="h-3 w-3" /> W
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-amber-500"
                    title="Rotate API key"
                    onClick={() => setRotateTarget(a)}
                  >
                    <RefreshCw className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    title="Revoke access"
                    onClick={() => revokeMutation.mutate(a.service_id)}
                    disabled={revokeMutation.isPending}
                  >
                    <Unlink className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Grant Service Access</DialogTitle>
            <DialogDescription>
              A unique encrypted API key will be generated for this service. The plaintext key is shown only once.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Project</Label>
              <Select value={selectedProjectId} onValueChange={(v) => { setSelectedProjectId(v); setSelectedServiceId(null) }}>
                <SelectTrigger><SelectValue placeholder="Select project..." /></SelectTrigger>
                <SelectContent>
                  {projects.map((p) => <SelectItem key={p.id} value={String(p.id)}>{p.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
            {selectedProjectId && (
              <div className="space-y-1.5">
                <Label>Service</Label>
                <Select value={selectedServiceId} onValueChange={setSelectedServiceId}>
                  <SelectTrigger><SelectValue placeholder="Select service..." /></SelectTrigger>
                  <SelectContent>
                    {projectServices
                      .filter((s) => !alreadyGranted.has(s.id))
                      .map((s) => <SelectItem key={s.id} value={String(s.id)}>{s.name}</SelectItem>)}
                    {projectServices.filter((s) => !alreadyGranted.has(s.id)).length === 0 && (
                      <SelectItem value="__none" disabled>No eligible services</SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </div>
            )}
            <div className="flex gap-4">
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input type="checkbox" checked={canRead} onChange={(e) => setCanRead(e.target.checked)} className="rounded" />
                Read
              </label>
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input type="checkbox" checked={canWrite} onChange={(e) => setCanWrite(e.target.checked)} className="rounded" />
                Write
              </label>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)}>Cancel</Button>
            <Button
              onClick={() => grantMutation.mutate()}
              disabled={!selectedServiceId || selectedServiceId === '__none' || grantMutation.isPending}
              className="gap-1.5"
            >
              {grantMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Grant Access
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!rotateTarget} onOpenChange={() => setRotateTarget(null)}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <RefreshCw className="h-5 w-5 text-amber-500" /> Rotate Key
            </DialogTitle>
            <DialogDescription>
              The current key for <strong>{rotateTarget?.service_name}</strong> will be invalidated immediately.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRotateTarget(null)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => rotateTarget && rotateMutation.mutate(rotateTarget.service_id)}
              disabled={rotateMutation.isPending}
              className="gap-1.5"
            >
              {rotateMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Rotate
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={keyDialogOpen} onOpenChange={setKeyDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Key className="h-5 w-5 text-amber-500" /> Save API Key
            </DialogTitle>
            <DialogDescription>
              This is the only time the plaintext key is shown. It will be auto-injected as{' '}
              <code className="bg-muted px-1 rounded text-xs font-mono">STORAGE_{'{NAME}'}_KEY</code> at the next deploy.
            </DialogDescription>
          </DialogHeader>
          {newKey && <div className="py-2"><SecretKeyReveal value={newKey} /></div>}
          <DialogFooter>
            <Button onClick={() => { setKeyDialogOpen(false); setNewKey(null) }}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function StatsTab({ storageId }: { storageId: number }) {
  const { data: stats, isLoading } = useQuery({
    queryKey: ['storage-stats', storageId],
    queryFn: () => storageApi.stats(storageId),
  })

  if (isLoading) {
    return (
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Skeleton className="h-20 rounded-xl" />
          <Skeleton className="h-20 rounded-xl" />
        </div>
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }
  if (!stats) return null

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 gap-3">
        <div className="rounded-xl border bg-card p-4 shadow-sm">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Total Size</p>
          <p className="text-2xl font-semibold mt-1">{formatBytes(stats.size_bytes)}</p>
        </div>
        <div className="rounded-xl border bg-card p-4 shadow-sm">
          <p className="text-xs text-muted-foreground font-medium uppercase tracking-wide">Objects</p>
          <p className="text-2xl font-semibold mt-1">{stats.object_count.toLocaleString()}</p>
        </div>
      </div>
      <div className="rounded-xl border overflow-hidden">
        <div className="flex items-center gap-2 px-4 py-3 border-b bg-muted/30">
          <BarChart2 className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">Bandwidth by Service</h3>
        </div>
        {!stats.bandwidth?.length ? (
          <div className="flex flex-col items-center justify-center py-10 text-muted-foreground">
            <BarChart2 className="h-8 w-8 mb-2 opacity-30" />
            <p className="text-sm">No bandwidth data yet.</p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/20 text-xs text-muted-foreground">
                <th className="text-left px-4 py-2 font-medium">Service</th>
                <th className="text-left px-4 py-2 font-medium">Period</th>
                <th className="text-right px-4 py-2 font-medium">Downloaded</th>
                <th className="text-right px-4 py-2 font-medium">Uploaded</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {stats.bandwidth.map((b: BandwidthEntry, i: number) => (
                <tr key={i} className="hover:bg-muted/20 transition-colors">
                  <td className="px-4 py-2.5 font-medium">{b.service_name}</td>
                  <td className="px-4 py-2.5 text-muted-foreground">{b.period}</td>
                  <td className="px-4 py-2.5 text-right">{formatBytes(b.bytes_read)}</td>
                  <td className="px-4 py-2.5 text-right">{formatBytes(b.bytes_written)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

export function StorageDetailPage() {
  const { storageId } = useParams<{ storageId: string }>()
  const navigate = useNavigate()
  const { timezone } = useTimezone()
  const id = Number(storageId)

  const { data: storage, isLoading } = useQuery({
    queryKey: ['storage', id],
    queryFn: () => storageApi.get(id),
    enabled: !!id,
  })

  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteConfirmName, setDeleteConfirmName] = useState('')
  const deleteMutation = useMutation({
    mutationFn: () => storageApi.delete(id),
    onSuccess: () => { navigate('/storage'); toast.success('Storage deleted.') },
    onError: () => toast.error('Failed to delete storage.'),
  })

  if (isLoading) {
    return (
      <div className="space-y-4 pb-8">
        <Skeleton className="h-9 w-32" />
        <Skeleton className="h-24 rounded-xl" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  }

  if (!storage) {
    return (
      <div className="flex flex-col items-center justify-center py-24 text-center text-muted-foreground">
        <HardDrive className="h-12 w-12 mb-3 opacity-30" />
        <p className="font-medium">Storage not found</p>
        <Button variant="outline" className="mt-4" onClick={() => navigate('/storage')}>Back to Storage</Button>
      </div>
    )
  }

  return (
    <div className="space-y-5 pb-8">
      <Button variant="ghost" size="sm" className="gap-1.5 text-muted-foreground" onClick={() => navigate('/storage')}>
        <ChevronLeft className="h-3.5 w-3.5" /> Back to Storage
      </Button>

      <div className="rounded-xl border bg-card shadow-sm p-5 flex items-start justify-between gap-4 flex-wrap">
        <div className="flex items-center gap-3 min-w-0">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <HardDrive className="h-5 w-5" />
          </div>
          <div className="min-w-0">
            <h1 className="text-xl font-semibold truncate">{storage.name}</h1>
            {storage.description && (
              <p className="text-sm text-muted-foreground mt-0.5 line-clamp-1">{storage.description}</p>
            )}
            <p className="text-xs text-muted-foreground/60 mt-1">Created {formatDate(storage.created_at, timezone)}</p>
          </div>
        </div>
        <div className="flex items-center gap-4 text-sm text-muted-foreground shrink-0">
          <div className="text-right">
            <p className="font-semibold text-foreground">{formatBytes(storage.size_bytes)}</p>
            <p className="text-xs">disk used</p>
          </div>
          <div className="text-right">
            <p className="font-semibold text-foreground">{storage.access_count}</p>
            <p className="text-xs">{storage.access_count === 1 ? 'service' : 'services'}</p>
          </div>
        </div>
      </div>

      <Tabs defaultValue="files">
        <TabsList className="mb-4">
          <TabsTrigger value="files" className="gap-1.5">
            <FolderOpen className="h-3.5 w-3.5" /> Files
          </TabsTrigger>
          <TabsTrigger value="services" className="gap-1.5">
            <Lock className="h-3.5 w-3.5" /> Services
          </TabsTrigger>
          <TabsTrigger value="stats" className="gap-1.5">
            <BarChart2 className="h-3.5 w-3.5" /> Stats
          </TabsTrigger>
        </TabsList>
        <TabsContent value="files"><FileBrowser storageId={id} /></TabsContent>
        <TabsContent value="services"><ServicesTab storageId={id} /></TabsContent>
        <TabsContent value="stats"><StatsTab storageId={id} /></TabsContent>
      </Tabs>

      <div className="rounded-xl border border-destructive/30 bg-destructive/5 p-5 space-y-3">
        <h3 className="font-semibold text-destructive flex items-center gap-2">
          <AlertTriangle className="h-4 w-4" /> Danger Zone
        </h3>
        <p className="text-sm text-muted-foreground">
          Permanently deletes all files, service keys, and bandwidth records. The disk directory will be removed. This cannot be undone.
        </p>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5 text-destructive border-destructive/30 hover:bg-destructive/10"
          onClick={() => { setDeleteConfirmName(''); setDeleteOpen(true) }}
        >
          <Trash2 className="h-3.5 w-3.5" /> Delete Storage
        </Button>
      </div>

      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-destructive">
              <AlertTriangle className="h-5 w-5" /> Delete Storage
            </DialogTitle>
            <DialogDescription>
              This will permanently delete <strong>{storage.name}</strong> and all files on disk. Type the storage name to confirm.
            </DialogDescription>
          </DialogHeader>
          <Input placeholder={storage.name} value={deleteConfirmName} onChange={(e) => setDeleteConfirmName(e.target.value)} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => deleteMutation.mutate()}
              disabled={deleteConfirmName !== storage.name || deleteMutation.isPending}
              className="gap-1.5"
            >
              {deleteMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
