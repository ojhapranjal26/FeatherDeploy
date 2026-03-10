import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Plus, Trash2, Eye, EyeOff, Upload } from 'lucide-react'
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
  const [revealedIds, setRevealedIds] = useState<Set<string>>(new Set())
  const [bulkText, setBulkText] = useState('')

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

  const parseBulk = () => {
    const lines = bulkText
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#') && l.includes('='))
    const vars: UpsertEnvPayload[] = lines.map((l) => {
      const eq = l.indexOf('=')
      return {
        key: l.slice(0, eq).trim(),
        value: l.slice(eq + 1).trim().replace(/^["']|["']$/g, ''),
        is_secret: true,
      }
    })
    bulkMutation.mutate(vars)
  }

  const toggleReveal = (id: string) => {
    setRevealedIds((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })
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
                        ? v.value
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
                          onClick={() => toggleReveal(v.id)}
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
                          if (confirm(`Remove "${v.key}"?`)) deleteMutation.mutate(v.id)
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
      <Dialog open={bulkOpen} onOpenChange={setBulkOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Bulk import (.env format)</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <Textarea
              rows={10}
              className="font-mono text-xs"
              placeholder={'KEY=value\nANOTHER_KEY=another_value'}
              value={bulkText}
              onChange={(e) => setBulkText(e.target.value)}
            />
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={() => setBulkOpen(false)}>Cancel</Button>
              <Button onClick={parseBulk} disabled={bulkMutation.isPending}>
                Import
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
