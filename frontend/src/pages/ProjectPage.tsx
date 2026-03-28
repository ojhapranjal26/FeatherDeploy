import { useState, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Plus, ChevronLeft, Rocket, Settings2, Trash2,
  ExternalLink, GitBranch, Terminal,
  Globe, AlertTriangle, Users, UserMinus, Loader2,
} from 'lucide-react'
import { toast } from 'sonner'
import { projectsApi, usersApi, type ProjectMember } from '@/api/projects'
import { servicesApi } from '@/api/services'
import { deploymentsApi } from '@/api/deployments'
import type { Service } from '@/api/services'
import { ServiceStatusBadge } from '@/components/ServiceStatusBadge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

function ServiceCard({ service, projectId, canEdit }: { service: Service; projectId: string; canEdit: boolean }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [confirmDelete, setConfirmDelete] = useState(false)

  const deployMutation = useMutation({
    mutationFn: () =>
      deploymentsApi.trigger(projectId, service.id, {
        deploy_type: service.deploy_type,
        repo_url: service.repo_url,
        repo_branch: service.repo_branch,
      }),
    onSuccess: (data) => {
      toast.success('Deployment triggered.')
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      navigate(`/projects/${projectId}/services/${service.id}/deployments/${data.deployment_id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to trigger deployment.'),
  })

  const deleteMutation = useMutation({
    mutationFn: () => servicesApi.delete(projectId, service.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      toast.success('Service deleted. Container cleanup running in background.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to delete service.'),
  })

  const isDeploying = service.status === 'deploying'

  return (
    <>
      <Card className="group relative flex flex-col overflow-hidden transition-shadow hover:shadow-md">
        {/* Status strip */}
        <div className={cn(
          'absolute inset-x-0 top-0 h-0.5',
          service.status === 'running'   ? 'bg-emerald-500' :
          service.status === 'deploying' ? 'bg-amber-400 animate-pulse' :
          service.status === 'error'     ? 'bg-red-500' :
          'bg-border'
        )} />
        <CardHeader className="pb-2 pt-4">
          <div className="flex items-start justify-between gap-2">
            <div className="min-w-0 flex-1">
              <button
                className="text-left w-full"
                onClick={() => navigate(`/projects/${projectId}/services/${service.id}`)}
              >
                <CardTitle className="text-sm font-semibold truncate hover:text-primary transition-colors">
                  {service.name}
                </CardTitle>
              </button>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                <ServiceStatusBadge status={service.status} />
                {service.framework && (
                  <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                    {service.framework}
                  </Badge>
                )}
                <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                  {service.deploy_type}
                </Badge>
              </div>
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" />}>
                <Settings2 className="h-3.5 w-3.5" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}`)}>
                  <Terminal className="mr-2 h-3.5 w-3.5" /> View service
                </DropdownMenuItem>
                {canEdit && (
                  <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/env`)}>
                    <Settings2 className="mr-2 h-3.5 w-3.5" /> Environment
                  </DropdownMenuItem>
                )}
                {canEdit && (
                  <DropdownMenuItem onClick={() => navigate(`/projects/${projectId}/services/${service.id}/domains`)}>
                    <Globe className="mr-2 h-3.5 w-3.5" /> Domains
                  </DropdownMenuItem>
                )}
                {canEdit && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      className="text-destructive focus:text-destructive"
                      onClick={() => setConfirmDelete(true)}
                    >
                      <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete service
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </CardHeader>

        <CardContent className="flex flex-col gap-3 flex-1 pb-4">
          {service.repo_url && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground overflow-hidden">
              <GitBranch className="h-3 w-3 shrink-0" />
              <span className="truncate font-mono text-[11px]">{repoShort(service.repo_url)}</span>
              {service.repo_branch && (
                <span className="shrink-0 text-muted-foreground">@ {service.repo_branch}</span>
              )}
            </div>
          )}

          {service.host_port ? (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <ExternalLink className="h-3 w-3 shrink-0" />
              <span>Port <span className="font-mono text-foreground">{service.host_port}</span></span>
            </div>
          ) : null}

          <div className="mt-auto flex gap-2">
            {canEdit && (
              <Button
                size="sm"
                className="flex-1 gap-1.5 text-xs h-8"
                onClick={() => deployMutation.mutate()}
                disabled={deployMutation.isPending || isDeploying}
              >
                {isDeploying ? (
                  <span className="flex items-center gap-1.5">
                    <span className="h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
                    Deploying…
                  </span>
                ) : (
                  <><Rocket className="h-3 w-3" /> Deploy</>
                )}
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              className="text-xs h-8 px-3"
              onClick={() => navigate(`/projects/${projectId}/services/${service.id}/deployments`)}
            >
              History
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Confirm delete dialog */}
      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete service
            </DialogTitle>
            <DialogDescription>
              This will permanently delete <strong>{service.name}</strong>, stop its container,
              remove all images, domains, deployments and environment variables.
              This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="ghost" size="sm" onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button
              variant="destructive" size="sm"
              disabled={deleteMutation.isPending}
              onClick={() => { setConfirmDelete(false); deleteMutation.mutate() }}
            >
              {deleteMutation.isPending ? 'Deleting…' : 'Delete service'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function repoShort(url: string) {
  try {
    // https://github.com/user/repo.git → user/repo
    const m = url.match(/[:/]([^:/]+\/[^/]+?)(\.git)?$/)
    return m ? m[1] : url
  } catch {
    return url
  }
}

export function ProjectPage() {
  const { projectId } = useParams<{ projectId: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [newServiceOpen, setNewServiceOpen] = useState(false)
  const [confirmDeleteProject, setConfirmDeleteProject] = useState(false)
  const [membersOpen, setMembersOpen] = useState(false)
  const [addMemberEmail, setAddMemberEmail] = useState('')
  const [addMemberEmailDisplay, setAddMemberEmailDisplay] = useState('')
  const [addMemberSearchQ, setAddMemberSearchQ] = useState('')
  const [addMemberDropdownOpen, setAddMemberDropdownOpen] = useState(false)
  const [addMemberRole, setAddMemberRole] = useState<'owner' | 'editor' | 'viewer'>('editor')
  const searchRef = useRef<HTMLDivElement>(null)

  const { data: project, isLoading: projLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => projectsApi.get(projectId!),
    enabled: !!projectId,
  })

  const isOwner = project?.my_role === 'owner'

  const { data: members } = useQuery({
    queryKey: ['project-members', projectId],
    queryFn: () => projectsApi.listMembers(projectId!),
    enabled: !!projectId && isOwner,
  })

  const { data: userSearchResults } = useQuery({
    queryKey: ['users-search', addMemberSearchQ],
    queryFn: () => usersApi.search(addMemberSearchQ),
    enabled: membersOpen && addMemberSearchQ.length >= 1,
    staleTime: 10_000,
  })

  const addMemberMutation = useMutation({
    mutationFn: async () => {
      const user = await usersApi.lookup(addMemberEmail.trim())
      return projectsApi.addMember(projectId!, user.id, addMemberRole)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      setAddMemberEmail('')
      setAddMemberEmailDisplay('')
      setAddMemberSearchQ('')
      toast.success('Member added.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to add member.'),
  })

  const updateMemberMutation = useMutation({
    mutationFn: ({ userId, role }: { userId: number; role: 'owner' | 'editor' | 'viewer' }) =>
      projectsApi.updateMember(projectId!, userId, role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      toast.success('Role updated.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to update role.'),
  })

  const removeMemberMutation = useMutation({
    mutationFn: (userId: number) => projectsApi.removeMember(projectId!, userId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-members', projectId] })
      toast.success('Member removed.')
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to remove member.'),
  })

  const { data: services, isLoading: svcLoading } = useQuery({
    queryKey: ['services', projectId],
    queryFn: () => servicesApi.list(projectId!),
    enabled: !!projectId,
    refetchInterval: 5000,
  })

  const [newSvcName, setNewSvcName] = useState('')

  const createSvcMutation = useMutation({
    mutationFn: () => servicesApi.create(projectId!, { name: newSvcName }),
    onSuccess: (service) => {
      qc.invalidateQueries({ queryKey: ['services', projectId] })
      setNewServiceOpen(false)
      setNewSvcName('')
      toast.success('Service created — configure your source in the Settings tab.')
      navigate(`/projects/${projectId}/services/${service.id}`)
    },
    onError: (err: unknown) => toast.error((err as any)?.response?.data?.error ?? 'Failed to create service.'),
  })

  const deleteProjectMutation = useMutation({
    mutationFn: () => projectsApi.delete(projectId!),
    onSuccess: () => {
      navigate('/dashboard')
      toast.success('Project deleted.')
    },
    onError: (err: unknown) => {
      setConfirmDeleteProject(false)
      toast.error((err as any)?.response?.data?.error ?? 'Failed to delete project.')
    },
  })

  const hasServices = (services?.length ?? 0) > 0

  return (
    <div className="space-y-6 pb-8">
      <Button
        variant="ghost"
        size="sm"
        className="gap-1.5 text-muted-foreground"
        onClick={() => navigate('/dashboard')}
      >
        <ChevronLeft className="h-3.5 w-3.5" /> All projects
      </Button>

      {projLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-72" />
        </div>
      ) : (
        <div className="flex items-start justify-between gap-4 flex-wrap">
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-2xl font-bold tracking-tight">{project?.name}</h1>
              {project?.my_role && (
                <Badge variant={project.my_role === 'owner' ? 'default' : 'secondary'} className="text-xs capitalize">
                  {project.my_role}
                </Badge>
              )}
            </div>
            {project?.description && (
              <p className="mt-1 text-sm text-muted-foreground">{project.description}</p>
            )}
            <p className="mt-1 text-xs text-muted-foreground">
              {services?.length ?? 0} service{(services?.length ?? 0) !== 1 ? 's' : ''}
            </p>
          </div>
          <div className="flex gap-2">
            {isOwner && (
              <Button
                size="sm"
                variant="outline"
                className="gap-1.5 text-destructive hover:text-destructive hover:bg-destructive/10"
                onClick={() => setConfirmDeleteProject(true)}
              >
                <Trash2 className="h-3.5 w-3.5" /> Delete project
              </Button>
            )}
            {(project?.my_role === 'owner' || project?.my_role === 'editor') && (
              <Button size="sm" className="gap-1.5" onClick={() => setNewServiceOpen(true)}>
                <Plus className="h-4 w-4" /> New service
              </Button>
            )}
          </div>
        </div>
      )}

      <Separator />

      {/* Services grid */}
      {svcLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[...Array(3)].map((_, i) => (
            <Card key={i} className="h-36">
              <CardHeader><Skeleton className="h-4 w-24" /></CardHeader>
              <CardContent><Skeleton className="h-8 w-full" /></CardContent>
            </Card>
          ))}
        </div>
      ) : services?.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-24 text-center">
          <div className="mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-muted">
            <Rocket className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-base font-medium">No services yet</p>
          <p className="mt-1 text-sm text-muted-foreground max-w-xs">
            Create a service to start deploying your applications as containers.
          </p>
          <Button size="sm" className="mt-5 gap-1.5" onClick={() => setNewServiceOpen(true)}>
            <Plus className="h-4 w-4" /> Create first service
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {services?.map((s) => (
            <ServiceCard key={s.id} service={s} projectId={projectId!} canEdit={project?.my_role === 'owner' || project?.my_role === 'editor'} />
          ))}
        </div>
      )}

      {/* ── Project members (visible to owners only) ───────────────────── */}
      {isOwner && (
        <div className="mt-6">
          <Separator className="mb-6" />
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-base font-semibold flex items-center gap-2">
              <Users className="h-4 w-4 text-muted-foreground" />
              Project members
              {members && <Badge variant="secondary" className="ml-1">{members.length}</Badge>}
            </h2>
            <Button size="sm" variant="outline" className="gap-1.5 text-xs" onClick={() => setMembersOpen(true)}>
              <Plus className="h-3.5 w-3.5" /> Add member
            </Button>
          </div>

          <div className="rounded-lg border divide-y">
            {members?.map((m: ProjectMember) => (
              <div key={m.user_id} className="flex items-center justify-between gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium truncate">{m.name || m.email}</p>
                  <p className="text-xs text-muted-foreground truncate">{m.email}</p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <Select
                    value={m.role}
                    onValueChange={(role) =>
                      updateMemberMutation.mutate({ userId: m.user_id, role: role as 'owner' | 'editor' | 'viewer' })
                    }
                  >
                    <SelectTrigger className="h-7 w-24 text-xs">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="owner">Owner</SelectItem>
                      <SelectItem value="editor">Editor</SelectItem>
                      <SelectItem value="viewer">Viewer</SelectItem>
                    </SelectContent>
                  </Select>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    disabled={removeMemberMutation.isPending}
                    onClick={() => removeMemberMutation.mutate(m.user_id)}
                  >
                    <UserMinus className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
            ))}
            {members?.length === 0 && (
              <div className="px-4 py-6 text-center text-sm text-muted-foreground">No members yet.</div>
            )}
          </div>
        </div>
      )}

      {/* ── Add member dialog ──────────────────────────────────────────── */}
      <Dialog open={membersOpen} onOpenChange={(o) => {
        setMembersOpen(o)
        if (!o) {
          setAddMemberEmail('')
          setAddMemberEmailDisplay('')
          setAddMemberSearchQ('')
          setAddMemberDropdownOpen(false)
        }
      }}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Users className="h-4 w-4 text-primary" /> Add project member
            </DialogTitle>
            <DialogDescription>
              Search for a registered user and choose their role.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <div>
              <Label htmlFor="member-email">User</Label>
              <div className="relative mt-1.5" ref={searchRef}>
                <Input
                  id="member-email"
                  type="text"
                  placeholder="Search by name or email…"
                  autoComplete="off"
                  value={addMemberEmailDisplay || addMemberSearchQ}
                  onChange={e => {
                    const v = e.target.value
                    setAddMemberEmailDisplay('')
                    setAddMemberEmail('')
                    setAddMemberSearchQ(v)
                    setAddMemberDropdownOpen(true)
                  }}
                  onFocus={() => {
                    if (!addMemberEmail) setAddMemberDropdownOpen(true)
                  }}
                  onBlur={(e) => {
                    // delay so click on list item fires first
                    if (!searchRef.current?.contains(e.relatedTarget as Node)) {
                      setTimeout(() => setAddMemberDropdownOpen(false), 150)
                    }
                  }}
                />
                {addMemberDropdownOpen && userSearchResults && userSearchResults.length > 0 && (
                  <div className="absolute z-50 mt-1 w-full rounded-md border border-border/60 bg-popover shadow-lg">
                    <ul className="max-h-44 overflow-y-auto py-1 text-sm">
                      {userSearchResults.map(u => (
                        <li
                          key={u.id}
                          tabIndex={0}
                          className="flex flex-col px-3 py-2 cursor-pointer hover:bg-accent focus:bg-accent outline-none"
                          onMouseDown={(e) => {
                            // prevent blur firing before click
                            e.preventDefault()
                            setAddMemberEmail(u.email)
                            setAddMemberEmailDisplay(u.name ? `${u.name} (${u.email})` : u.email)
                            setAddMemberSearchQ('')
                            setAddMemberDropdownOpen(false)
                          }}
                        >
                          <span className="font-medium truncate">{u.name || u.email}</span>
                          {u.name && <span className="text-xs text-muted-foreground truncate">{u.email}</span>}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {addMemberDropdownOpen && addMemberSearchQ.length >= 1 && (!userSearchResults || userSearchResults.length === 0) && (
                  <div className="absolute z-50 mt-1 w-full rounded-md border border-border/60 bg-popover shadow-sm">
                    <p className="px-3 py-2 text-xs text-muted-foreground">No users found</p>
                  </div>
                )}
              </div>
            </div>
            <div>
              <Label htmlFor="member-role">Role</Label>
              <Select value={addMemberRole} onValueChange={(v) => setAddMemberRole(v as 'owner' | 'editor' | 'viewer')}>
                <SelectTrigger id="member-role" className="mt-1.5">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="owner">Owner — full control</SelectItem>
                  <SelectItem value="editor">Editor — deploy &amp; configure</SelectItem>
                  <SelectItem value="viewer">Viewer — read-only</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => {
              setMembersOpen(false)
              setAddMemberEmail('')
              setAddMemberEmailDisplay('')
              setAddMemberSearchQ('')
              setAddMemberDropdownOpen(false)
            }}>Cancel</Button>
            <Button
              size="sm"
              disabled={!addMemberEmail || addMemberMutation.isPending}
              onClick={() => addMemberMutation.mutate()}
            >
              {addMemberMutation.isPending
                ? <><Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />Adding…</>
                : 'Add member'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── New service dialog ─────────────────────────────────────────── */}
      <Dialog open={newServiceOpen} onOpenChange={(o) => { setNewServiceOpen(o); if (!o) setNewSvcName('') }}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Rocket className="h-4 w-4 text-primary" /> New service
            </DialogTitle>
            <DialogDescription>
              Give your service a name. You can connect a repo and configure settings after creation.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-2">
            <Label htmlFor="svc-name">Service name</Label>
            <Input
              id="svc-name"
              placeholder="e.g. web, api, worker"
              autoFocus
              value={newSvcName}
              onChange={e => setNewSvcName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
              onKeyDown={e => e.key === 'Enter' && newSvcName.length >= 2 && createSvcMutation.mutate()}
            />
            <p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens only.</p>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setNewServiceOpen(false)}>Cancel</Button>
            <Button
              size="sm"
              disabled={newSvcName.length < 2 || createSvcMutation.isPending}
              onClick={() => createSvcMutation.mutate()}
            >
              {createSvcMutation.isPending ? 'Creating…' : 'Create service'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Confirm delete project ─────────────────────────────────────── */}
      <Dialog open={confirmDeleteProject} onOpenChange={setConfirmDeleteProject}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              Delete project
            </DialogTitle>
            <DialogDescription>
              {hasServices ? (
                <span className="text-destructive font-medium">
                  This project has {services?.length} active service{services?.length !== 1 ? 's' : ''}.
                  Delete all services first to clean up their containers before deleting the project.
                </span>
              ) : (
                <>Permanently delete <strong>{project?.name}</strong>? This cannot be undone.</>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="ghost" size="sm" onClick={() => setConfirmDeleteProject(false)}>Cancel</Button>
            <Button
              variant="destructive" size="sm"
              disabled={hasServices || deleteProjectMutation.isPending}
              onClick={() => deleteProjectMutation.mutate()}
            >
              {deleteProjectMutation.isPending ? 'Deleting…' : 'Delete project'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
