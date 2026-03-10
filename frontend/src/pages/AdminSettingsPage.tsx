import { ShieldCheck, Server, Key } from 'lucide-react'

export function AdminSettingsPage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">System Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">Platform-wide configuration (superadmin only).</p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {[
          { icon: ShieldCheck, title: 'Security', desc: 'JWT expiry, password policy' },
          { icon: Server,      title: 'Infrastructure', desc: 'Resource limits, quotas' },
          { icon: Key,         title: 'API Keys',   desc: 'Manage platform API keys' },
        ].map(({ icon: Icon, title, desc }) => (
          <div key={title} className="rounded-xl border border-border bg-card p-5 space-y-3">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <Icon className="h-4.5 w-4.5" />
            </div>
            <div>
              <p className="font-medium">{title}</p>
              <p className="text-sm text-muted-foreground mt-0.5">{desc}</p>
            </div>
          </div>
        ))}
      </div>

      <div className="rounded-xl border border-amber-200 dark:border-amber-800/40 bg-amber-50 dark:bg-amber-900/10 px-4 py-3 text-sm text-amber-800 dark:text-amber-400">
        System settings are read-only in demo mode. Connect the backend to enable configuration.
      </div>
    </div>
  )
}
