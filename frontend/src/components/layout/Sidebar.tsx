import { NavLink, useNavigate } from 'react-router-dom'
import {
  LayoutDashboard,
  FolderKanban,
  Users,
  ShieldCheck,
  LogOut,
  Settings,
  Sun,
  Moon,
  Monitor,
  PanelLeftClose,
  Github,
  Network,
  Feather,
  Smartphone,
} from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { cn } from '@/lib/utils'
import { useAuth } from '@/context/AuthContext'
import { useTheme } from '@/context/ThemeContext'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { settingsApi } from '@/api/settings'
import type { User } from '@/api/auth'

type Role = User['role']

interface NavItem {
  to: string
  label: string
  icon: React.ElementType
  /** If set, only users whose role is in this array see the item */
  roles?: Role[]
}

const NAV_ITEMS: NavItem[] = [
  { to: '/dashboard',       label: 'Dashboard',      icon: LayoutDashboard },
  { to: '/projects',        label: 'Projects',       icon: FolderKanban },
  { to: '/settings/github', label: 'GitHub',         icon: Github },
  { to: '/devices',         label: 'Devices',        icon: Smartphone },
  { to: '/admin/users',     label: 'User Management', icon: Users,       roles: ['admin', 'superadmin'] },
  { to: '/admin/nodes',     label: 'Cluster Nodes',   icon: Network,     roles: ['admin', 'superadmin'] },
  { to: '/admin/settings',  label: 'System Settings', icon: ShieldCheck, roles: ['superadmin'] },
]

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
}

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const { user, logout } = useAuth()
  const { theme, setTheme } = useTheme()
  const navigate = useNavigate()

  const { data: branding } = useQuery({
    queryKey: ['branding'],
    queryFn: settingsApi.getBranding,
    staleTime: 5 * 60 * 1000,
  })

  const platformName = branding?.company_name || 'FeatherDeploy'

  const handleLogout = () => {
    logout()
    navigate('/login')
  }

  const cycleTheme = () =>
    setTheme(theme === 'light' ? 'dark' : theme === 'dark' ? 'system' : 'light')

  const ThemeIcon = theme === 'dark' ? Moon : theme === 'light' ? Sun : Monitor
  const themeLabel = theme === 'light' ? 'Light' : theme === 'dark' ? 'Dark' : 'System'

  const initials =
    user?.name
      ?.split(' ')
      .map((w) => w[0])
      .slice(0, 2)
      .join('')
      .toUpperCase() ?? 'U'

  return (
    <aside
      className={cn(
        'flex h-screen shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground transition-[width] duration-200 ease-in-out select-none',
        collapsed ? 'w-14' : 'w-60',
      )}
    >
      {/* ── Header ── */}
      <div
        className={cn(
          'flex h-14 shrink-0 items-center border-b border-sidebar-border',
          collapsed ? 'justify-center' : 'justify-between pl-4 pr-2',
        )}
      >
        {collapsed ? (
          <button
            onClick={onToggle}
            title="Expand sidebar"
            className="flex h-9 w-9 items-center justify-center rounded-md hover:bg-sidebar-accent transition-colors"
          >
            {branding?.logo_url ? (
              <img
                src={branding.logo_url}
                alt={platformName}
                className="h-7 w-7 rounded-md object-cover"
              />
            ) : (
              <div className="flex h-7 w-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
                <Feather className="h-4 w-4" />
              </div>
            )}
          </button>
        ) : (
          <>
            <div className="flex items-center gap-2.5">
              {branding?.logo_url ? (
                <img
                  src={branding.logo_url}
                  alt={platformName}
                  className="h-7 w-auto max-w-[100px] rounded-md object-contain"
                />
              ) : (
                <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground">
                  <Feather className="h-4 w-4" />
                </div>
              )}
              <span className="font-semibold tracking-tight">{platformName}</span>
            </div>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 text-sidebar-foreground/50 hover:text-sidebar-foreground hover:bg-sidebar-accent"
              onClick={onToggle}
              title="Collapse sidebar"
            >
              <PanelLeftClose className="h-4 w-4" />
            </Button>
          </>
        )}
      </div>

      {/* ── Navigation ── */}
      <nav className="flex-1 overflow-y-auto px-2 py-3 space-y-0.5">
        {NAV_ITEMS.filter(item => !item.roles || (user?.role && item.roles.includes(user.role))).map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            title={collapsed ? label : undefined}
            className={({ isActive }) =>
              cn(
                'relative flex items-center rounded-lg px-2.5 py-2 text-sm transition-all duration-150',
                collapsed ? 'justify-center' : 'gap-2.5',
                isActive
                  ? 'bg-sidebar-primary/12 text-sidebar-primary font-medium shadow-sm shadow-sidebar-primary/5'
                  : 'text-sidebar-foreground/65 hover:bg-sidebar-accent hover:text-sidebar-foreground',
              )
            }
          >
            {({ isActive }) => (
              <>
                {isActive && !collapsed && (
                  <span className="absolute left-0 top-1/2 -translate-y-1/2 h-5 w-[3px] rounded-r-full bg-sidebar-primary" />
                )}
                <Icon className="h-4 w-4 shrink-0" />
                {!collapsed && <span className="flex-1 truncate">{label}</span>}
              </>
            )}
          </NavLink>
        ))}
      </nav>

      {/* ── Footer ── */}
      <div className="border-t border-sidebar-border bg-sidebar-accent/30">
        {/* Theme toggle */}
        <div
          className={cn(
            'flex items-center px-2.5 py-2',
            collapsed ? 'justify-center' : 'gap-2',
          )}
        >
          <Button
            variant="ghost"
            size="icon"
            onClick={cycleTheme}
            title={themeLabel + ' theme — click to switch'}
            className="h-8 w-8 text-sidebar-foreground/60 hover:text-sidebar-foreground hover:bg-sidebar-accent shrink-0"
          >
            <ThemeIcon className="h-4 w-4" />
          </Button>
          {!collapsed && (
            <span className="text-xs text-sidebar-foreground/45 select-none">
              {themeLabel} theme
            </span>
          )}
        </div>

        <Separator className="mx-2.5 bg-sidebar-border/60" />

        {/* User section */}
        <div
          className={cn(
            'flex items-center px-2.5 py-2.5',
            collapsed ? 'flex-col gap-1.5 py-3' : 'gap-2.5',
          )}
        >
          <Avatar className={cn('shrink-0 ring-2 ring-sidebar-primary/15', collapsed ? 'h-8 w-8' : 'h-8 w-8')}>
            <AvatarFallback className="bg-sidebar-primary/12 text-sidebar-primary text-xs font-semibold">
              {initials}
            </AvatarFallback>
          </Avatar>
          {!collapsed && (
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] font-medium leading-tight">{user?.name}</p>
              <p className="truncate text-[11px] text-sidebar-foreground/45 capitalize">{user?.role}</p>
            </div>
          )}
          {!collapsed ? (
            <div className="flex shrink-0 gap-0.5">
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-sidebar-foreground/50 hover:text-sidebar-foreground hover:bg-sidebar-accent"
                onClick={() => navigate('/settings')}
                title="Settings"
              >
                <Settings className="h-3.5 w-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-sidebar-foreground/50 hover:text-destructive hover:bg-destructive/10"
                onClick={handleLogout}
                title="Log out"
              >
                <LogOut className="h-3.5 w-3.5" />
              </Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 text-sidebar-foreground/50 hover:text-destructive hover:bg-destructive/10"
              onClick={handleLogout}
              title="Log out"
            >
              <LogOut className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      </div>
    </aside>
  )
}
