import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, Plus, Trash2, ShieldCheck, Shield } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { toast } from 'sonner'
import { domainsApi } from '@/api/domains'
import type { AddDomainPayload } from '@/api/domains'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
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
  domain: z
    .string()
    .regex(
      /^[a-z0-9][a-z0-9\-.]*[a-z0-9]$/,
      'Enter a valid domain (e.g. app.example.com)'
    ),
  tls: z.boolean().optional(),
})
type FormData = z.infer<typeof schema>

export function DomainsPage() {
  const { projectId, serviceId } = useParams<{ projectId: string; serviceId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = useState(false)

  const { data: domains, isLoading } = useQuery({
    queryKey: ['domains', serviceId],
    queryFn: () => domainsApi.list(projectId!, serviceId!),
    enabled: !!projectId && !!serviceId,
  })

  const addMutation = useMutation({
    mutationFn: (data: AddDomainPayload) =>
      domainsApi.add(projectId!, serviceId!, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['domains', serviceId] })
      setAddOpen(false)
      reset()
      toast.success('Domain added. Point your DNS A record to this server.')
    },
    onError: () => toast.error('Failed to add domain.'),
  })

  const deleteMutation = useMutation({
    mutationFn: (domainId: string) =>
      domainsApi.delete(projectId!, serviceId!, domainId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['domains', serviceId] })
      toast.success('Domain removed.')
    },
    onError: () => toast.error('Failed to remove domain.'),
  })

  const verifyMutation = useMutation({
    mutationFn: (domainId: string) =>
      domainsApi.verify(projectId!, serviceId!, domainId),
    onSuccess: (d) => {
      qc.invalidateQueries({ queryKey: ['domains', serviceId] })
      if (d.verified) {
        toast.success('DNS verified successfully.')
      } else {
        toast.error(
          `DNS not verified. Resolved: ${d.resolved_ip ?? 'none'}, expected: ${d.server_ip}`
        )
      }
    },
    onError: () => toast.error('DNS verification failed.'),
  })

  const { register, handleSubmit, reset, formState: { errors, isSubmitting } } =
    useForm<FormData>({ resolver: zodResolver(schema), defaultValues: { tls: true } })

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
        <h1 className="text-2xl font-semibold">Domains</h1>
        <Button size="sm" className="gap-1.5" onClick={() => setAddOpen(true)}>
          <Plus className="h-4 w-4" /> Add domain
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
        </div>
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Domain</TableHead>
                <TableHead>TLS</TableHead>
                <TableHead>DNS</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {domains?.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="py-10 text-center text-muted-foreground">
                    No domains configured.
                  </TableCell>
                </TableRow>
              )}
              {domains?.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="font-mono text-sm">{d.domain}</TableCell>
                  <TableCell>
                    {d.tls ? (
                      <div className="flex items-center gap-1 text-xs text-green-700">
                        <ShieldCheck className="h-3.5 w-3.5" /> HTTPS
                      </div>
                    ) : (
                      <div className="flex items-center gap-1 text-xs text-muted-foreground">
                        <Shield className="h-3.5 w-3.5" /> HTTP
                      </div>
                    )}
                  </TableCell>
                  <TableCell>
                    {d.verified ? (
                      <Badge variant="outline" className="bg-green-500/10 text-green-700 border-green-200 text-xs">
                        Verified
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="text-xs text-amber-700 border-amber-200 bg-amber-500/10">
                        Unverified
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      {!d.verified && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => verifyMutation.mutate(d.id)}
                          disabled={verifyMutation.isPending}
                        >
                          Verify DNS
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-destructive hover:text-destructive"
                        onClick={() => {
                          if (confirm(`Remove domain "${d.domain}"?`)) {
                            deleteMutation.mutate(d.id)
                          }
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

      {/* Add domain dialog */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add domain</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={handleSubmit((d) => addMutation.mutate({ domain: d.domain, tls: d.tls ?? true }))}
            className="space-y-4 pt-2"
          >
            <div className="space-y-1.5">
              <Label>Domain name</Label>
              <Input placeholder="app.example.com" {...register('domain')} />
              {errors.domain && (
                <p className="text-xs text-destructive">{errors.domain.message}</p>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              After adding, point your domain's A record to this server's IP.
              Caddy will automatically obtain a TLS certificate.
            </p>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => setAddOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={isSubmitting || addMutation.isPending}>
                Add domain
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
