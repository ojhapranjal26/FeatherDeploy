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
import { StorageDocsButton } from '@/components/StorageDocsButton'

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
      <div className="flex items-center justify-between gap-3">
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
        <div className="flex items-center gap-2 shrink-0">
          <StorageDocsButton />
          <Button onClick={() => setCreateOpen(true)} className="gap-1.5">
            <Plus className="h-4 w-4" /> New Storage
          </Button>
        </div>
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
