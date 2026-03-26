import { useState, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Search, Github, ChevronRight, ArrowLeft, FolderOpen,
  Check, Lock, Globe, Loader2, Link2, Link2Off, RefreshCw, Home,
} from 'lucide-react'
import { githubApi, type GitHubRepo } from '@/api/github'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

export interface RepoSelection {
  repo_url: string
  repo_branch: string
  repo_folder: string
}

interface Props {
  value: RepoSelection
  onChange: (value: RepoSelection) => void
}

// ─── Main component ───────────────────────────────────────────────────────────

export function GitHubRepoSelector({ value, onChange }: Props) {
  const [tab, setTab] = useState<'github' | 'manual'>('github')

  // GitHub browser state
  const [selectedRepo, setSelectedRepo] = useState<GitHubRepo | null>(null)
  const [repoSearch, setRepoSearch] = useState('')
  const [folderPath, setFolderPath] = useState<string[]>([])

  const { data: status, isLoading: statusLoading } = useQuery({
    queryKey: ['github-status'],
    queryFn: githubApi.status,
  })

  const { data: repos, isLoading: reposLoading, refetch: refetchRepos } = useQuery({
    queryKey: ['github-repos'],
    queryFn: githubApi.listRepos,
    enabled: status?.connected === true,
    staleTime: 60_000,
  })

  const repoParts = selectedRepo ? selectedRepo.full_name.split('/') : []
  const repoOwner = repoParts[0] ?? ''
  const repoName = repoParts[1] ?? ''
  const currentPathStr = folderPath.join('/')

  const { data: branches, isLoading: branchesLoading } = useQuery({
    queryKey: ['github-branches', selectedRepo?.full_name],
    queryFn: () => githubApi.listBranches(repoOwner, repoName),
    enabled: !!selectedRepo,
    staleTime: 30_000,
  })

  const { data: treeData, isLoading: treeLoading } = useQuery({
    queryKey: ['github-tree', selectedRepo?.full_name, value.repo_branch, currentPathStr],
    queryFn: () => githubApi.getTree(repoOwner, repoName, value.repo_branch, currentPathStr),
    enabled: !!selectedRepo && !!value.repo_branch,
    staleTime: 30_000,
  })

  // ── Handlers ─────────────────────────────────────────────────────────────

  const connectGitHub = async () => {
    try {
      const { url } = await githubApi.getAuthURL()
      window.location.href = url
    } catch {/* handled by error state */ }
  }

  const selectRepo = (repo: GitHubRepo) => {
    setSelectedRepo(repo)
    setFolderPath([])
    onChange({ repo_url: repo.clone_url, repo_branch: repo.default_branch, repo_folder: '' })
  }

  const clearRepo = () => {
    setSelectedRepo(null)
    setFolderPath([])
    onChange({ repo_url: '', repo_branch: 'main', repo_folder: '' })
  }

  const selectBranch = (branch: string | null) => {
    if (!branch) return
    setFolderPath([]) // reset folder when branch changes
    onChange({ ...value, repo_branch: branch, repo_folder: '' })
  }

  const enterFolder = useCallback((folderName: string) => {
    const newPath = [...folderPath, folderName]
    setFolderPath(newPath)
    onChange({ ...value, repo_folder: newPath.join('/') })
  }, [folderPath, value, onChange])

  const navigateBreadcrumb = useCallback((index: number) => {
    const newPath = folderPath.slice(0, index)
    setFolderPath(newPath)
    onChange({ ...value, repo_folder: newPath.join('/') })
  }, [folderPath, value, onChange])

  const filteredRepos = (repos ?? []).filter(r =>
    r.full_name.toLowerCase().includes(repoSearch.toLowerCase()) ||
    (r.description ?? '').toLowerCase().includes(repoSearch.toLowerCase()),
  )

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as 'github' | 'manual')}>
      <TabsList className="w-full h-8">
        <TabsTrigger value="github" className="flex-1 gap-1.5 text-xs">
          <Github className="h-3.5 w-3.5" /> Browse GitHub
        </TabsTrigger>
        <TabsTrigger value="manual" className="flex-1 gap-1.5 text-xs">
          <Link2 className="h-3.5 w-3.5" /> Manual URL
        </TabsTrigger>
      </TabsList>

      {/* ── GitHub browser tab ─────────────────────────────────────────────── */}
      <TabsContent value="github" className="mt-0">
        <div className="rounded-b-lg border border-t-0 bg-muted/20 overflow-hidden">

          {/* Not connected */}
          {statusLoading ? (
            <div className="flex items-center gap-2 p-4">
              <Skeleton className="h-4 w-4 rounded-full" />
              <Skeleton className="h-4 w-40" />
            </div>
          ) : !status?.connected ? (
            <div className="flex flex-col items-center gap-3 px-4 py-6 text-center">
              <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
                <Github className="h-5 w-5 text-muted-foreground" />
              </div>
              {!status?.configured ? (
                <>
                  <p className="text-sm font-medium">GitHub not configured</p>
                  <p className="text-xs text-muted-foreground max-w-xs">
                    A superadmin needs to set <code>GITHUB_CLIENT_ID</code> and{' '}
                    <code>GITHUB_CLIENT_SECRET</code> in the server configuration.
                  </p>
                </>
              ) : (
                <>
                  <p className="text-sm font-medium">Connect your GitHub account</p>
                  <p className="text-xs text-muted-foreground max-w-xs">
                    Connect once to browse all your repositories and pick a branch and folder.
                  </p>
                  <Button size="sm" className="gap-1.5 mt-1" onClick={connectGitHub}>
                    <Github className="h-3.5 w-3.5" /> Connect GitHub
                  </Button>
                </>
              )}
            </div>
          ) : !selectedRepo ? (
            /* Repo list */
            <div>
              <div className="flex items-center gap-2 border-b px-3 py-2">
                <div className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
                  <Github className="h-3.5 w-3.5" />
                  <span className="font-medium">@{status.github_login}</span>
                </div>
                <button
                  type="button"
                  className="ml-auto flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground transition-colors"
                  onClick={() => refetchRepos()}
                >
                  <RefreshCw className="h-3 w-3" /> Refresh
                </button>
              </div>
              <div className="relative px-3 pt-2 pb-1">
                <Search className="absolute left-5 top-3.5 h-3.5 w-3.5 text-muted-foreground" />
                <Input
                  className="pl-8 h-8 text-xs"
                  placeholder="Search repositories…"
                  value={repoSearch}
                  onChange={(e) => setRepoSearch(e.target.value)}
                />
              </div>
              <div className="max-h-52 overflow-y-auto divide-y">
                {reposLoading ? (
                  [...Array(4)].map((_, i) => (
                    <div key={i} className="flex items-center gap-2 px-3 py-2">
                      <Skeleton className="h-4 w-4 rounded" />
                      <Skeleton className="h-4 w-48" />
                    </div>
                  ))
                ) : filteredRepos.length === 0 ? (
                  <p className="px-3 py-4 text-center text-xs text-muted-foreground">
                    {repoSearch ? 'No repositories match your search.' : 'No repositories found.'}
                  </p>
                ) : (
                  filteredRepos.map((repo) => (
                    <button
                      key={repo.id}
                      type="button"
                      className="flex w-full items-start gap-2.5 px-3 py-2.5 hover:bg-muted/60 transition-colors text-left"
                      onClick={() => selectRepo(repo)}
                    >
                      {repo.private
                        ? <Lock className="h-3.5 w-3.5 mt-0.5 shrink-0 text-amber-500" />
                        : <Globe className="h-3.5 w-3.5 mt-0.5 shrink-0 text-muted-foreground" />}
                      <div className="min-w-0 flex-1">
                        <p className="text-sm font-medium truncate">{repo.full_name}</p>
                        {repo.description && (
                          <p className="text-[11px] text-muted-foreground truncate mt-0.5">{repo.description}</p>
                        )}
                      </div>
                      {repo.language && (
                        <Badge variant="outline" className="text-[10px] shrink-0">{repo.language}</Badge>
                      )}
                      <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                    </button>
                  ))
                )}
              </div>
            </div>
          ) : (
            /* Repo selected — branch + folder picker */
            <div>
              {/* Selected repo header */}
              <div className="flex items-center gap-2 border-b px-3 py-2">
                <button
                  type="button"
                  className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
                  onClick={clearRepo}
                >
                  <ArrowLeft className="h-3.5 w-3.5" /> Back
                </button>
                <div className="flex items-center gap-1.5 ml-1">
                  {selectedRepo.private
                    ? <Lock className="h-3 w-3 text-amber-500" />
                    : <Globe className="h-3 w-3 text-muted-foreground" />}
                  <span className="text-xs font-medium">{selectedRepo.full_name}</span>
                </div>
                <Check className="h-3.5 w-3.5 text-emerald-500 ml-auto" />
              </div>

              <div className="px-3 py-3 space-y-3">
                {/* Branch selector */}
                <div className="space-y-1.5">
                  <Label className="text-xs">Branch</Label>
                  {branchesLoading ? (
                    <Skeleton className="h-8 w-full" />
                  ) : (
                    <Select value={value.repo_branch} onValueChange={selectBranch}>
                      <SelectTrigger className="h-8 text-xs font-mono">
                        <SelectValue placeholder="Select branch…" />
                      </SelectTrigger>
                      <SelectContent>
                        {(branches ?? []).map((b) => (
                          <SelectItem key={b.name} value={b.name} className="text-xs font-mono">
                            {b.name}
                            {b.name === selectedRepo.default_branch && (
                              <span className="ml-1.5 text-[10px] text-muted-foreground">(default)</span>
                            )}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </div>

                {/* Folder picker */}
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between">
                    <Label className="text-xs">
                      Deploy folder{' '}
                      <span className="font-normal text-muted-foreground">(optional)</span>
                    </Label>
                    {value.repo_folder && (
                      <button
                        type="button"
                        className="text-[10px] text-muted-foreground hover:text-foreground"
                        onClick={() => navigateBreadcrumb(0)}
                      >
                        Reset to root
                      </button>
                    )}
                  </div>

                  {/* Breadcrumb */}
                  <div className="flex items-center gap-1 text-xs font-mono text-muted-foreground flex-wrap">
                    <button
                      type="button"
                      className="flex items-center gap-1 hover:text-foreground transition-colors"
                      onClick={() => navigateBreadcrumb(0)}
                    >
                      <Home className="h-3 w-3" />
                      <span>root</span>
                    </button>
                    {folderPath.map((seg, i) => (
                      <span key={i} className="flex items-center gap-1">
                        <ChevronRight className="h-3 w-3" />
                        <button
                          type="button"
                          className={`hover:text-foreground transition-colors ${i === folderPath.length - 1 ? 'text-foreground font-medium' : ''}`}
                          onClick={() => navigateBreadcrumb(i + 1)}
                        >
                          {seg}
                        </button>
                      </span>
                    ))}
                  </div>

                  {/* Folder list */}
                  <div className="rounded-md border max-h-36 overflow-y-auto">
                    {treeLoading ? (
                      <div className="flex items-center justify-center py-4">
                        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                      </div>
                    ) : !treeData?.entries.length ? (
                      <p className="py-3 text-center text-xs text-muted-foreground">
                        {folderPath.length === 0 ? 'No subdirectories — deploy from root.' : 'No subdirectories here.'}
                      </p>
                    ) : (
                      treeData.entries.map((entry) => (
                        <button
                          key={entry.path}
                          type="button"
                          className="flex w-full items-center gap-2 px-3 py-1.5 hover:bg-muted/60 transition-colors text-left"
                          onClick={() => enterFolder(entry.name)}
                        >
                          <FolderOpen className="h-3.5 w-3.5 shrink-0 text-amber-500" />
                          <span className="text-xs font-mono">{entry.name}</span>
                          <ChevronRight className="h-3 w-3 ml-auto text-muted-foreground" />
                        </button>
                      ))
                    )}
                  </div>

                  {value.repo_folder && (
                    <p className="text-[11px] text-muted-foreground">
                      Deploying from: <code className="font-mono text-foreground">/{value.repo_folder}</code>
                    </p>
                  )}
                </div>
              </div>
            </div>
          )}
        </div>
      </TabsContent>

      {/* ── Manual URL tab ─────────────────────────────────────────────────── */}
      <TabsContent value="manual" className="mt-0">
        <div className="rounded-b-lg border border-t-0 bg-muted/20 px-3 py-3 space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="manual-repo-url" className="text-xs">Repository URL</Label>
            <Input
              id="manual-repo-url"
              className="h-8 font-mono text-xs"
              placeholder="https://github.com/you/repo  or  git@github.com:you/repo.git"
              value={value.repo_url}
              onChange={(e) => onChange({ ...value, repo_url: e.target.value })}
            />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div className="space-y-1.5">
              <Label htmlFor="manual-branch" className="text-xs">Branch</Label>
              <Input
                id="manual-branch"
                className="h-8 font-mono text-xs"
                placeholder="main"
                value={value.repo_branch}
                onChange={(e) => onChange({ ...value, repo_branch: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="manual-folder" className="text-xs">
                Deploy folder <span className="text-muted-foreground">(optional)</span>
              </Label>
              <Input
                id="manual-folder"
                className="h-8 font-mono text-xs"
                placeholder="apps/web"
                value={value.repo_folder}
                onChange={(e) => onChange({ ...value, repo_folder: e.target.value })}
              />
            </div>
          </div>
          <p className="text-[11px] text-muted-foreground flex items-start gap-1">
            <Link2Off className="h-3 w-3 mt-0.5 shrink-0" />
            For private repos, add an SSH key in <strong className="text-foreground">Settings → SSH Keys</strong> and use an SSH URL.
          </p>
        </div>
      </TabsContent>
    </Tabs>
  )
}
