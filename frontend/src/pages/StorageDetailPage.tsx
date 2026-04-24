import { useState } from 'react'
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
  Loader2,
  Database,
  Lock,
  Unlink,
  ShieldCheck,
  AlertTriangle,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
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
import { toast } from 'sonner'
import { storageApi, type StorageKVItem, type StorageAccess } from '@/api/storage'
import { projectsApi } from '@/api/projects'
import { servicesApi } from '@/api/services'
import type { Service } from '@/api/services'
import { formatDate } from '@/lib/dateFormat'
import { useTimezone } from '@/context/TimezoneContext'
import { cn } from '@/lib/utils'

// ── Helpers ──────────────────────────────────────────────────────────────────

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

function SectionCard({ title, icon: Icon, children, className }: {
  title: string
  icon: React.ElementType
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn('rounded-xl border bg-card shadow-sm overflow-hidden', className)}>
      <div className="flex items-center gap-2 px-5 py-3.5 border-b bg-muted/30">
        <Icon className="h-4 w-4 text-muted-foreground" />
        <h2 className="font-semibold text-sm">{title}</h2>
      </div>
      <div className="p-5">{children}</div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function StorageDetailPage() {
  const { storageId } = useParams<{ storageId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { timezone } = useTimezone()
  const id = Number(storageId)

  const { data: storage, isLoading } = useQuery({
    queryKey: ['storage', id],
    queryFn: () => storageApi.get(id),
    enabled: !!id,
  })

  const { data: access = [] } = useQuery({
    queryKey: ['storage-access', id],
    queryFn: () => storageApi.listAccess(id),
    enabled: !!id,
  })

  const { data: kvItems = [] } = useQuery({
    queryKey: ['storage-kv', id],
    queryFn: () => storageApi.listKeys(id),
    enabled: !!id,
  })

  // ── Rotate key ─────────────────────────────────────────────────────────────
  const [rotateConfirmOpen, setRotateConfirmOpen] = useState(false)
  const [newKey, setNewKey] = useState<string | null>(null)
  const [newKeyDialogOpen, setNewKeyDialogOpen] = useState(false)
  const [keyShown, setKeyShown] = useState(false)

  const rotateMutation = useMutation({
    mutationFn: () => storageApi.rotateKey(id),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['storage', id] })
      setRotateConfirmOpen(false)
      setNewKey(data.api_key)
      setKeyShown(false)
      setNewKeyDialogOpen(true)
    },
    onError: () => toast.error('Failed to rotate key.'),
  })

  // ── Delete storage ─────────────────────────────────────────────────────────
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false)
  const [deleteConfirmName, setDeleteConfirmName] = useState('')

  const deleteMutation = useMutation({
    mutationFn: () => storageApi.delete(id),
    onSuccess: () => {
      navigate('/storage')
      toast.success('Storage deleted.')
    },
    onError: () => toast.error('Failed to delete storage.'),
  })

  // ── Add service access ─────────────────────────────────────────────────────
  const [addServiceOpen, setAddServiceOpen] = useState(false)
  const [selectedProjectId, setSelectedProjectId] = useState<string>('')
  const [selectedServiceId, setSelectedServiceId] = useState<string>('')

  const { data: projects = [] } = useQuery({
    queryKey: ['projects'],
    queryFn: projectsApi.list,
    enabled: addServiceOpen,
  })

  const { data: projectServices = [] } = useQuery({
    queryKey: ['project-services', selectedProjectId],
    queryFn: () => {
      if (!selectedProjectId) return Promise.resolve([] as Service[])
      return servicesApi.list(selectedProjectId)
    },
    enabled: !!selectedProjectId,
  })

  const grantAccessMutation = useMutation({
    mutationFn: (serviceId: number) => storageApi.grantAccess(id, { service_id: serviceId }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storage-access', id] })
      qc.invalidateQueries({ queryKey: ['storage', id] })
      setAddServiceOpen(false)
      setSelectedProjectId('')
      setSelectedServiceId('')
      toast.success('Service granted access.')
    },
    onError: () => toast.error('Failed to grant access.'),
  })

  const revokeAccessMutation = useMutation({
    mutationFn: (serviceId: number) => storageApi.revokeAccess(id, serviceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storage-access', id] })
      qc.invalidateQueries({ queryKey: ['storage', id] })
      toast.success('Access revoked.')
    },
    onError: () => toast.error('Failed to revoke access.'),
  })

  // ── Delete KV key ──────────────────────────────────────────────────────────
  const deleteKeyMutation = useMutation({
    mutationFn: (key: string) => storageApi.adminDeleteKey(id, key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storage-kv', id] })
      qc.invalidateQueries({ queryKey: ['storage', id] })
    },
    onError: () => toast.error('Failed to delete key.'),
  })

  // ── Render ─────────────────────────────────────────────────────────────────

  if (isLoading) {
    return (
      <div className="space-y-4 pb-8">
        <Skeleton className="h-9 w-32" />
        <Skeleton className="h-24 rounded-xl" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }

  if (!storage) {
    return (
      <div className="flex flex-col items-center justify-center py-24 text-center text-muted-foreground">
        <HardDrive className="h-12 w-12 mb-3 opacity-30" />
        <p className="font-medium">Storage not found</p>
        <Button variant="outline" className="mt-4" onClick={() => navigate('/storage')}>
          Back to Storage
        </Button>
      </div>
    )
  }

  const alreadyGranted = new Set(access.map((a: StorageAccess) => a.service_id))

  return (
    <div className="space-y-5 pb-8">
      {/* Back */}
      <Button
        variant="ghost"
        size="sm"
        className="gap-1.5 text-muted-foreground"
        onClick={() => navigate('/storage')}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> Back to Storage
      </Button>

      {/* Header card */}
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
            <p className="text-xs text-muted-foreground/60 mt-1">
              Created {formatDate(storage.created_at, timezone)}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2 text-sm text-muted-foreground shrink-0">
          <Key className="h-4 w-4" />
          <span>{storage.key_count} {storage.key_count === 1 ? 'key' : 'keys'}</span>
          <span className="text-muted-foreground/30 mx-1">|</span>
          <Database className="h-4 w-4" />
          <span>{storage.access_count} {storage.access_count === 1 ? 'service' : 'services'}</span>
        </div>
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        {/* API Key section */}
        <SectionCard title="API Key" icon={Key}>
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Services use this key via the <code className="bg-muted px-1 rounded text-xs">X-Storage-Key</code> header.
              The full key is only visible right after creation or rotation.
            </p>
            <div className="flex items-center gap-2 font-mono text-sm bg-muted rounded-lg px-3 py-2.5">
              <span className="text-muted-foreground flex-1">
                {storage.api_key_preview}
                <span className="opacity-50">••••••••••••••••••••</span>
              </span>
              <CopyButton text={storage.api_key_preview} />
            </div>
            <p className="text-xs text-muted-foreground/70">
              Only the first 12 characters are shown. Store the full key as a service env var.
            </p>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-amber-600 dark:text-amber-400 border-amber-200 dark:border-amber-800 hover:bg-amber-50 dark:hover:bg-amber-900/20"
              onClick={() => setRotateConfirmOpen(true)}
            >
              <RefreshCw className="h-3.5 w-3.5" /> Rotate Key
            </Button>
          </div>
        </SectionCard>

        {/* Usage info */}
        <SectionCard title="How to use" icon={ShieldCheck}>
          <div className="space-y-3 text-sm">
            <p className="text-muted-foreground">
              Make HTTP requests to the platform API from within your service containers.
            </p>
            <div className="space-y-1">
              <p className="font-medium text-xs uppercase tracking-wide text-muted-foreground/70">Read a value</p>
              <pre className="bg-muted rounded-lg px-3 py-2 text-xs overflow-x-auto">
{`GET /api/storage/${id}/kv/your-key
X-Storage-Key: <your-api-key>`}
              </pre>
            </div>
            <div className="space-y-1">
              <p className="font-medium text-xs uppercase tracking-wide text-muted-foreground/70">Write a value</p>
              <pre className="bg-muted rounded-lg px-3 py-2 text-xs overflow-x-auto">
{`PUT /api/storage/${id}/kv/your-key
X-Storage-Key: <your-api-key>
Content-Type: text/plain

<value here>`}
              </pre>
            </div>
            <p className="text-xs text-muted-foreground/70">
              The host is your platform's internal address. Add <code className="bg-muted px-1 rounded">PLATFORM_URL</code> as an env var pointing to it.
            </p>
          </div>
        </SectionCard>
      </div>

      {/* Service access */}
      <SectionCard title="Service Access" icon={Lock}>
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Only services listed here can access this storage using the API key.
          </p>

          {access.length === 0 ? (
            <div className="text-center py-8 text-muted-foreground">
              <Lock className="h-8 w-8 mx-auto mb-2 opacity-30" />
              <p className="text-sm">No services have access yet.</p>
            </div>
          ) : (
            <div className="divide-y rounded-lg border overflow-hidden">
              {access.map((a: StorageAccess) => (
                <div key={a.id} className="flex items-center justify-between px-4 py-3 bg-card hover:bg-muted/40 transition-colors">
                  <div className="flex items-center gap-2 min-w-0">
                    <Database className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="font-medium text-sm truncate">{a.service_name}</span>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-xs text-muted-foreground">
                      {a.can_read && a.can_write ? 'Read & Write' : a.can_read ? 'Read only' : 'Write only'}
                    </span>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      title="Revoke access"
                      onClick={() => revokeAccessMutation.mutate(a.service_id)}
                      disabled={revokeAccessMutation.isPending}
                    >
                      <Unlink className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}

          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setAddServiceOpen(true)}
          >
            <Plus className="h-4 w-4" /> Add Service
          </Button>
        </div>
      </SectionCard>

      {/* KV keys */}
      <SectionCard title="Stored Keys" icon={Database}>
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Keys stored in this storage. Values are encrypted at rest and only accessible
            via the API key.
          </p>
          {kvItems.length === 0 ? (
            <div className="text-center py-8 text-muted-foreground">
              <Key className="h-8 w-8 mx-auto mb-2 opacity-30" />
              <p className="text-sm">No keys stored yet.</p>
            </div>
          ) : (
            <div className="divide-y rounded-lg border overflow-hidden">
              {kvItems.map((item: StorageKVItem) => (
                <div key={item.key} className="flex items-center justify-between px-4 py-3 bg-card hover:bg-muted/40 transition-colors">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <code className="text-sm font-mono font-medium truncate">{item.key}</code>
                      <span className="text-xs bg-muted px-1.5 py-0.5 rounded text-muted-foreground shrink-0">
                        {item.content_type}
                      </span>
                    </div>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      {(item.size_bytes / 1024).toFixed(1)} KB · Updated {formatDate(item.updated_at, timezone)}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive shrink-0"
                    title="Delete key"
                    onClick={() => deleteKeyMutation.mutate(item.key)}
                    disabled={deleteKeyMutation.isPending}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      </SectionCard>

      {/* Danger zone */}
      <div className="rounded-xl border border-destructive/30 bg-destructive/5 p-5 space-y-3">
        <h3 className="font-semibold text-destructive flex items-center gap-2">
          <AlertTriangle className="h-4 w-4" /> Danger Zone
        </h3>
        <p className="text-sm text-muted-foreground">
          Deleting this storage permanently removes all stored keys, values, and service access records.
          This action cannot be undone.
        </p>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5 text-destructive border-destructive/30 hover:bg-destructive/10"
          onClick={() => { setDeleteConfirmName(''); setDeleteConfirmOpen(true) }}
        >
          <Trash2 className="h-3.5 w-3.5" /> Delete Storage
        </Button>
      </div>

      {/* Rotate key confirm */}
      <Dialog open={rotateConfirmOpen} onOpenChange={setRotateConfirmOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <RefreshCw className="h-5 w-5 text-amber-500" /> Rotate API Key
            </DialogTitle>
            <DialogDescription>
              The current key will stop working immediately. Any services using it will need to
              be updated with the new key.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRotateConfirmOpen(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => rotateMutation.mutate()}
              disabled={rotateMutation.isPending}
              className="gap-1.5"
            >
              {rotateMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Rotate
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* New key display after rotation */}
      <Dialog open={newKeyDialogOpen} onOpenChange={setNewKeyDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Key className="h-5 w-5 text-amber-500" /> New API Key
            </DialogTitle>
            <DialogDescription>
              Copy and store this key now — it will not be shown again.
            </DialogDescription>
          </DialogHeader>
          {newKey && (
            <div className="space-y-3 py-2">
              <div className="flex items-center gap-2 font-mono text-sm bg-muted rounded-lg px-3 py-2.5">
                <span className="flex-1 break-all select-all">
                  {keyShown ? newKey : '•'.repeat(Math.min(newKey.length, 40))}
                </span>
                <button
                  onClick={() => setKeyShown((s) => !s)}
                  className="text-muted-foreground hover:text-foreground transition-colors shrink-0"
                  title={keyShown ? 'Hide' : 'Reveal'}
                >
                  {keyShown ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                </button>
                <CopyButton text={newKey} />
              </div>
            </div>
          )}
          <DialogFooter>
            <Button onClick={() => { setNewKeyDialogOpen(false); setNewKey(null) }}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Add service access dialog */}
      <Dialog open={addServiceOpen} onOpenChange={setAddServiceOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Grant Service Access</DialogTitle>
            <DialogDescription>
              Choose a service to allow it to read and write to this storage using the API key.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label>Project</Label>
              <Select value={selectedProjectId} onValueChange={(v: string | null) => { setSelectedProjectId(v ?? ''); setSelectedServiceId('') }}>
                <SelectTrigger>
                  <SelectValue placeholder="Select project…" />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>{p.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {selectedProjectId && (
              <div className="space-y-1.5">
                <Label>Service</Label>
                <Select value={selectedServiceId} onValueChange={(v: string | null) => setSelectedServiceId(v ?? '')}>
                  <SelectTrigger>
                    <SelectValue placeholder="Select service…" />
                  </SelectTrigger>
                  <SelectContent>
                    {projectServices
                      .filter((s) => !alreadyGranted.has(s.id))
                      .map((s) => (
                        <SelectItem key={s.id} value={String(s.id)}>{s.name}</SelectItem>
                      ))}
                    {projectServices.filter((s) => !alreadyGranted.has(s.id)).length === 0 && (
                      <SelectItem value="__none" disabled>No eligible services</SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddServiceOpen(false)}>Cancel</Button>
            <Button
              onClick={() => selectedServiceId && grantAccessMutation.mutate(Number(selectedServiceId))}
              disabled={!selectedServiceId || selectedServiceId === '__none' || grantAccessMutation.isPending}
              className="gap-1.5"
            >
              {grantAccessMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Grant Access
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm */}
      <Dialog open={deleteConfirmOpen} onOpenChange={setDeleteConfirmOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-destructive">
              <AlertTriangle className="h-5 w-5" /> Delete Storage
            </DialogTitle>
            <DialogDescription>
              This will permanently delete <strong>{storage.name}</strong> and all{' '}
              <strong>{storage.key_count}</strong> stored keys. Type the storage name to confirm.
            </DialogDescription>
          </DialogHeader>
          <Input
            placeholder={storage.name}
            value={deleteConfirmName}
            onChange={(e) => setDeleteConfirmName(e.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteConfirmOpen(false)}>Cancel</Button>
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
