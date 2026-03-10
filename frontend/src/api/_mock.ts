// ---------------------------------------------------------------------------
// MOCK DATA STORE
// All API modules import from here while the real backend is bypassed.
// ---------------------------------------------------------------------------

export const delay = (ms = 350) =>
  new Promise<void>((r) => setTimeout(r, ms))

export const uid = () => Math.random().toString(36).slice(2, 10)

const ago = (daysAgo: number, h = 12): string => {
  const d = new Date()
  d.setDate(d.getDate() - daysAgo)
  d.setHours(h, 0, 0, 0)
  return d.toISOString()
}

// Pre-seed auth so AuthContext treats the visitor as logged-in
if (typeof localStorage !== 'undefined' && !localStorage.getItem('token')) {
  localStorage.setItem('token', 'mock-token')
}

// ── Auth ────────────────────────────────────────────────────────────────────
export const MOCK_USER = {
  id: 'user-1',
  email: 'alex@acme.com',
  name: 'Alex Chen',
  role: 'superadmin' as const,
  created_at: ago(30),
}

// ── Projects ────────────────────────────────────────────────────────────────
export const projects = [
  {
    id: 'proj-1',
    name: 'storefront',
    description: 'Main customer-facing e-commerce frontend',
    service_count: 2,
    created_at: ago(12),
    updated_at: ago(1),
  },
  {
    id: 'proj-2',
    name: 'api-gateway',
    description: 'Public REST API with rate-limiting',
    service_count: 1,
    created_at: ago(9),
    updated_at: ago(0),
  },
  {
    id: 'proj-3',
    name: 'data-pipeline',
    description: 'ETL background workers and cron jobs',
    service_count: 1,
    created_at: ago(6),
    updated_at: ago(2),
  },
]

// ── Services ────────────────────────────────────────────────────────────────
type MockSvc = {
  id: string; project_id: string; name: string; description: string
  deploy_type: 'git' | 'artifact' | 'dockerfile'
  repo_url?: string; repo_branch: string; framework?: string
  build_command?: string; start_command?: string
  app_port: number; host_port?: number
  status: 'inactive' | 'deploying' | 'running' | 'error' | 'stopped'
  container_id?: string; created_at: string; updated_at: string
}
export type MockService = MockSvc

export const services: Record<string, MockSvc[]> = {
  'proj-1': [
    {
      id: 'svc-1',
      project_id: 'proj-1',
      name: 'web',
      description: 'Next.js storefront',
      deploy_type: 'git' as const,
      repo_url: 'https://github.com/acme/storefront',
      repo_branch: 'main',
      framework: 'Next.js',
      build_command: 'npm run build',
      start_command: 'npm start',
      app_port: 3000,
      host_port: 3001,
      status: 'running' as const,
      container_id: 'c1a2b3d4e5f6',
      created_at: ago(11),
      updated_at: ago(1),
    },
    {
      id: 'svc-2',
      project_id: 'proj-1',
      name: 'order-worker',
      description: 'Async order processing worker',
      deploy_type: 'git' as const,
      repo_url: 'https://github.com/acme/storefront',
      repo_branch: 'worker',
      framework: 'Node.js',
      build_command: 'npm run build',
      start_command: 'node dist/worker.js',
      app_port: 8080,
      host_port: undefined,
      status: 'error' as const,
      container_id: undefined,
      created_at: ago(11),
      updated_at: ago(0),
    },
  ],
  'proj-2': [
    {
      id: 'svc-3',
      project_id: 'proj-2',
      name: 'gateway',
      description: 'Express API gateway',
      deploy_type: 'dockerfile' as const,
      repo_url: undefined,
      repo_branch: 'main',
      framework: 'Express',
      build_command: undefined,
      start_command: undefined,
      app_port: 4000,
      host_port: 4001,
      status: 'running' as const,
      container_id: 'f9e8d7c6b5a4',
      created_at: ago(8),
      updated_at: ago(0),
    },
  ],
  'proj-3': [
    {
      id: 'svc-4',
      project_id: 'proj-3',
      name: 'etl-worker',
      description: 'Data ingestion pipeline',
      deploy_type: 'git' as const,
      repo_url: 'https://github.com/acme/pipeline',
      repo_branch: 'main',
      framework: undefined,
      build_command: 'make build',
      start_command: './bin/worker',
      app_port: 9000,
      host_port: undefined,
      status: 'stopped' as const,
      container_id: undefined,
      created_at: ago(5),
      updated_at: ago(2),
    },
  ],
}

// Flat lookup for convenience
export const allServices = (): MockService[] =>
  Object.values(services).flat()

// ── Deployments ─────────────────────────────────────────────────────────────
export const deployments: Record<string, object[]> = {
  'svc-1': [
    {
      id: 'dep-1',
      service_id: 'svc-1',
      triggered_by: 'user-1',
      deploy_type: 'git',
      repo_url: 'https://github.com/acme/storefront',
      commit_sha: 'a1b2c3d',
      artifact_path: undefined,
      status: 'success',
      error_message: undefined,
      started_at: ago(1, 10),
      finished_at: ago(1, 10),
      created_at: ago(1, 10),
    },
    {
      id: 'dep-2',
      service_id: 'svc-1',
      triggered_by: 'user-1',
      deploy_type: 'git',
      repo_url: 'https://github.com/acme/storefront',
      commit_sha: 'f4e5d6c',
      artifact_path: undefined,
      status: 'success',
      error_message: undefined,
      started_at: ago(3, 14),
      finished_at: ago(3, 14),
      created_at: ago(3, 14),
    },
    {
      id: 'dep-3',
      service_id: 'svc-1',
      triggered_by: 'user-1',
      deploy_type: 'git',
      repo_url: 'https://github.com/acme/storefront',
      commit_sha: '9876543',
      artifact_path: undefined,
      status: 'failed',
      error_message: 'npm run build exited with code 1\nnpm ERR! missing script: build',
      started_at: ago(5, 9),
      finished_at: ago(5, 9),
      created_at: ago(5, 9),
    },
  ],
  'svc-2': [
    {
      id: 'dep-4',
      service_id: 'svc-2',
      triggered_by: 'user-1',
      deploy_type: 'git',
      repo_url: 'https://github.com/acme/storefront',
      commit_sha: 'bbb1111',
      artifact_path: undefined,
      status: 'failed',
      error_message: 'Container OOM killed (limit 512 MB)',
      started_at: ago(0, 11),
      finished_at: ago(0, 11),
      created_at: ago(0, 11),
    },
  ],
  'svc-3': [
    {
      id: 'dep-5',
      service_id: 'svc-3',
      triggered_by: 'user-1',
      deploy_type: 'dockerfile',
      repo_url: undefined,
      commit_sha: undefined,
      artifact_path: undefined,
      status: 'success',
      error_message: undefined,
      started_at: ago(0, 8),
      finished_at: ago(0, 8),
      created_at: ago(0, 8),
    },
  ],
  'svc-4': [],
}

// ── Env Variables ────────────────────────────────────────────────────────────
export const envVars: Record<string, object[]> = {
  'svc-1': [
    { id: 'env-1', key: 'NODE_ENV',              value: 'production',                  is_secret: false, updated_at: ago(5) },
    { id: 'env-2', key: 'NEXT_PUBLIC_API_URL',   value: 'https://api.acme.com',        is_secret: false, updated_at: ago(5) },
    { id: 'env-3', key: 'DATABASE_URL',          value: 'postgres://db:5432/storefront', is_secret: true, updated_at: ago(3) },
    { id: 'env-4', key: 'STRIPE_SECRET_KEY',     value: 'sk_live_xxxxxxxxxxxxxxxxxxxx', is_secret: true,  updated_at: ago(2) },
    { id: 'env-5', key: 'REDIS_URL',             value: 'redis://cache:6379/0',         is_secret: false, updated_at: ago(2) },
  ],
  'svc-2': [
    { id: 'env-6', key: 'QUEUE_URL',             value: 'amqp://rabbitmq:5672',         is_secret: false, updated_at: ago(4) },
    { id: 'env-7', key: 'WORKER_CONCURRENCY',    value: '5',                            is_secret: false, updated_at: ago(4) },
    { id: 'env-8', key: 'DATABASE_URL',          value: 'postgres://db:5432/storefront', is_secret: true, updated_at: ago(3) },
  ],
  'svc-3': [
    { id: 'env-9', key: 'PORT',                  value: '4000',                         is_secret: false, updated_at: ago(7) },
    { id: 'env-10', key: 'JWT_SECRET',           value: 'super-secret-jwt-key-here',    is_secret: true,  updated_at: ago(7) },
  ],
  'svc-4': [],
}

// ── Domains ──────────────────────────────────────────────────────────────────
export const domains: Record<string, object[]> = {
  'svc-1': [
    { id: 'dom-1', service_id: 'svc-1', domain: 'store.acme.com',   tls: true,  verified: true,  created_at: ago(8), updated_at: ago(8) },
    { id: 'dom-2', service_id: 'svc-1', domain: 'www.acme.com',     tls: true,  verified: true,  created_at: ago(6), updated_at: ago(6) },
    { id: 'dom-3', service_id: 'svc-1', domain: 'staging.acme.com', tls: false, verified: false, created_at: ago(1), updated_at: ago(1) },
  ],
  'svc-2': [],
  'svc-3': [
    { id: 'dom-4', service_id: 'svc-3', domain: 'api.acme.com',     tls: true,  verified: true,  created_at: ago(7), updated_at: ago(7) },
  ],
  'svc-4': [],
}

// ── Mock log lines (used by useDeploymentLogs) ───────────────────────────────
export const MOCK_LOG_LINES = [
  '\x1b[90m[09:12:01]\x1b[0m Initializing deployment pipeline...',
  '\x1b[90m[09:12:01]\x1b[0m Cloning \x1b[36mhttps://github.com/acme/storefront\x1b[0m @ \x1b[33mmain\x1b[0m',
  '\x1b[90m[09:12:02]\x1b[0m Checked out commit \x1b[33ma1b2c3d\x1b[0m',
  '\x1b[90m[09:12:03]\x1b[0m Detecting runtime... \x1b[32mNode.js 20.x\x1b[0m',
  '\x1b[90m[09:12:03]\x1b[0m Restoring build cache...',
  '\x1b[90m[09:12:04]\x1b[0m Cache hit ✓ (node_modules, .next/cache)',
  '\x1b[90m[09:12:05]\x1b[0m \x1b[1mRunning:\x1b[0m npm install',
  '\x1b[90m[09:12:06]\x1b[0m npm warn deprecated inflight@1.0.6',
  '\x1b[90m[09:12:11]\x1b[0m added 1425 packages, changed 38 packages in 6.2s',
  '\x1b[90m[09:12:12]\x1b[0m \x1b[1mRunning:\x1b[0m npm run build',
  '\x1b[90m[09:12:12]\x1b[0m > storefront@1.0.0 build',
  '\x1b[90m[09:12:12]\x1b[0m > next build',
  '\x1b[90m[09:12:14]\x1b[0m   \x1b[32m✓\x1b[0m Compiled successfully',
  '\x1b[90m[09:12:16]\x1b[0m   \x1b[32m✓\x1b[0m Linting and checking validity of types',
  '\x1b[90m[09:12:19]\x1b[0m   \x1b[32m✓\x1b[0m Collecting page data',
  '\x1b[90m[09:12:21]\x1b[0m   \x1b[32m✓\x1b[0m Generating static pages (24/24)',
  '\x1b[90m[09:12:22]\x1b[0m   \x1b[32m✓\x1b[0m Finalizing page optimization',
  '\x1b[90m[09:12:22]\x1b[0m Build complete. Writing layer cache...',
  '\x1b[90m[09:12:23]\x1b[0m Starting container from image...',
  '\x1b[90m[09:12:23]\x1b[0m Container ID: \x1b[33mc1a2b3d4e5f6\x1b[0m',
  '\x1b[90m[09:12:24]\x1b[0m Waiting for health check: GET /api/health',
  '\x1b[90m[09:12:25]\x1b[0m Health check \x1b[32mpassed\x1b[0m (200 OK, 12 ms)',
  '\x1b[90m[09:12:25]\x1b[0m Routing traffic to new container...',
  '\x1b[32m[09:12:25] ✓ Deployment successful — service running on port 3001\x1b[0m',
]
