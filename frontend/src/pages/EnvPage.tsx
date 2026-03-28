import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Plus, Trash2, Eye, EyeOff, Upload, Copy, Download, X, Lock, Globe } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { envApi } from '@/api/env'
import type { UpsertEnvPayload } from '@/api/env'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

const schema = z.object({
  key:       z.string().min(1).regex(/^[A-Za-z_][A-Za-z0-9_]*$/, 'Valid env key required'),
  value:     z.string().min(1, 'Value is required'),
  is_secret: z.boolean().optional(),
})
type FormData = z.infer<typeof schema>

export function EnvPage() {
  const { projectId, serviceId } = useParams<{ projectId: string; serviceId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = useState(false)
  const [bulkOpen, setBulkOpen] = useState(false)
  const [revealedIds, setRevealedIds] = useState<Set<number>>(new Set())
  const [revealedValues, setRevealedValues] = useState<Record<number, string>>({})
  const [bulkText, setBulkText] = useState('')
  const [previewRows, setPreviewRows] = useState<{id: string; key: string; value: string; isSecret: boolean}[]>([])

  const { data: vars, isLoading } = useQuery({
    queryKey: ['env', serviceId],
    queryFn: () => envApi.list(projectId!, serviceId!),
    enabled: !!projectId && !!serviceId,
  })

  const upsertMutation = useMutation({
    mutationFn: (data: UpsertEnvPayload) =>
      envApi.upsert(projectId!, serviceId!, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      setAddOpen(false)
      reset()
      toast.success('Variable saved.')
    },
    onError: () => toast.error('Failed to save variable.'),
  })

  const deleteMutation = useMutation({
    mutationFn: (envId: string) => envApi.delete(projectId!, serviceId!, envId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      toast.success('Variable removed.')
    },
    onError: () => toast.error('Failed to remove variable.'),
  })

  const bulkMutation = useMutation({
    mutationFn: (vars: UpsertEnvPayload[]) =>
      envApi.bulkUpsert(projectId!, serviceId!, vars),
    onSuccess: (d) => {
      qc.invalidateQueries({ queryKey: ['env', serviceId] })
      setBulkOpen(false)
      setBulkText('')
      toast.success(`${d.upserted} variable(s) imported.`)
    },
    onError: () => toast.error('Bulk import failed.'),
  })

  const { register, handleSubmit, reset, formState: { errors, isSubmitting } } =
    useForm<FormData>({ resolver: zodResolver(schema), defaultValues: { is_secret: true } })

  const parseLine = (l: string) => {
    const eq = l.indexOf('=')
    return {
      id: `${Math.random()}`,
      key: l.slice(0, eq).trim(),
      value: l.slice(eq + 1).trim().replace(/^["']|["']$/g, ''),
      isSecret: true,
    }
  }

  const handleBulkText = (text: string) => {
    setBulkText(text)
    const rows = text
      .split('\n')
      .map(l => l.trim())
      .filter(l => l && !l.startsWith('#') && l.includes('='))
      .map(parseLine)
    setPreviewRows(rows)
  }

  const updatePreviewRow = (id: string, field: 'key' | 'value' | 'isSecret', val: string | boolean) =>
    setPreviewRows(rows => rows.map(r => r.id === id ? { ...r, [field]: val } : r))

  const removePreviewRow = (id: string) =>
    setPreviewRows(rows => rows.filter(r => r.id !== id))

  const importPreview = () => {
    const vars: UpsertEnvPayload[] = previewRows
      .filter(r => r.key.trim())
      .map(r => ({ key: r.key.trim(), value: r.value, is_secret: r.isSecret }))
    bulkMutation.mutate(vars)
  }

  const toggleReveal = async (id: number, key: string, isSecret: boolean) => {
    const currentlyRevealed = revealedIds.has(id)
    setRevealedIds((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })
    // Toggling off — nothing more to do
    if (currentlyRevealed) return
    if (!isSecret) return
    // Already fetched — reuse cached plaintext
    if (revealedValues[id] !== undefined) return
    try {
      const value = await envApi.reveal(projectId!, serviceId!, key)
      setRevealedValues(prev => ({ ...prev, [id]: value }))
    } catch {
      toast.error('Failed to reveal secret.')
      // Close the eye since we have no value to show
      setRevealedIds(prev => { const n = new Set(prev); n.delete(id); return n })
    }
  }

  const buildEnvText = () => {
    return (vars ?? []).map(v => {
      const val = v.is_secret ? (revealedValues[v.id] ?? '••••••••') : v.value
      return `${v.key}=${val}`
    }).join('\n')
  }

  const copyEnv = async () => {
    try {
      await navigator.clipboard.writeText(buildEnvText())
      toast.success('Copied to clipboard.')
    } catch {
      toast.error('Clipboard copy failed.')
    }
  }

  const downloadEnv = () => {
    const blob = new Blob([buildEnvText()], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = '.env'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-6">
      <Button
        variant="ghost"
        size="sm"
        className="mb-4 gap-1.5 text-muted-foreground"
        onClick={() => navigate(`/projects/${projectId}/services/${serviceId}`)}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> Back to service
      </Button>

      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Environment variables</h1>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={copyEnv}
            disabled={!vars?.length}
          >
            <Copy className="h-3.5 w-3.5" /> Copy .env
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={downloadEnv}
            disabled={!vars?.length}
          >
            <Download className="h-3.5 w-3.5" /> Download .env
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setBulkOpen(true)}
          >
            <Upload className="h-3.5 w-3.5" /> Bulk import
          </Button>
          <Button size="sm" className="gap-1.5" onClick={() => setAddOpen(true)}>
            <Plus className="h-4 w-4" /> Add variable
          </Button>
        </div>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
        </div>
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Key</TableHead>
                <TableHead>Value</TableHead>
                <TableHead>Type</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {vars?.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="py-10 text-center text-muted-foreground">
                    No variables yet.
                  </TableCell>
                </TableRow>
              )}
              {vars?.map((v) => (
                <TableRow key={v.id}>
                  <TableCell className="font-mono text-sm">{v.key}</TableCell>
                  <TableCell className="font-mono text-sm max-w-xs truncate">
                    {v.is_secret
                      ? revealedIds.has(v.id)
                        ? (revealedValues[v.id] ?? <span className="text-muted-foreground italic text-xs">loading…</span>)
                        : '••••••••'
                      : v.value}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {v.is_secret ? 'Secret' : 'Plain'}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      {v.is_secret && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7"
                          onClick={() => toggleReveal(v.id, v.key, v.is_secret)}
                        >
                          {revealedIds.has(v.id)
                            ? <EyeOff className="h-3.5 w-3.5" />
                            : <Eye className="h-3.5 w-3.5" />}
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-destructive hover:text-destructive"
                        onClick={() => {
                          if (confirm(`Remove "${v.key}"?`)) deleteMutation.mutate(v.key)
                        }}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Add variable dialog */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add variable</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={handleSubmit((d) => upsertMutation.mutate(d))}
            className="space-y-4 pt-2"
          >
            <div className="space-y-1.5">
              <Label>Key</Label>
              <Input placeholder="DATABASE_URL" {...register('key')} />
              {errors.key && <p className="text-xs text-destructive">{errors.key.message}</p>}
            </div>
            <div className="space-y-1.5">
              <Label>Value</Label>
              <Input type="password" placeholder="value" {...register('value')} />
              {errors.value && <p className="text-xs text-destructive">{errors.value.message}</p>}
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setAddOpen(false)}>Cancel</Button>
              <Button type="submit" disabled={isSubmitting || upsertMutation.isPending}>Save</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>

      {/* Bulk import dialog */}
      <Dialog open={bulkOpen} onOpenChange={(o) => { setBulkOpen(o); if (!o) { setBulkText(''); setPreviewRows([]) } }}>
        <DialogContent className="max-w-2xl flex flex-col max-h-[90dvh]">
          <DialogHeader className="shrink-0">
            <DialogTitle>Import .env file</DialogTitle>
          </DialogHeader>
          {/* Scrollable body */}
          <div className="flex-1 min-h-0 overflow-y-auto space-y-3 pr-1">
            <Textarea
              rows={5}
              className="font-mono text-xs resize-none"
              placeholder={'DATABASE_URL=postgres://user:pass@host/db\nAPI_KEY=supersecret\nDEBUG=false'}
              value={bulkText}
              onChange={(e) => handleBulkText(e.target.value)}
              autoFocus
            />
            {previewRows.length > 0 && (
              <div className="rounded-lg border overflow-hidden">
                <div className="bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground flex items-center justify-between">
                  <span>{previewRows.length} variable{previewRows.length !== 1 ? 's' : ''} — review &amp; edit before importing</span>
                </div>
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
                    {previewRows.map(row => (
                      <TableRow key={row.id}>
                        <TableCell className="py-1">
                          <Input
                            className="h-7 font-mono text-xs"
                            value={row.key}
                            onChange={e => updatePreviewRow(row.id, 'key', e.target.value)}
                          />
                        </TableCell>
                        <TableCell className="py-1">
                          <Input
                            className="h-7 font-mono text-xs"
                            type={row.isSecret ? 'password' : 'text'}
                            value={row.value}
                            onChange={e => updatePreviewRow(row.id, 'value', e.target.value)}
                          />
                        </TableCell>
                        <TableCell className="py-1">
                          <button
                            type="button"
                            className={`flex items-center gap-1 text-xs rounded-full px-2.5 py-0.5 font-medium transition-colors ${
                              row.isSecret
                                ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400'
                                : 'bg-muted text-muted-foreground'
                            }`}
                            onClick={() => updatePreviewRow(row.id, 'isSecret', !row.isSecret)}
                          >
                            {row.isSecret
                              ? <><Lock className="h-2.5 w-2.5" /> Secret</>
                              : <><Globe className="h-2.5 w-2.5" /> Plain</>}
                          </button>
                        </TableCell>
                        <TableCell className="py-1">
                          <Button
                            variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground"
                            onClick={() => removePreviewRow(row.id)}
                          >
                            <X className="h-3 w-3" />
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </div>
          {/* Sticky footer — always visible */}
          <div className="flex justify-end gap-2 pt-3 mt-1 border-t shrink-0">
            <Button variant="outline" onClick={() => setBulkOpen(false)}>Cancel</Button>
            <Button
              onClick={importPreview}
              disabled={!previewRows.filter(r => r.key.trim()).length || bulkMutation.isPending}
            >
              {previewRows.filter(r => r.key.trim()).length > 0
                ? `Import ${previewRows.filter(r => r.key.trim()).length} variable${previewRows.filter(r => r.key.trim()).length !== 1 ? 's' : ''}`
                : 'Import'}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
