import { useState } from 'react'
import { Users, Shield, Trash2, MoreHorizontal, UserPlus, Copy, Check, Loader2 } from 'lucide-react'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from 'sonner'
import { useAuth } from '@/context/AuthContext'

const API_BASE = import.meta.env.VITE_API_BASE ?? ''

const MOCK_USERS = [
  { id: 'u1', name: 'Alex Chen',    email: 'alex@acme.com',    role: 'superadmin' as const, created_at: '2025-01-01' },
  { id: 'u2', name: 'Sara Kim',     email: 'sara@acme.com',    role: 'admin' as const,      created_at: '2025-01-15' },
  { id: 'u3', name: 'James Brown',  email: 'james@acme.com',   role: 'user' as const,       created_at: '2025-02-01' },
  { id: 'u4', name: 'Maria Garcia', email: 'maria@acme.com',   role: 'user' as const,       created_at: '2025-03-10' },
]

const ROLE_BADGE: Record<string, string> = {
  superadmin: 'bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-300/30',
  admin:      'bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-300/30',
  user:       'bg-slate-500/10 text-slate-600 dark:text-slate-400 border-slate-300/30',
}

export function AdminUsersPage() {
  const { user } = useAuth()
  const isSuperAdmin = user?.role === 'superadmin'
  const isAdmin = user?.role === 'admin' || isSuperAdmin

  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<'user' | 'admin' | 'superadmin'>('user')
  const [inviting, setInviting] = useState(false)
  const [inviteURL, setInviteURL] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  const handleInvite = async () => {
    if (!inviteEmail.includes('@')) { toast.error('Enter a valid email address.'); return }
    setInviting(true)
    try {
      const token = localStorage.getItem('token')
      const res = await fetch(`${API_BASE}/api/admin/invitations`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
        body: JSON.stringify({ email: inviteEmail, role: inviteRole }),
      })
      if (!res.ok) {
        const d = await res.json().catch(() => ({}))
        toast.error(d.error ?? 'Failed to send invitation.')
        return
      }
      const d = await res.json()
      setInviteURL(d.invite_url)
      toast.success(`Invitation sent to ${inviteEmail}`)
    } catch {
      toast.error('Something went wrong. Please try again.')
    } finally {
      setInviting(false)
    }
  }

  const handleCopy = async () => {
    if (!inviteURL) return
    await navigator.clipboard.writeText(inviteURL)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const handleDialogClose = (open: boolean) => {
    if (!open) {
      setInviteEmail('')
      setInviteRole('user')
      setInviteURL(null)
      setCopied(false)
    }
    setInviteOpen(open)
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">User Management</h1>
          <p className="mt-1 text-sm text-muted-foreground">Manage users and their global roles.</p>
        </div>
        {isAdmin && (
          <Dialog open={inviteOpen} onOpenChange={handleDialogClose}>
            <DialogTrigger>
              <Button size="sm" className="gap-1.5">
                <UserPlus className="h-3.5 w-3.5" /> Invite user
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>Invite a new user</DialogTitle>
                <DialogDescription>
                  An invitation link valid for 15 minutes will be sent to the user's email. If email is not configured, copy the link below.
                </DialogDescription>
              </DialogHeader>

              {!inviteURL ? (
                <div className="space-y-4 py-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="invite-email">Email address</Label>
                    <Input
                      id="invite-email"
                      type="email"
                      placeholder="colleague@company.com"
                      value={inviteEmail}
                      onChange={(e) => setInviteEmail(e.target.value)}
                      className="h-10"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="invite-role">Role</Label>
                    <Select value={inviteRole} onValueChange={(v) => setInviteRole(v as typeof inviteRole)}>
                      <SelectTrigger id="invite-role" className="h-10">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="user">User</SelectItem>
                        <SelectItem value="admin">Admin</SelectItem>
                        {isSuperAdmin && <SelectItem value="superadmin">Superadmin</SelectItem>}
                      </SelectContent>
                    </Select>
                  </div>
                </div>
              ) : (
                <div className="space-y-3 py-2">
                  <p className="text-sm text-muted-foreground">
                    Share this link with <strong>{inviteEmail}</strong>. It expires in 15 minutes.
                  </p>
                  <div className="flex gap-2">
                    <Input readOnly value={inviteURL} className="h-9 text-xs font-mono" />
                    <Button size="sm" variant="outline" onClick={handleCopy} className="shrink-0 gap-1.5">
                      {copied ? <><Check className="h-3.5 w-3.5" /> Copied</> : <><Copy className="h-3.5 w-3.5" /> Copy</>}
                    </Button>
                  </div>
                </div>
              )}

              <DialogFooter>
                {!inviteURL ? (
                  <>
                    <Button variant="outline" onClick={() => handleDialogClose(false)}>Cancel</Button>
                    <Button onClick={handleInvite} disabled={inviting} className="gap-1.5">
                      {inviting ? <><Loader2 className="h-3.5 w-3.5 animate-spin" />Sending…</> : 'Send invitation'}
                    </Button>
                  </>
                ) : (
                  <Button onClick={() => handleDialogClose(false)}>Done</Button>
                )}
              </DialogFooter>
            </DialogContent>
          </Dialog>
        )}
      </div>

      <div className="rounded-xl border border-border bg-card overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/40">
              <th className="px-4 py-3 text-left font-medium text-muted-foreground">User</th>
              <th className="px-4 py-3 text-left font-medium text-muted-foreground">Role</th>
              <th className="px-4 py-3 text-left font-medium text-muted-foreground">Joined</th>
              {isSuperAdmin && <th className="px-4 py-3 w-10" />}
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {MOCK_USERS.map((u) => (
              <tr key={u.id} className="hover:bg-muted/30 transition-colors">
                <td className="px-4 py-3">
                  <div className="flex items-center gap-3">
                    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary/10 text-primary text-xs font-semibold">
                      {u.name.split(' ').map(w => w[0]).join('').toUpperCase()}
                    </div>
                    <div>
                      <p className="font-medium">{u.name}</p>
                      <p className="text-xs text-muted-foreground">{u.email}</p>
                    </div>
                  </div>
                </td>
                <td className="px-4 py-3">
                  <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${ROLE_BADGE[u.role]}`}>
                    {u.role === 'superadmin' && <Shield className="h-3 w-3" />}
                    {u.role}
                  </span>
                </td>
                <td className="px-4 py-3 text-muted-foreground">
                  {new Date(u.created_at).toLocaleDateString()}
                </td>
                {isSuperAdmin && (
                  <td className="px-4 py-3">
                    {u.id !== user?.id && (
                      <DropdownMenu>
                        <DropdownMenuTrigger>
                          <Button variant="ghost" size="icon" className="h-7 w-7">
                            <MoreHorizontal className="h-4 w-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem className="gap-2">
                            <Users className="h-3.5 w-3.5" /> Change role
                          </DropdownMenuItem>
                          <DropdownMenuItem className="gap-2 text-destructive focus:text-destructive">
                            <Trash2 className="h-3.5 w-3.5" /> Delete user
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
