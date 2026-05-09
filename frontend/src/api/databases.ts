import client from './client'

export type DatabaseType = 'postgres' | 'mysql' | 'sqlite'
export type DatabaseStatus = 'stopped' | 'starting' | 'running' | 'error'

export interface DatabaseRecord {
  id: number
  project_id: number
  name: string
  db_type: DatabaseType
  db_version: string
  db_name: string
  db_user: string
  host_port?: number
  status: DatabaseStatus
  container_id?: string
  network_public: boolean
  last_error?: string
  node_id: string
  target_node_id: string
  created_at: string
  updated_at: string
  connection_url?: string
  public_connection_url?: string
  env_var_name?: string
}

export interface CreateDatabasePayload {
  name: string
  db_type: DatabaseType
  db_version?: string
  db_name?: string
  db_user?: string
  db_password?: string
  target_node_id?: string
}

export interface UpdateDatabasePayload {
  db_version?: string
  target_node_id?: string
}

export interface DatabaseLogsResponse {
  container: string
  logs: string
  start_log: string
}

export interface DatabaseBackupDownload {
  blob: Blob
  filename: string
}

function parseFilename(contentDisposition?: string) {
  const match = contentDisposition?.match(/filename="?([^";]+)"?/i)
  return match?.[1] ?? 'database-backup.tar'
}

export const databasesApi = {
  list: (projectId: string | number): Promise<DatabaseRecord[]> =>
    client.get<DatabaseRecord[]>(`/projects/${projectId}/databases`).then((r) => r.data),

  get: (projectId: string | number, databaseId: string | number): Promise<DatabaseRecord> =>
    client.get<DatabaseRecord>(`/projects/${projectId}/databases/${databaseId}`).then((r) => r.data),

  create: (projectId: string | number, data: CreateDatabasePayload): Promise<DatabaseRecord> =>
    client.post<DatabaseRecord>(`/projects/${projectId}/databases`, data).then((r) => r.data),

  start: (projectId: string | number, databaseId: string | number): Promise<{ status: string }> =>
    client.post<{ status: string }>(`/projects/${projectId}/databases/${databaseId}/start`).then((r) => r.data),

  restart: (projectId: string | number, databaseId: string | number): Promise<{ status: string }> =>
    client.post<{ status: string }>(`/projects/${projectId}/databases/${databaseId}/restart`).then((r) => r.data),

  stop: (projectId: string | number, databaseId: string | number): Promise<{ status: string }> =>
    client.post<{ status: string }>(`/projects/${projectId}/databases/${databaseId}/stop`).then((r) => r.data),

  delete: (projectId: string | number, databaseId: string | number): Promise<void> =>
    client.delete(`/projects/${projectId}/databases/${databaseId}`).then(() => undefined),

  update: (
    projectId: string | number,
    databaseId: string | number,
    data: UpdateDatabasePayload,
  ): Promise<DatabaseRecord> =>
    client.put<DatabaseRecord>(`/projects/${projectId}/databases/${databaseId}`, data).then((r) => r.data),

  getLogs: (
    projectId: string | number,
    databaseId: string | number,
  ): Promise<DatabaseLogsResponse> =>
    client.get<DatabaseLogsResponse>(`/projects/${projectId}/databases/${databaseId}/logs`).then((r) => r.data),

  downloadBackup: async (
    projectId: string | number,
    databaseId: string | number,
  ): Promise<DatabaseBackupDownload> => {
    const response = await client.get<Blob>(
      `/projects/${projectId}/databases/${databaseId}/backup`,
      { responseType: 'blob' },
    )

    return {
      blob: response.data,
      filename: parseFilename(response.headers['content-disposition']),
    }
  },

  togglePublic: (
    projectId: string | number,
    databaseId: string | number,
    enable: boolean,
  ): Promise<{ ok: boolean; network_public: boolean; public_connection_url?: string }> =>
    client
      .post<{ ok: boolean; network_public: boolean; public_connection_url?: string }>(
        `/projects/${projectId}/databases/${databaseId}/public`,
        { public: enable },
      )
      .then((r) => r.data),

  restoreBackup: (
    projectId: string | number,
    databaseId: string | number,
    file: File,
  ): Promise<{ status: string }> => {
    const form = new FormData()
    form.append('file', file)
    return client
      .post<{ status: string }>(`/projects/${projectId}/databases/${databaseId}/restore`, form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      })
      .then((r) => r.data)
  },
}
