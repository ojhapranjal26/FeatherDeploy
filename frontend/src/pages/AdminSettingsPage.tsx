import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { ShieldCheck, Server, Key, ImageIcon, Building2 } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { settingsApi } from '@/api/settings'

export function AdminSettingsPage() {
  const qc = useQueryClient()

  const { data: branding } = useQuery({
    queryKey: ['branding'],
    queryFn: settingsApi.getBranding,
  })

  const [companyName, setCompanyName] = useState('')
  const [logoUrl, setLogoUrl] = useState('')

  useEffect(() => {
    if (branding) {
      setCompanyName(branding.company_name)
      setLogoUrl(branding.logo_url)
    }
  }, [branding])

  const mutation = useMutation({
    mutationFn: () => settingsApi.setBranding({ company_name: companyName, logo_url: logoUrl }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['branding'] })
      toast.success('Branding saved')
    },
    onError: () => {
      toast.error('Failed to save branding')
    },
  })

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">System Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">Platform-wide configuration (superadmin only).</p>
      </div>

      {/* ── Company Branding ─────────────────────────────────────────────── */}
      <div className="rounded-xl border border-border bg-card p-6 space-y-5">
        <div className="flex items-center gap-3">
          <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <Building2 className="h-4.5 w-4.5" />
          </div>
          <div>
            <p className="font-medium">Company Branding</p>
            <p className="text-sm text-muted-foreground">Customise the name and logo shown on the login page and sidebar.</p>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="company-name">Company name</Label>
            <Input
              id="company-name"
              placeholder="FeatherDeploy"
              value={companyName}
              onChange={(e) => setCompanyName(e.target.value)}
              maxLength={120}
            />
            <p className="text-xs text-muted-foreground">Shown in the sidebar header and login page. Leave blank to use "FeatherDeploy".</p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="logo-url">Logo URL</Label>
            <Input
              id="logo-url"
              type="url"
              placeholder="https://example.com/logo.png"
              value={logoUrl}
              onChange={(e) => setLogoUrl(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">Direct link to a PNG/SVG. Leave blank to use the default feather icon.</p>
          </div>
        </div>

        {/* Logo preview */}
        {logoUrl && (
          <div className="flex items-center gap-3 rounded-lg border border-border bg-muted/40 px-4 py-3">
            <ImageIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="text-xs text-muted-foreground">Preview:</span>
            <img
              src={logoUrl}
              alt="Logo preview"
              className="h-8 w-auto max-w-[200px] object-contain"
              onError={(e) => {
                (e.target as HTMLImageElement).style.display = 'none'
              }}
            />
          </div>
        )}

        <div className="flex justify-end">
          <Button
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending}
            className="min-w-[100px]"
          >
            {mutation.isPending ? 'Saving…' : 'Save branding'}
          </Button>
        </div>
      </div>

      {/* ── Placeholder settings cards ────────────────────────────────── */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {[
          { icon: ShieldCheck, title: 'Security', desc: 'JWT expiry, password policy' },
          { icon: Server,      title: 'Infrastructure', desc: 'Resource limits, quotas' },
          { icon: Key,         title: 'API Keys',   desc: 'Manage platform API keys' },
        ].map(({ icon: Icon, title, desc }) => (
          <div key={title} className="rounded-xl border border-border bg-card p-5 space-y-3 opacity-60">
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
    </div>
  )
}
