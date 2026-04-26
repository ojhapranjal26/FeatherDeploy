# Storage Integration Guide

FeatherDeploy Storage is a disk-backed object store with server-side encryption and per-service API keys.

## How access works

1. Create a storage bucket from the Storage screen.
2. Open the bucket and go to the Services tab.
3. Grant a service read and/or write access.
4. Copy the service key shown once, or redeploy the service so FeatherDeploy injects the storage env vars automatically.

Each service gets its own key. There is no shared bucket key for all services.

Do not manually keep stale generic values like `STORAGE_KEY` and `STORAGE_ENDPOINT` alongside injected per-storage vars unless your app is intentionally reading the generic names. A stale manual key can override the correct injected key and cause `invalid key` errors.

## Auto-injected env vars

After a service is granted access and redeployed, FeatherDeploy injects:

- `STORAGE_{NAME}_KEY`
- `STORAGE_{NAME}_BUCKET`
- `STORAGE_{NAME}_ENDPOINT`

Preferred integration:

1. Grant access to the service.
2. Redeploy the service.
3. Read the injected `STORAGE_{NAME}_KEY` and `STORAGE_{NAME}_ENDPOINT` vars inside the app.
4. Avoid hardcoding `STORAGE_KEY` and `STORAGE_ENDPOINT` unless you have only one storage and explicitly want manual configuration.

Example for a storage named `media uploads`:

- `STORAGE_MEDIA_UPLOADS_KEY`
- `STORAGE_MEDIA_UPLOADS_BUCKET`
- `STORAGE_MEDIA_UPLOADS_ENDPOINT`

For services running inside FeatherDeploy, the endpoint value is injected in this form:

```text
http://10.0.2.2:8080/api/storage/{id}
```

For external clients, use your panel base URL:

```text
https://panel.example.com/api/storage/{id}
```

## Auth

All object requests use the service key in the `X-Storage-Key` header.

```http
X-Storage-Key: <service-key>
```

Admin endpoints under `/api/storages/*` use panel JWT auth and are intended for the dashboard. App integrations should use `/api/storage/*`.

## Object API

Base URL:

```text
{STORAGE_NAME_ENDPOINT}
```

Routes:

- `GET /list?prefix=folder/`
- `GET /objects/{path}`
- `PUT /objects/{path}`
- `DELETE /objects/{path}`
- `POST /multipart/init?path={path}`
- `PUT /multipart/{uploadId}/part/{partNumber}`
- `POST /multipart/{uploadId}/complete`
- `DELETE /multipart/{uploadId}`

## Path rules

- Paths are folder-like, for example `avatars/user-42.png`
- `..` is rejected
- `.multipart` is reserved and rejected
- Empty paths are rejected

## Listing objects

```bash
curl -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/list?prefix=avatars/"
```

Example response:

```json
[
  {
    "path": "avatars/user-42.png",
    "size": 18293,
    "updated_at": "2026-04-26T06:22:10Z"
  }
]
```

## Uploading a file

```bash
curl -X PUT \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  --data-binary @./avatar.png \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/objects/avatars/user-42.png"
```

The server encrypts the file before writing it to disk. Clients send plaintext bytes.

## Downloading a file

```bash
curl -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/objects/avatars/user-42.png" \
  --output avatar.png
```

Downloads are streamed and decrypted on the fly.

## Deleting a file

```bash
curl -X DELETE \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/objects/avatars/user-42.png"
```

## Multipart upload flow

Use multipart when files are large or you want retryable chunk uploads.

### 1. Start the upload

```bash
curl -X POST \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/multipart/init?path=videos/demo.mp4"
```

Response:

```json
{
  "upload_id": "a1b2c3..."
}
```

### 2. Upload parts in order or in parallel

```bash
curl -X PUT \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  --data-binary @./demo.part1 \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/multipart/$UPLOAD_ID/part/1"

curl -X PUT \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  --data-binary @./demo.part2 \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/multipart/$UPLOAD_ID/part/2"
```

Parts are assembled by numeric part number during completion.

### 3. Complete the upload

```bash
curl -X POST \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/multipart/$UPLOAD_ID/complete"
```

Response:

```json
{
  "path": "videos/demo.mp4",
  "size_bytes": 73400320
}
```

### 4. Abort if needed

```bash
curl -X DELETE \
  -H "X-Storage-Key: $STORAGE_MEDIA_UPLOADS_KEY" \
  "$STORAGE_MEDIA_UPLOADS_ENDPOINT/multipart/$UPLOAD_ID"
```

## Node.js example

```ts
const endpoint = process.env.STORAGE_MEDIA_UPLOADS_ENDPOINT!
const key = process.env.STORAGE_MEDIA_UPLOADS_KEY!

export async function uploadBuffer(path: string, data: Buffer) {
  const response = await fetch(`${endpoint}/objects/${encodeURI(path)}`, {
    method: 'PUT',
    headers: {
      'X-Storage-Key': key,
      'Content-Type': 'application/octet-stream',
    },
    body: data,
  })

  if (!response.ok) {
    throw new Error(`Upload failed: ${response.status} ${await response.text()}`)
  }
}
```

## Recommended process

1. Grant access only to the services that need the bucket.
2. Prefer read-only keys for consumers that should not write.
3. Redeploy after granting or rotating a key so injected env vars stay current.
4. Use multipart for large uploads or retry-heavy mobile clients.
5. Keep file names deterministic, for example `users/{id}/avatar.png`.
6. Treat the service key like any other secret.
7. Rotate the service key immediately if it leaks.

## Notes

- Files are encrypted at rest with AES-256-CTR.
- The nonce is stored with the encrypted file automatically.
- Storage size and bandwidth are tracked by bucket and service.
- The admin dashboard can browse files, manage access, rotate keys, and inspect bandwidth usage.