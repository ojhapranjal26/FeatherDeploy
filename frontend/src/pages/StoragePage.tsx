import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  HardDrive,
  Plus,
  Database,
  Loader2,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from 'sonner'
import { storageApi } from '@/api/storage'
import { formatDate } from '@/lib/dateFormat'
import { useTimezone } from '@/context/TimezoneContext'

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function StoragePage() {
  const qc = useQueryClient()
  const { timezone } = useTimezone()

  const { data: storages, isLoading } = useQuery({
    queryKey: ['storages'],
    queryFn: storageApi.list,
  })

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')

  const createMutation = useMutation({
    mutationFn: storageApi.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['storages'] })
      setCreateOpen(false)
      setName('')
      setDesc('')
      toast.success('Storage created. Grant services access from the storage page.')
    },
    onError: (err: any) => {
      const msg = err?.response?.data?.error ?? 'Failed to create storage.'
      toast.error(msg)
    },
  })

  const handleCreate = () => {
    if (!name.trim()) { toast.error('Name is required.'); return }
    createMutation.mutate({ name: name.trim(), description: desc.trim() })
  }

  return (
    <div className="space-y-6 pb-8">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <HardDrive className="h-6 w-6 text-primary" />
            Storage
          </h1>
          <p className="text-muted-foreground mt-1 text-sm">
            Encrypted disk-backed object stores. Each service gets its own API key with
            configurable read/write permissions. Files are AES-256 encrypted at rest.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)} className="gap-1.5">
          <Plus className="h-4 w-4" /> New Storage
        </Button>
      </div>

      {/* Storage list */}
      {isLoading ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-36 rounded-xl" />
          ))}
        </div>
      ) : !storages?.length ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-16 text-center text-muted-foreground">
          <Database className="h-10 w-10 mb-3 opacity-30" />
          <p className="font-medium">No storages yet</p>
          <p className="text-sm mt-1">Create a storage to share files between your services.</p>
          <Button variant="outline" className="mt-4 gap-1.5" onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New Storage
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {storages.map((s) => (
            <Link
              key={s.id}
              to={`/storage/${s.id}`}
              className="group flex flex-col gap-3 rounded-xl border bg-card p-5 shadow-sm hover:border-primary/40 hover:shadow-md transition-all"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="flex items-center gap-2 min-w-0">
                  <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                    <HardDrive className="h-4 w-4" />
                  </div>
                  <span className="font-semibold truncate">{s.name}</span>
                </div>
              </div>

              {s.description && (
                <p className="text-sm text-muted-foreground line-clamp-2">{s.description}</p>
              )}

              <div className="flex items-center gap-4 text-xs text-muted-foreground mt-auto">
                <span>{formatBytes(s.size_bytes)}</span>
                <span className="flex items-center gap-1">
                  <Database className="h-3 w-3" />
                  {s.access_count} {s.access_count === 1 ? 'service' : 'services'}
                </span>
              </div>

              <div className="text-[11px] text-muted-foreground/60">
                Created {formatDate(s.created_at, timezone)}
              </div>
            </Link>
          ))}
        </div>
      )}

      {/* Create dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Create Storage</DialogTitle>
            <DialogDescription>
              A new encrypted object store will be created on disk. Grant individual services
              access from the storage settings — each service gets its own API key.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="storage-name">Name</Label>
              <Input
                id="storage-name"
                placeholder="e.g. my-app-files"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="storage-desc">Description <span className="text-muted-foreground">(optional)</span></Label>
              <Textarea
                id="storage-desc"
                placeholder="What is this storage used for?"
                rows={2}
                value={desc}
                onChange={(e) => setDesc(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
            <Button onClick={handleCreate} disabled={createMutation.isPending} className="gap-1.5">
              {createMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from 'sonner'
import { storageApi, type StorageCreated } from '@/api/storage'
import { formatDate } from '@/lib/dateFormat'
import { useTimezone } from '@/context/TimezoneContext'

function CopyButton({ text }: { text: string }) {
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
      className="text-muted-foreground hover:text-foreground transition-colors"
      title="Copy"
    >
      {copied ? <Check className="h-3.5 w-3.5 text-green-500" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  )
}

function APIKeyReveal({ apiKey }: { apiKey: string }) {
  const [shown, setShown] = useState(false)
  return (
    <div className="flex items-center gap-2 font-mono text-sm bg-muted rounded px-3 py-2">
      <span className="flex-1 break-all select-all">{shown ? apiKey : '•'.repeat(Math.min(apiKey.length, 40))}</span>
      <button
        onClick={() => setShown((s) => !s)}
        className="text-muted-foreground hover:text-foreground transition-colors shrink-0"
        title={shown ? 'Hide' : 'Reveal'}
      >
        {shown ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
      <CopyButton text={apiKey} />
    </div>
  )
}

export function StoragePage() {
  const qc = useQueryClient()
  const { timezone } = useTimezone()

  const { data: storages, isLoading } = useQuery({
    queryKey: ['storages'],
    queryFn: storageApi.list,
  })

  // ── Create dialog ─────────────────────────────────────────────────────────
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')

  // After creation, hold the returned API key so user can copy it
  const [newStorage, setNewStorage] = useState<StorageCreated | null>(null)
  const [keyDialogOpen, setKeyDialogOpen] = useState(false)

  const createMutation = useMutation({
    mutationFn: storageApi.create,
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['storages'] })
      setCreateOpen(false)
      setName('')
      setDesc('')
      setNewStorage(data)
      setKeyDialogOpen(true)
    },
    onError: (err: any) => {
      const msg = err?.response?.data?.error ?? 'Failed to create storage.'
      toast.error(msg)
    },
  })

  const handleCreate = () => {
    if (!name.trim()) { toast.error('Name is required.'); return }
    createMutation.mutate({ name: name.trim(), description: desc.trim() })
  }

  return (
    <div className="space-y-6 pb-8">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold flex items-center gap-2">
            <HardDrive className="h-6 w-6 text-primary" />
            Storage
          </h1>
          <p className="text-muted-foreground mt-1 text-sm">
            Encrypted key-value stores for your services. Each storage has an API key that
            services use to read and write values over the internal network.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)} className="gap-1.5">
          <Plus className="h-4 w-4" /> New Storage
        </Button>
      </div>

      {/* Storage list */}
      {isLoading ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-36 rounded-xl" />
          ))}
        </div>
      ) : !storages?.length ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-16 text-center text-muted-foreground">
          <Database className="h-10 w-10 mb-3 opacity-30" />
          <p className="font-medium">No storages yet</p>
          <p className="text-sm mt-1">Create a storage to securely share data between your services.</p>
          <Button variant="outline" className="mt-4 gap-1.5" onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New Storage
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {storages.map((s) => (
            <Link
              key={s.id}
              to={`/storage/${s.id}`}
              className="group flex flex-col gap-3 rounded-xl border bg-card p-5 shadow-sm hover:border-primary/40 hover:shadow-md transition-all"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="flex items-center gap-2 min-w-0">
                  <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                    <HardDrive className="h-4 w-4" />
                  </div>
                  <span className="font-semibold truncate">{s.name}</span>
                </div>
              </div>

              {s.description && (
                <p className="text-sm text-muted-foreground line-clamp-2">{s.description}</p>
              )}

              <div className="flex items-center gap-4 text-xs text-muted-foreground mt-auto">
                <span className="flex items-center gap-1">
                  <Key className="h-3 w-3" />
                  {s.key_count} {s.key_count === 1 ? 'key' : 'keys'}
                </span>
                <span className="flex items-center gap-1">
                  <Database className="h-3 w-3" />
                  {s.access_count} {s.access_count === 1 ? 'service' : 'services'}
                </span>
              </div>

              <div className="text-[11px] text-muted-foreground/60">
                Key: <span className="font-mono">{s.api_key_preview}••••</span>
                {' · '}
                Created {formatDate(s.created_at, timezone)}
              </div>
            </Link>
          ))}
        </div>
      )}

      {/* Create dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Create Storage</DialogTitle>
            <DialogDescription>
              A new API key will be generated. Copy it after creation — it is only shown once.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="storage-name">Name</Label>
              <Input
                id="storage-name"
                placeholder="e.g. my-app-cache"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="storage-desc">Description <span className="text-muted-foreground">(optional)</span></Label>
              <Textarea
                id="storage-desc"
                placeholder="What is this storage used for?"
                rows={2}
                value={desc}
                onChange={(e) => setDesc(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
            <Button onClick={handleCreate} disabled={createMutation.isPending} className="gap-1.5">
              {createMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* One-time API key dialog */}
      <Dialog open={keyDialogOpen} onOpenChange={setKeyDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Key className="h-5 w-5 text-amber-500" />
              Save your API key
            </DialogTitle>
            <DialogDescription>
              This is the only time the plaintext key will be shown. Copy it now and store it
              securely — it cannot be recovered. You can rotate the key from the storage settings
              if it is ever lost.
            </DialogDescription>
          </DialogHeader>
          {newStorage && (
            <div className="space-y-4 py-2">
              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">Storage name</Label>
                <p className="font-medium">{newStorage.name}</p>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">API Key</Label>
                <APIKeyReveal apiKey={newStorage.api_key} />
              </div>
              <p className="text-xs text-amber-600 dark:text-amber-400 bg-amber-50 dark:bg-amber-900/20 rounded-lg px-3 py-2 border border-amber-200/60 dark:border-amber-700/40">
                Set this as an environment variable in your service (e.g.{' '}
                <code className="font-mono">STORAGE_KEY</code>) and use it with the{' '}
                <code className="font-mono">X-Storage-Key</code> request header.
              </p>
            </div>
          )}
          <DialogFooter>
            <Button onClick={() => setKeyDialogOpen(false)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
