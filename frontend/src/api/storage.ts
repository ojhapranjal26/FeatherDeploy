import client from './client'

// ── Types ─────────────────────────────────────────────────────────────────

export interface Storage {
  id: number
  name: string
  description: string
  api_key_preview: string
  created_by: number
  created_at: string
  updated_at: string
  key_count: number
  access_count: number
}

/** Returned only on creation or key rotation — api_key is a one-time value */
export interface StorageCreated extends Storage {
  api_key: string
}

export interface StorageAccess {
  id: number
  storage_id: number
  service_id: number
  service_name: string
  can_read: boolean
  can_write: boolean
  granted_at: string
}

export interface StorageKVItem {
  key: string
  content_type: string
  size_bytes: number
  updated_at: string
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
    client.get('/api/storages').then((r) => r.data),

  /** Get a single storage by ID. */
  get: (id: number): Promise<Storage> =>
    client.get(`/api/storages/${id}`).then((r) => r.data),

  /** Create a storage. Returns the plaintext API key ONCE. */
  create: (payload: CreateStoragePayload): Promise<StorageCreated> =>
    client.post('/api/storages', payload).then((r) => r.data),

  /** Permanently delete a storage and all its data. */
  delete: (id: number): Promise<void> =>
    client.delete(`/api/storages/${id}`).then(() => undefined),

  /**
   * Rotate the API key of a storage.
   * Returns the new plaintext key ONCE — it cannot be recovered later.
   */
  rotateKey: (id: number): Promise<{ api_key: string; api_key_preview: string }> =>
    client.post(`/api/storages/${id}/rotate-key`).then((r) => r.data),

  // ── Access management ────────────────────────────────────────────────────

  /** List services that have access to a storage. */
  listAccess: (id: number): Promise<StorageAccess[]> =>
    client.get(`/api/storages/${id}/access`).then((r) => r.data),

  /** Grant a service access to a storage. */
  grantAccess: (id: number, payload: GrantAccessPayload): Promise<void> =>
    client.post(`/api/storages/${id}/access`, payload).then(() => undefined),

  /** Revoke a service's access to a storage. */
  revokeAccess: (storageId: number, serviceId: number): Promise<void> =>
    client.delete(`/api/storages/${storageId}/access/${serviceId}`).then(() => undefined),

  // ── KV admin ─────────────────────────────────────────────────────────────

  /** List all keys in a storage (admin view, no values). */
  listKeys: (id: number): Promise<StorageKVItem[]> =>
    client.get(`/api/storages/${id}/kv`).then((r) => r.data),

  /** Delete a key from a storage (admin action, no storage API key needed). */
  adminDeleteKey: (id: number, key: string): Promise<void> =>
    client.delete(`/api/storages/${id}/kv/${encodeURIComponent(key)}`).then(() => undefined),
}
