import client from './client'

export interface GitHubStatus {
  connected: boolean
  github_login: string
  configured: boolean
}

export interface GitHubRepo {
  id: number
  name: string
  full_name: string
  description: string
  private: boolean
  html_url: string
  clone_url: string
  ssh_url: string
  default_branch: string
  language: string
  updated_at: string
}

export interface GitHubBranch {
  name: string
  commit: { sha: string }
}

export interface GitHubTreeEntry {
  name: string
  path: string
}

export const githubApi = {
  status: (): Promise<GitHubStatus> =>
    client.get<GitHubStatus>('/github/status').then((r) => r.data),

  getAuthURL: (): Promise<{ url: string }> =>
    client.get<{ url: string }>('/github/auth').then((r) => r.data),

  disconnect: (): Promise<void> =>
    client.delete('/github/disconnect').then(() => undefined),

  listRepos: (): Promise<GitHubRepo[]> =>
    client.get<GitHubRepo[]>('/github/repos').then((r) => r.data),

  listBranches: (owner: string, repo: string): Promise<GitHubBranch[]> =>
    client.get<GitHubBranch[]>(`/github/repos/${owner}/${repo}/branches`).then((r) => r.data),

  getTree: (owner: string, repo: string, ref: string, path = ''): Promise<{ entries: GitHubTreeEntry[] }> =>
    client
      .get<{ entries: GitHubTreeEntry[] }>(
        `/github/repos/${owner}/${repo}/tree?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`,
      )
      .then((r) => r.data),
}
