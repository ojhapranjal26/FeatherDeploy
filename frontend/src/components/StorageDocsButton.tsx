import { useMemo, useState } from 'react'
import { BookOpen, Check, Copy, Download, Key, List, PackageOpen, Shield, Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

type StorageDocsButtonProps = {
  storage?: {
    id?: number
    name?: string
  }
  className?: string
  variant?: 'default' | 'outline' | 'ghost' | 'secondary'
  size?: 'default' | 'sm' | 'lg'
}

function envVarBase(name?: string) {
  if (!name) return 'NAME'
  const normalized = name
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
  return normalized || 'NAME'
}

function CodeBlock({ code, className }: { code: string; className?: string }) {
  const [copied, setCopied] = useState(false)

  const copy = () => {
    navigator.clipboard.writeText(code).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    })
  }

  return (
    <div className={cn('rounded-xl border bg-zinc-950 text-zinc-50', className)}>
      <div className="flex items-center justify-between border-b border-zinc-800 px-3 py-2 text-xs text-zinc-400">
        <span>Example</span>
        <button
          type="button"
          onClick={copy}
          className="inline-flex items-center gap-1 text-zinc-300 transition-colors hover:text-white"
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre className="overflow-x-auto px-3 py-3 text-xs leading-5">
        <code>{code}</code>
      </pre>
    </div>
  )
}

function ApiRow({ method, path, description }: { method: string; path: string; description: string }) {
  return (
    <div className="grid gap-2 rounded-xl border bg-card/60 p-3 md:grid-cols-[90px_1fr_220px] md:items-center">
      <Badge variant="outline" className="w-fit font-mono text-[11px] uppercase tracking-wide">{method}</Badge>
      <code className="overflow-x-auto rounded bg-muted px-2 py-1 text-xs">{path}</code>
      <p className="text-xs text-muted-foreground">{description}</p>
    </div>
  )
}

export function StorageDocsButton({ storage, className, variant = 'outline', size = 'sm' }: StorageDocsButtonProps) {
  const [open, setOpen] = useState(false)

  const docs = useMemo(() => {
    const envBase = envVarBase(storage?.name)
    const storageId = storage?.id ?? '{id}'
    const endpointPath = `/api/storage/${storageId}`
    const internalEndpoint = `http://10.0.2.2:8080${endpointPath}`
    const endpointVar = `STORAGE_${envBase}_ENDPOINT`
    const keyVar = `STORAGE_${envBase}_KEY`
    const bucketVar = `STORAGE_${envBase}_BUCKET`

    return {
      envBase,
      endpointVar,
      keyVar,
      bucketVar,
      endpointPath,
      internalEndpoint,
      listExample: `curl -H "X-Storage-Key: $${keyVar}" \\
  "$${endpointVar}/list?prefix=uploads/"`,
      putExample: `curl -X PUT \\
  -H "X-Storage-Key: $${keyVar}" \\
  --data-binary @./avatar.png \\
  "$${endpointVar}/objects/uploads/avatar.png"`,
      getExample: `curl -H "X-Storage-Key: $${keyVar}" \\
  "$${endpointVar}/objects/uploads/avatar.png" \\
  --output avatar.png`,
      initExample: `curl -X POST \\
  -H "X-Storage-Key: $${keyVar}" \\
  "$${endpointVar}/multipart/init?path=videos/demo.mp4"`,
      partExample: `curl -X PUT \\
  -H "X-Storage-Key: $${keyVar}" \\
  --data-binary @./demo.part1 \\
  "$${endpointVar}/multipart/$UPLOAD_ID/part/1"`,
      completeExample: `curl -X POST \\
  -H "X-Storage-Key: $${keyVar}" \\
  "$${endpointVar}/multipart/$UPLOAD_ID/complete"`,
      abortExample: `curl -X DELETE \\
  -H "X-Storage-Key: $${keyVar}" \\
  "$${endpointVar}/multipart/$UPLOAD_ID"`,
      nodeExample: `const endpoint = process.env.${endpointVar}!
const key = process.env.${keyVar}!

export async function uploadBuffer(path: string, data: Buffer) {
  const response = await fetch(\`${'${endpoint}'}/objects/\${encodeURI(path)}\`, {
    method: 'PUT',
    headers: {
      'X-Storage-Key': key,
      'Content-Type': 'application/octet-stream',
    },
    body: data,
  })

  if (!response.ok) {
    throw new Error(\`Upload failed: \${response.status} \${await response.text()}\`)
  }
}`,
    }
  }, [storage?.id, storage?.name])

  return (
    <>
      <Button variant={variant} size={size} className={cn('gap-1.5', className)} onClick={() => setOpen(true)}>
        <BookOpen className="h-4 w-4" /> View Docs
      </Button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-5xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <BookOpen className="h-5 w-5 text-primary" /> Storage Integration Docs
            </DialogTitle>
            <DialogDescription>
              Connect apps with per-service keys, path-based object APIs, and multipart uploads.
              {storage?.name ? ` This guide is scoped to ${storage.name}.` : ' Grant access to a service, then redeploy it to receive the injected env vars.'}
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-3 md:grid-cols-3">
            <Card size="sm">
              <CardHeader>
                <CardTitle className="flex items-center gap-2"><Shield className="h-4 w-4 text-emerald-500" /> Access model</CardTitle>
                <CardDescription>Every service gets its own key and its own read/write permissions.</CardDescription>
              </CardHeader>
            </Card>
            <Card size="sm">
              <CardHeader>
                <CardTitle className="flex items-center gap-2"><Key className="h-4 w-4 text-amber-500" /> Auth</CardTitle>
                <CardDescription>Use the service key in the <code className="rounded bg-muted px-1 py-0.5 text-[11px]">X-Storage-Key</code> header.</CardDescription>
              </CardHeader>
            </Card>
            <Card size="sm">
              <CardHeader>
                <CardTitle className="flex items-center gap-2"><PackageOpen className="h-4 w-4 text-sky-500" /> Storage model</CardTitle>
                <CardDescription>Paths behave like folders, not KV keys. Files are encrypted server-side at rest.</CardDescription>
              </CardHeader>
            </Card>
          </div>

          <Tabs defaultValue="quick-start">
            <TabsList className="mb-4 flex-wrap h-auto">
              <TabsTrigger value="quick-start">Quick Start</TabsTrigger>
              <TabsTrigger value="env">Env Vars</TabsTrigger>
              <TabsTrigger value="api">HTTP API</TabsTrigger>
              <TabsTrigger value="examples">Examples</TabsTrigger>
              <TabsTrigger value="multipart">Multipart</TabsTrigger>
            </TabsList>

            <TabsContent value="quick-start" className="space-y-4">
              <Card>
                <CardHeader>
                  <CardTitle>Recommended process</CardTitle>
                  <CardDescription>Use this flow for the cleanest integration.</CardDescription>
                </CardHeader>
                <CardContent className="space-y-3 text-sm">
                  <div className="rounded-xl border p-3">1. Create a storage bucket from the Storage screen.</div>
                  <div className="rounded-xl border p-3">2. Open the bucket, go to Services, and grant a service read and/or write access.</div>
                  <div className="rounded-xl border p-3">3. Redeploy that service so FeatherDeploy injects the storage env vars.</div>
                  <div className="rounded-xl border p-3">4. Read the endpoint and key from env vars inside the app, then call the object API with <code className="rounded bg-muted px-1 py-0.5 text-[11px]">X-Storage-Key</code>.</div>
                  <div className="rounded-xl border p-3">5. Use direct PUT for small files and multipart for large or retry-heavy uploads.</div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Path rules</CardTitle>
                </CardHeader>
                <CardContent className="grid gap-2 text-sm text-muted-foreground">
                  <p>Use folder-like paths such as <code className="rounded bg-muted px-1 py-0.5 text-[11px]">avatars/user-42.png</code>.</p>
                  <p><code className="rounded bg-muted px-1 py-0.5 text-[11px]">..</code> is rejected.</p>
                  <p><code className="rounded bg-muted px-1 py-0.5 text-[11px]">.multipart</code> is reserved and rejected.</p>
                  <p>Empty paths are rejected.</p>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="env" className="space-y-4">
              <Card>
                <CardHeader>
                  <CardTitle>Injected env vars</CardTitle>
                  <CardDescription>These appear after the service has access and has been redeployed.</CardDescription>
                </CardHeader>
                <CardContent className="space-y-3">
                  <div className="rounded-xl border p-3 font-mono text-xs">{docs.keyVar}</div>
                  <div className="rounded-xl border p-3 font-mono text-xs">{docs.bucketVar}</div>
                  <div className="rounded-xl border p-3 font-mono text-xs">{docs.endpointVar}</div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle>Endpoint values</CardTitle>
                </CardHeader>
                <CardContent className="space-y-3 text-sm">
                  <div className="rounded-xl border p-3">
                    <p className="font-medium">Inside FeatherDeploy service containers</p>
                    <code className="mt-2 block rounded bg-muted px-2 py-1 text-xs">{docs.internalEndpoint}</code>
                  </div>
                  <div className="rounded-xl border p-3">
                    <p className="font-medium">External client pattern</p>
                    <code className="mt-2 block rounded bg-muted px-2 py-1 text-xs">https://panel.example.com{docs.endpointPath}</code>
                  </div>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="api" className="space-y-3">
              <ApiRow method="GET" path={`${docs.endpointPath}/list?prefix=folder/`} description="List objects under an optional prefix." />
              <ApiRow method="GET" path={`${docs.endpointPath}/objects/{path}`} description="Download and decrypt an object." />
              <ApiRow method="PUT" path={`${docs.endpointPath}/objects/{path}`} description="Upload and encrypt an object." />
              <ApiRow method="DELETE" path={`${docs.endpointPath}/objects/{path}`} description="Delete an object if the key has write access." />
              <ApiRow method="POST" path={`${docs.endpointPath}/multipart/init?path={path}`} description="Create a multipart upload and receive an upload ID." />
              <ApiRow method="PUT" path={`${docs.endpointPath}/multipart/{uploadId}/part/{partNumber}`} description="Upload a single multipart chunk. Part numbers must be 1-10000." />
              <ApiRow method="POST" path={`${docs.endpointPath}/multipart/{uploadId}/complete`} description="Assemble all uploaded parts in numeric order and finalize the encrypted object." />
              <ApiRow method="DELETE" path={`${docs.endpointPath}/multipart/{uploadId}`} description="Abort a multipart upload and delete staged parts." />
            </TabsContent>

            <TabsContent value="examples" className="space-y-4">
              <div className="grid gap-4 xl:grid-cols-2">
                <Card>
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2"><List className="h-4 w-4 text-primary" /> List objects</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <CodeBlock code={docs.listExample} />
                  </CardContent>
                </Card>

                <Card>
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2"><Upload className="h-4 w-4 text-primary" /> Upload a file</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <CodeBlock code={docs.putExample} />
                  </CardContent>
                </Card>

                <Card>
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2"><Download className="h-4 w-4 text-primary" /> Download a file</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <CodeBlock code={docs.getExample} />
                  </CardContent>
                </Card>

                <Card>
                  <CardHeader>
                    <CardTitle>Node.js upload example</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <CodeBlock code={docs.nodeExample} />
                  </CardContent>
                </Card>
              </div>
            </TabsContent>

            <TabsContent value="multipart" className="space-y-4">
              <Card>
                <CardHeader>
                  <CardTitle>Multipart upload flow</CardTitle>
                  <CardDescription>Use multipart for large files or uploads that need chunk retries.</CardDescription>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="space-y-2">
                    <p className="text-sm font-medium">1. Initialize the upload</p>
                    <CodeBlock code={docs.initExample} />
                  </div>
                  <div className="space-y-2">
                    <p className="text-sm font-medium">2. Upload numbered parts</p>
                    <CodeBlock code={docs.partExample} />
                  </div>
                  <div className="space-y-2">
                    <p className="text-sm font-medium">3. Complete the upload</p>
                    <CodeBlock code={docs.completeExample} />
                  </div>
                  <div className="space-y-2">
                    <p className="text-sm font-medium">4. Abort if needed</p>
                    <CodeBlock code={docs.abortExample} />
                  </div>
                  <div className="rounded-xl border bg-muted/30 p-3 text-sm text-muted-foreground">
                    Completion does not require an ETag manifest. The server assembles every uploaded <code className="rounded bg-muted px-1 py-0.5 text-[11px]">part-xxxxx</code> file in numeric order and writes the final encrypted object.
                  </div>
                </CardContent>
              </Card>
            </TabsContent>
          </Tabs>
        </DialogContent>
      </Dialog>
    </>
  )
}