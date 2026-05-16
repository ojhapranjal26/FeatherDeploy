import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Server, Plus, Trash2, Copy, Check, Loader2, RefreshCw,
  CheckCircle2, Clock, WifiOff, AlertCircle, Crown, Terminal,
  Cpu, MemoryStick, HardDrive, Globe, X
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
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from 'sonner'
import { useAuth } from '@/context/AuthContext'
import { nodesApi, clusterApi, type Node, type AddNodeResponse } from '@/api/nodes'
import { useTimezone } from '@/context/TimezoneContext'
import { formatDateFull } from '@/lib/dateFormat'

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

// ── Resource bar ─────────────────────────────────────────────────────────────

function pct(used: number | null, total: number | null): number {
  if (!used || !total || total === 0) return 0
  return Math.round((used / total) * 100)
}

function fmtBytes(bytes: number | null): string {
  if (bytes === null || bytes === 0) return '—'
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(0) + ' MB'
  return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB'
}

function MiniBar({ value, color = 'bg-blue-500' }: { value: number; color?: string }) {
  return (
    <div className="w-16 h-1.5 rounded-full bg-muted overflow-hidden">
      <div className={`h-full rounded-full ${color}`} style={{ width: `${Math.min(value, 100)}%` }} />
    </div>
  )
}

function NodeStats({ node }: { node: Node }) {
  if (!node.last_stats_at) return <span className="text-xs text-muted-foreground italic">no stats</span>
  const cpuPct = Math.round(node.cpu_usage ?? 0)
  const ramPct = pct(node.ram_used, node.ram_total)
  const diskPct = pct(node.disk_used, node.disk_total)
  return (
    <div className="flex flex-col gap-1 min-w-[130px]">
      <div className="flex items-center gap-1.5">
        <Cpu className="h-3 w-3 text-muted-foreground" />
        <MiniBar value={cpuPct} color={cpuPct > 85 ? 'bg-red-500' : 'bg-blue-500'} />
        <span className="text-xs text-muted-foreground w-8 tabular-nums">{cpuPct}%</span>
      </div>
      <div className="flex items-center gap-1.5">
        <MemoryStick className="h-3 w-3 text-muted-foreground" />
        <MiniBar value={ramPct} color={ramPct > 85 ? 'bg-red-500' : 'bg-violet-500'} />
        <span className="text-xs text-muted-foreground w-8 tabular-nums">{fmtBytes(node.ram_used)}</span>
      </div>
      <div className="flex items-center gap-1.5">
        <HardDrive className="h-3 w-3 text-muted-foreground" />
        <MiniBar value={diskPct} color={diskPct > 90 ? 'bg-red-500' : 'bg-emerald-500'} />
        <span className="text-xs text-muted-foreground w-8 tabular-nums">{fmtBytes(node.disk_used)}</span>
      </div>
    </div>
  )
}

// ── SSH Command dialog ────────────────────────────────────────────────────────

function SSHCommandDialog({ node }: { node: Node }) {
  const [open, setOpen] = useState(false)
  const [cmd, setCmd] = useState('')
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)

  const fetchCmd = async () => {
    setLoading(true)
    try {
      const res = await nodesApi.sshCommand(node.id)
      setCmd(res.command)
      setNote(res.note)
    } catch {
      toast.error('Could not fetch SSH command. Are you authenticated?')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { setOpen(v); if (v) fetchCmd() }}>
      <DialogTrigger render={
        <Button variant="ghost" size="icon" className="h-8 w-8" title="SSH into node">
          <Terminal className="h-4 w-4" />
        </Button>
      } />
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>SSH into {node.name}</DialogTitle>
          <DialogDescription>
            Run this command from the main server terminal to connect without a password.
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="py-6 flex justify-center"><Loader2 className="h-5 w-5 animate-spin" /></div>
        ) : cmd ? (
          <div className="space-y-3 py-2">
            <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm break-all">{cmd}</div>
            {note && <p className="text-xs text-muted-foreground">{note}</p>}
            <div className="flex justify-end">
              <CopyButton text={cmd} label="Copy Command" />
            </div>
          </div>
        ) : (
          <p className="py-4 text-sm text-muted-foreground">No SSH command available.</p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Node Domains dialog ──────────────────────────────────────────────────────

function NodeDomainsDialog({ node }: { node: Node }) {
  const [open, setOpen] = useState(false)
  const [domains, setDomains] = useState<string[]>(node.assigned_domains || [])
  const [newDomain, setNewDomain] = useState('')
  const qc = useQueryClient()

  const mutation = useMutation({
    mutationFn: (list: string[]) => nodesApi.updateDomains(node.id, list),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      toast.success('Assigned domains updated.')
    },
    onError: () => toast.error('Failed to update domains.'),
  })

  const addDomain = () => {
    const d = newDomain.trim().toLowerCase()
    if (!d) return
    if (domains.includes(d)) { toast.error('Domain already added.'); return }
    const newList = [...domains, d]
    setDomains(newList)
    setNewDomain('')
    mutation.mutate(newList)
  }

  const removeDomain = (d: string) => {
    const newList = domains.filter(x => x !== d)
    setDomains(newList)
    mutation.mutate(newList)
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Manage Edge Domains">
          <Globe className="h-4 w-4" />
        </Button>
      } />
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Edge Domains: {node.name}</DialogTitle>
          <DialogDescription>
            Domains assigned to this node will have their traffic routed here directly.
            Nginx on this node will handle SSL provisioning.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="flex gap-2">
            <Input
              placeholder="example.com"
              value={newDomain}
              onChange={(e) => setNewDomain(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && addDomain()}
            />
            <Button onClick={addDomain} disabled={mutation.isPending}>Add</Button>
          </div>
          <div className="space-y-2">
            {domains.length === 0 ? (
              <p className="text-sm text-muted-foreground italic text-center py-4">No domains assigned.</p>
            ) : (
              domains.map(d => (
                <div key={d} className="flex items-center justify-between rounded-md border bg-muted/30 px-3 py-1.5 text-sm">
                  <span className="font-medium">{d}</span>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-destructive hover:bg-destructive/10"
                    onClick={() => removeDomain(d)}
                    disabled={mutation.isPending}
                  >
                    <X className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function NodesPage() {
  const { user } = useAuth()
  const qc = useQueryClient()
  const { timezone } = useTimezone()

  const canManage = user?.role === 'superadmin' || user?.role === 'admin'

  const { data: nodes, isLoading, refetch } = useQuery({
    queryKey: ['nodes'],
    queryFn: nodesApi.list,
    enabled: canManage,
    refetchInterval: 10_000, // match heartbeat interval
  })

  const { data: brainStats } = useQuery({
    queryKey: ['cluster-brain'],
    queryFn: clusterApi.getBrain,
    enabled: canManage,
    refetchInterval: 10_000,
  })

  // ── Add node dialog ────────────────────────────────────────────────────────
  const [addOpen, setAddOpen] = useState(false)
  const [name, setName] = useState('')
  const [ip, setIp] = useState('')
  const [type, setType] = useState<'worker' | 'brain'>('worker')
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
    addMutation.mutate({ name: name.trim(), ip: ip.trim(), node_type: type })
  }

  const closeAdd = (open: boolean) => {
    if (!open) {
      setName(''); setIp(''); setType('worker'); setJoinResult(null)
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

  const regenerateMutation = useMutation({
    mutationFn: (id: number) => nodesApi.regenerateToken(id),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      setJoinResult(data)
      setAddOpen(true)
      toast.success('Token regenerated.')
    },
    onError: () => toast.error('Failed to regenerate token.'),
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
            <DialogTrigger render={
              <Button className="gap-1.5">
                <Plus className="h-4 w-4" />
                Add Node
              </Button>
            } />
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
                      <Label>Node Type</Label>
                      <select
                        className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                        value={type}
                        onChange={(e) => setType(e.target.value as 'worker' | 'brain')}
                      >
                        <option value="worker" className="bg-background">Worker Node</option>
                        <option value="brain" className="bg-background">Brain Node (High Availability)</option>
                      </select>
                      <p className="text-[11px] text-muted-foreground mt-1">
                        {type === 'worker' ? 'Worker nodes handle workloads and don\'t participate in Leader Elections.' : 'Brain nodes store replicas of databases and panel state, and can become leaders.'}
                      </p>
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
        <div className="space-y-4">
          {brainStats && !nodes?.some(n => n.node_id === brainStats.BrainID || n.is_brain) && (
            <div className="rounded-xl border border-amber-500/30 bg-amber-500/5 overflow-hidden">
               <div className="bg-amber-500/10 px-4 py-2 border-b border-amber-500/20 flex items-center justify-between">
                  <div className="flex items-center gap-2 text-amber-700 dark:text-amber-400 font-medium">
                    <Crown className="h-4 w-4" />
                    Main Brain Node (Local)
                  </div>
                  <Badge variant="outline" className="bg-emerald-500/10 text-emerald-600 border-emerald-500/20">Active</Badge>
               </div>
               <div className="p-4 grid grid-cols-1 md:grid-cols-3 gap-4">
                  <div className="space-y-1">
                    <p className="text-[10px] text-muted-foreground uppercase">Address</p>
                    <p className="font-mono text-sm">{brainStats.BrainAddr}</p>
                  </div>
                  <div className="space-y-1">
                    <p className="text-[10px] text-muted-foreground uppercase">Resources</p>
                    <div className="flex flex-col gap-1">
                      <div className="flex items-center gap-1.5">
                        <Cpu className="h-3 w-3 text-muted-foreground" />
                        <MiniBar value={brainStats.CPU} color="bg-amber-500" />
                        <span className="text-xs">{brainStats.CPU}%</span>
                      </div>
                      <div className="flex items-center gap-1.5">
                        <MemoryStick className="h-3 w-3 text-muted-foreground" />
                        <MiniBar value={pct(brainStats.RAMUsed, brainStats.RAMTotal)} color="bg-amber-500" />
                        <span className="text-xs">{fmtBytes(brainStats.RAMUsed)}</span>
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center justify-end">
                     <Badge variant="outline" className="text-[10px]">SYSTEM CORE</Badge>
                  </div>
               </div>
            </div>
          )}

          <div className="rounded-xl border overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr className="border-b">
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Name</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Address</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Status</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Resources</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Domains</th>
                <th className="px-4 py-3 text-left font-medium text-muted-foreground">Last Seen</th>
                <th className="w-20" />
              </tr>
            </thead>
            <tbody>
              {nodes.map((node, idx) => {
                const isBrain = node.is_brain
                return (
                  <tr key={node.id} className={idx !== nodes.length - 1 ? 'border-b' : ''}>
                    <td className="px-4 py-3">
                      <div className="flex flex-col gap-0.5">
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{node.name}</span>
                          {isBrain && (
                            <span className="inline-flex items-center gap-1 rounded-full bg-amber-500/15 border border-amber-300/30 px-2 py-0.5 text-xs font-medium text-amber-600 dark:text-amber-400" title="This node hosts the FeatherDeploy control panel">
                              <Crown className="h-3 w-3" />
                              Panel Host
                            </span>
                          )}
                          {node.wg_mesh_ip && (
                            <span className="inline-flex items-center gap-1 rounded-full bg-blue-500/15 border border-blue-300/30 px-2 py-0.5 text-xs font-medium text-blue-600 dark:text-blue-400 shrink-0" title={`WireGuard Mesh IP: ${node.wg_mesh_ip}`}>
                              <span className="h-1.5 w-1.5 rounded-full bg-blue-500 animate-pulse" />
                              wg0: {node.wg_mesh_ip}
                            </span>
                          )}
                        </div>
                        {node.hostname && (
                          <div className="text-[10px] text-muted-foreground flex items-center gap-1">
                            <span className="opacity-70">Host:</span> {node.hostname}
                            {node.os_info && <span className="opacity-40">| {node.os_info}</span>}
                          </div>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3 font-mono text-muted-foreground">
                      {node.ip}
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge status={node.status} />
                    </td>
                    <td className="px-4 py-3">
                      <NodeStats node={node} />
                    </td>
                    <td className="px-4 py-3">
                      {node.assigned_domains?.length > 0 ? (
                        <div className="flex flex-wrap gap-1 max-w-[150px]">
                          {node.assigned_domains.map(d => (
                            <span key={d} className="px-1.5 py-0.5 rounded bg-muted text-[10px] border truncate" title={d}>{d}</span>
                          ))}
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground italic">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-muted-foreground text-xs">
                      {node.last_seen
                        ? formatDateFull(node.last_seen, timezone)
                        : <span className="italic opacity-50">never</span>}
                    </td>
                    <td className="px-4 py-3 flex items-center gap-1">
                      {node.status === 'pending' && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8"
                          title="Regenerate Join Command"
                          onClick={() => regenerateMutation.mutate(node.id)}
                        >
                          <RefreshCw className={`h-4 w-4 ${regenerateMutation.isPending ? 'animate-spin' : ''}`} />
                        </Button>
                      )}
                      <NodeDomainsDialog node={node} />
                      <SSHCommandDialog node={node} />
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
                )
              })}
            </tbody>
          </table>
        </div>
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
