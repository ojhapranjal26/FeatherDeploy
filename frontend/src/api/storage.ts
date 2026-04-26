import client from './client'

// ── Types ─────────────────────────────────────────────────────────────────

export interface Storage {
  id: number
  name: string
  description: string
  size_bytes: number
  created_by: number
  created_at: string
  updated_at: string
  access_count: number
}

export interface StorageAccess {
  id: number
  storage_id: number
  service_id: number
  service_name: string
  can_read: boolean
  can_write: boolean
  service_key_preview: string
  granted_at: string
}

export interface GrantAccessResult {
  service_key: string
  service_key_preview: string
  can_read: boolean
  can_write: boolean
}

export interface ObjectEntry {
  path: string
  size: number
  updated_at: string
}

export interface BandwidthEntry {
  service_id: number
  service_name: string
  period: string
  bytes_read: number
  bytes_written: number
}

export interface StorageStats {
  id: number
  name: string
  size_bytes: number
  object_count: number
  bandwidth: BandwidthEntry[]
}

export interface CreateStoragePayload {
  name: string
  description?: string
}

export interface GrantAccessPayload {
  service_id: number
  can_read?: boolean
  can_write?: boolean
}

// ── API calls ─────────────────────────────────────────────────────────────

export const storageApi = {
  /** List all storages (admin/superadmin). */
  list: (): Promise<Storage[]> =>
    client.get('/storages').then((r) => r.data),

  /** Get a single storage by ID. */
  get: (id: number): Promise<Storage> =>
    client.get(`/storages/${id}`).then((r) => r.data),

  /** Create a storage bucket. */
  create: (payload: CreateStoragePayload): Promise<Storage> =>
    client.post('/storages', payload).then((r) => r.data),

  /** Permanently delete a storage and all its files. */
  delete: (id: number): Promise<void> =>
    client.delete(`/storages/${id}`).then(() => undefined),

  // ── Access management ────────────────────────────────────────────────────

  /** List services that have access to a storage. */
  listAccess: (id: number): Promise<StorageAccess[]> =>
    client.get(`/storages/${id}/access`).then((r) => r.data),

  /**
   * Grant a service access to a storage.
   * Returns the one-time plaintext service_key — cannot be recovered later.
   */
  grantAccess: (id: number, payload: GrantAccessPayload): Promise<GrantAccessResult> =>
    client.post(`/storages/${id}/access`, payload).then((r) => r.data),

  /** Update read/write permissions for a service. */
  updateAccess: (storageId: number, serviceId: number, payload: { can_read?: boolean; can_write?: boolean }): Promise<void> =>
    client.patch(`/storages/${storageId}/access/${serviceId}`, payload).then(() => undefined),

  /** Revoke a service's access to a storage. */
  revokeAccess: (storageId: number, serviceId: number): Promise<void> =>
    client.delete(`/storages/${storageId}/access/${serviceId}`).then(() => undefined),

  /**
   * Rotate the per-service API key.
   * Returns the new plaintext key ONCE.
   */
  rotateServiceKey: (storageId: number, serviceId: number): Promise<{ service_key: string; service_key_preview: string }> =>
    client.post(`/storages/${storageId}/access/${serviceId}/rotate-key`).then((r) => r.data),

  // ── Admin file browser ───────────────────────────────────────────────────

  /** List objects in a storage bucket with optional prefix. */
  browse: (id: number, prefix?: string): Promise<ObjectEntry[]> =>
    client.get(`/storages/${id}/browse`, { params: prefix ? { prefix } : undefined }).then((r) => r.data),

  /** Delete an object (admin action). */
  adminDeleteObject: (id: number, path: string): Promise<void> =>
    client.delete(`/storages/${id}/objects`, { params: { path } }).then(() => undefined),

  /** Get storage statistics (size, object count, bandwidth per service). */
  stats: (id: number): Promise<StorageStats> =>
    client.get(`/storages/${id}/stats`).then((r) => r.data),
}

