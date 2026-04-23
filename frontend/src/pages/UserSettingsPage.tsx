import { Globe, Check, Sun, Moon, Monitor } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useTimezone } from '@/context/TimezoneContext'
import { useTheme } from '@/context/ThemeContext'

// ─── Section header ───────────────────────────────────────────────────────────
function SectionHeader({ icon: Icon, title, desc }: { icon: React.ElementType; title: string; desc: string }) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary shrink-0">
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <p className="font-medium leading-tight">{title}</p>
        <p className="text-sm text-muted-foreground mt-0.5">{desc}</p>
      </div>
    </div>
  )
}

const THEMES = [
  { value: 'light',  label: 'Light',  icon: Sun },
  { value: 'dark',   label: 'Dark',   icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
] as const

export function UserSettingsPage() {
  const { timezone } = useTimezone()
  const { theme, setTheme } = useTheme()

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">My Preferences</h1>
        <p className="mt-1 text-sm text-muted-foreground">Personal display preferences — stored in your browser.</p>
      </div>

      {/* ── Timezone (read-only — set by a superadmin in System Settings) ── */}
      <section className="rounded-xl border bg-card p-6 space-y-4">
        <SectionHeader
          icon={Globe}
          title="Platform Timezone"
          desc="All dates and times in the panel use the timezone configured by your administrator."
        />
        <div className="flex items-center gap-2 rounded-lg border bg-muted/30 px-4 py-2.5 text-sm">
          <Globe className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="text-muted-foreground">Active timezone:</span>
          <span className="font-mono font-medium ml-1">{timezone}</span>
        </div>
        <p className="text-xs text-muted-foreground">
          To change this, ask a superadmin to update it under <strong>System Settings → Timezone</strong>.
        </p>
      </section>

      {/* ── Appearance ────────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <SectionHeader
          icon={Monitor}
          title="Appearance"
          desc="Choose how the panel looks. System follows your OS setting."
        />
        <div className="grid grid-cols-3 gap-3">
          {THEMES.map(({ value, label, icon: Icon }) => (
            <button
              key={value}
              type="button"
              onClick={() => setTheme(value)}
              className={cn(
                'flex flex-col items-center gap-2 rounded-xl border-2 p-4 text-sm transition-all',
                theme === value
                  ? 'border-primary bg-primary/8 text-primary font-medium'
                  : 'border-border hover:border-muted-foreground/40 text-muted-foreground',
              )}
            >
              <Icon className="h-5 w-5" />
              {label}
              {theme === value && <Check className="h-3.5 w-3.5" />}
            </button>
          ))}
        </div>
      </section>
    </div>
  )
}
