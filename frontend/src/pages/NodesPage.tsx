import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Server, Plus, Trash2, Copy, Check, Loader2, RefreshCw,
  CheckCircle2, Clock, WifiOff, AlertCircle,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from 'sonner'
import { useAuth } from '@/context/AuthContext'
import { nodesApi, type Node, type AddNodeResponse } from '@/api/nodes'

// ── Status badge ─────────────────────────────────────────────────────────────

const STATUS_CFG = {
  connected: { label: 'Connected', icon: CheckCircle2, cls: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-300/30' },
  pending:   { label: 'Pending',   icon: Clock,        cls: 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-300/30' },
  offline:   { label: 'Offline',   icon: WifiOff,      cls: 'bg-slate-500/10 text-slate-600 dark:text-slate-400 border-slate-300/30' },
  error:     { label: 'Error',     icon: AlertCircle,  cls: 'bg-red-500/15 text-red-600 dark:text-red-400 border-red-300/30' },
} as const

function StatusBadge({ status }: { status: Node['status'] }) {
  const cfg = STATUS_CFG[status] ?? STATUS_CFG.offline
  const Icon = cfg.icon
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${cfg.cls}`}>
      <Icon className="h-3 w-3" />
      {cfg.label}
    </span>
  )
}

// ── Copy button ───────────────────────────────────────────────────────────────

function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const handleCopy = async () => {
    await navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <Button variant="outline" size="sm" onClick={handleCopy} className="gap-1.5 shrink-0">
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
      {copied ? 'Copied!' : label}
    </Button>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function NodesPage() {
  const { user } = useAuth()
  const qc = useQueryClient()

  const canManage = user?.role === 'superadmin' || user?.role === 'admin'

  const { data: nodes, isLoading, refetch } = useQuery({
    queryKey: ['nodes'],
    queryFn: nodesApi.list,
    enabled: canManage,
    refetchInterval: 30_000, // auto-refresh every 30s
  })

  // ── Add node dialog ────────────────────────────────────────────────────────
  const [addOpen, setAddOpen] = useState(false)
  const [name, setName] = useState('')
  const [ip, setIp] = useState('')
  const [port, setPort] = useState('')
  const [joinResult, setJoinResult] = useState<AddNodeResponse | null>(null)

  const addMutation = useMutation({
    mutationFn: nodesApi.add,
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      setJoinResult(data)
      toast.success(`Node "${data.name}" added — copy & run the join command.`)
    },
    onError: (err: unknown) => {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
      toast.error(msg ?? 'Failed to add node.')
    },
  })

  const handleAdd = () => {
    if (!name.trim()) { toast.error('Node name is required.'); return }
    if (!ip.trim()) { toast.error('IP address is required.'); return }
    addMutation.mutate({ name: name.trim(), ip: ip.trim(), port: port ? parseInt(port) : undefined })
  }

  const closeAdd = (open: boolean) => {
    if (!open) {
      setName(''); setIp(''); setPort(''); setJoinResult(null)
    }
    setAddOpen(open)
  }

  // ── Delete ─────────────────────────────────────────────────────────────────
  const deleteMutation = useMutation({
    mutationFn: (id: number) => nodesApi.delete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      toast.success('Node removed.')
    },
    onError: () => toast.error('Failed to remove node.'),
  })

  // ── Render ─────────────────────────────────────────────────────────────────
  if (!canManage) {
    return (
      <div className="flex flex-col items-center justify-center py-24 text-muted-foreground gap-2">
        <Server className="h-10 w-10 opacity-30" />
        <p>You don't have permission to manage nodes.</p>
      </div>
    )
  }

  return (
    <div className="p-6 max-w-5xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Cluster Nodes</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            Connect worker nodes via mTLS. Nodes replicate the rqlite database
            and can serve the panel independently if this server goes down.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="icon" onClick={() => refetch()} title="Refresh">
            <RefreshCw className="h-4 w-4" />
          </Button>

          <Dialog open={addOpen} onOpenChange={closeAdd}>
            <DialogTrigger>
              <Button className="gap-1.5">
                <Plus className="h-4 w-4" />
                Add Node
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-lg">
              <DialogHeader>
                <DialogTitle>Add Worker Node</DialogTitle>
                <DialogDescription>
                  Register a new server. After saving, copy the generated join command
                  and run it on the target server as root.
                </DialogDescription>
              </DialogHeader>

              {!joinResult ? (
                <>
                  <div className="space-y-4 py-2">
                    <div className="space-y-1.5">
                      <Label>Name</Label>
                      <Input
                        placeholder="e.g. node-eu-1"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label>IP Address</Label>
                      <Input
                        placeholder="e.g. 192.168.1.20"
                        value={ip}
                        onChange={(e) => setIp(e.target.value)}
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label>Port <span className="text-muted-foreground font-normal">(optional, default 7443)</span></Label>
                      <Input
                        type="number"
                        placeholder="7443"
                        value={port}
                        onChange={(e) => setPort(e.target.value)}
                      />
                    </div>
                  </div>
                  <DialogFooter>
                    <Button variant="outline" onClick={() => closeAdd(false)}>Cancel</Button>
                    <Button onClick={handleAdd} disabled={addMutation.isPending}>
                      {addMutation.isPending && <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />}
                      Generate Join Command
                    </Button>
                  </DialogFooter>
                </>
              ) : (
                <>
                  <div className="space-y-4 py-2">
                    <p className="text-sm text-muted-foreground">
                      Run the command below on <strong>{joinResult.ip}</strong> as root.
                      It will install all dependencies, download the node binary, and join
                      this cluster. The token expires in <strong>24 hours</strong>.
                    </p>
                    <div className="rounded-md bg-muted p-3 font-mono text-xs break-all">
                      {joinResult.join_command}
                    </div>
                    <div className="flex justify-end">
                      <CopyButton text={joinResult.join_command} label="Copy Command" />
                    </div>
                  </div>
                  <DialogFooter>
                    <Button onClick={() => closeAdd(false)}>Done</Button>
                  </DialogFooter>
                </>
              )}
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {/* Node table */}
      {isLoading ? (
        <div className="space-y-3">
          {[...Array(3)].map((_, i) => <Skeleton key={i} className="h-16 w-full rounded-lg" />)}
        </div>
      ) : !nodes?.length ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-20 text-muted-foreground gap-3">
          <Server className="h-10 w-10 opacity-30" />
          <p className="font-medium">No worker nodes yet</p>
          <p className="text-sm">Add a node to start building a distributed cluster.</p>
        </div>
      ) : (
        <div className="rounded-xl border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr className="border-b">
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Name</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Address</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Status</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Last Seen</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">rqlite</th>
                <th className="w-12" />
              </tr>
            </thead>
            <tbody>
              {nodes.map((node, idx) => (
                <tr key={node.id} className={idx !== nodes.length - 1 ? 'border-b' : ''}>
                  <td className="px-4 py-3 font-medium">{node.name}</td>
                  <td className="px-4 py-3 font-mono text-muted-foreground">
                    {node.ip}:{node.port}
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge status={node.status} />
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {node.last_seen
                      ? new Date(node.last_seen).toLocaleString()
                      : <span className="italic opacity-50">never</span>}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
                    {node.rqlite_addr || <span className="italic opacity-50">—</span>}
                  </td>
                  <td className="px-4 py-3">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-destructive hover:text-destructive hover:bg-destructive/10"
                      onClick={() => {
                        if (confirm(`Remove node "${node.name}"?`)) deleteMutation.mutate(node.id)
                      }}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Info callout */}
      <div className="rounded-lg border bg-muted/30 p-4 text-sm text-muted-foreground space-y-1">
        <p className="font-medium text-foreground">How cluster nodes work</p>
        <ul className="list-disc list-inside space-y-0.5 pl-1">
          <li>Each node runs <code className="text-xs font-mono bg-muted px-1 py-0.5 rounded">featherdeploy-node</code> connected via mTLS.</li>
          <li>Nodes join the rqlite Raft cluster and replicate all data automatically.</li>
          <li>If this main server goes offline, any connected node can serve the panel.</li>
          <li>Env vars are encrypted with AES-256-GCM during the join handshake.</li>
        </ul>
      </div>
    </div>
  )
}
