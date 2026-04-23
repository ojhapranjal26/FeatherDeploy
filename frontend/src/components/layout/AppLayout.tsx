import { useEffect, useState } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Menu, Feather, ChevronRight } from 'lucide-react'
import { Sidebar } from './Sidebar'
import { Toaster } from '@/components/ui/sonner'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Button } from '@/components/ui/button'
import { settingsApi } from '@/api/settings'

// Derive a human-readable page title from the current pathname.
function usePageTitle() {
  const { pathname } = useLocation()
  if (pathname === '/dashboard' || pathname === '/') return 'Dashboard'
  if (pathname === '/projects') return 'Projects'
  if (/\/services\/[^/]+\/deployments\/[^/]+/.test(pathname)) return 'Deployment'
  if (pathname.endsWith('/deployments')) return 'Deployments'
  if (pathname.endsWith('/env')) return 'Environment'
  if (pathname.endsWith('/domains')) return 'Domains'
  if (/\/services\/[^/]+$/.test(pathname)) return 'Service'
  if (/\/projects\/[^/]+$/.test(pathname)) return 'Project'
  if (pathname.startsWith('/admin/users')) return 'User Management'
  if (pathname.startsWith('/admin/nodes')) return 'Cluster Nodes'
  if (pathname.startsWith('/admin/settings')) return 'System Settings'
  if (pathname.startsWith('/settings/github')) return 'GitHub'
  if (pathname.startsWith('/settings/profile')) return 'My Preferences'
  return ''
}

export function AppLayout() {
  const [collapsed, setCollapsed] = useState(false)
  const [mobileOpen, setMobileOpen] = useState(false)
  const { pathname } = useLocation()
  const pageTitle = usePageTitle()

  // Close mobile drawer on route change
  useEffect(() => { setMobileOpen(false) }, [pathname])

  const { data: branding } = useQuery({
    queryKey: ['branding'],
    queryFn: settingsApi.getBranding,
    staleTime: 5 * 60 * 1000,
  })
  const platformName = branding?.company_name || 'FeatherDeploy'

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      {/* Desktop sidebar — hidden on mobile */}
      <div className="hidden md:flex">
        <Sidebar collapsed={collapsed} onToggle={() => setCollapsed((c) => !c)} />
      </div>

      {/* Mobile slide-over sidebar */}
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetContent side="left" className="p-0 w-64 max-w-[80vw]" showCloseButton={false}>
          <Sidebar collapsed={false} onToggle={() => setMobileOpen(false)} />
        </SheetContent>
      </Sheet>

      {/* Content area */}
      <div className="flex flex-1 flex-col overflow-hidden min-w-0">
        {/* Mobile top bar — hidden on desktop */}
        <header className="flex md:hidden h-14 shrink-0 items-center border-b border-border bg-background px-3 gap-2">
          <Button
            variant="ghost"
            size="icon"
            className="h-9 w-9 shrink-0"
            onClick={() => setMobileOpen(true)}
          >
            <Menu className="h-5 w-5" />
          </Button>

          <div className="flex items-center gap-2 min-w-0 flex-1 overflow-hidden">
            {branding?.logo_url ? (
              <img
                src={branding.logo_url}
                alt={platformName}
                className="h-6 w-auto max-w-[80px] rounded object-contain shrink-0"
              />
            ) : (
              <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded bg-primary text-primary-foreground">
                <Feather className="h-3.5 w-3.5" />
              </div>
            )}

            {/* Breadcrumb: AppName / PageTitle */}
            <nav className="flex min-w-0 items-center gap-1 text-sm overflow-hidden">
              <span className="truncate text-muted-foreground shrink-0 max-w-[90px]">{platformName}</span>
              {pageTitle && (
                <>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
                  <span className="truncate font-medium">{pageTitle}</span>
                </>
              )}
            </nav>
          </div>
        </header>

        <main className="flex-1 overflow-y-auto min-w-0">
          <div className="mx-auto max-w-6xl px-4 py-4 md:px-6 md:py-6">
            <Outlet />
          </div>
        </main>
      </div>

      <Toaster richColors position="top-right" />
    </div>
  )
}

