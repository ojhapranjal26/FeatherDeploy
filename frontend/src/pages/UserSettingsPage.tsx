import { useState, useMemo } from 'react'
import { Globe, Clock, Check, Sun, Moon, Monitor, Search } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import { useTimezone } from '@/context/TimezoneContext'
import { useTheme } from '@/context/ThemeContext'
import { formatDateFull } from '@/lib/dateFormat'

// All IANA timezones grouped by region
const ALL_TIMEZONES: string[] = (() => {
  try {
    return (Intl as unknown as { supportedValuesOf: (k: string) => string[] })
      .supportedValuesOf('timeZone')
  } catch {
    // Fallback list of common timezones
    return [
      'UTC',
      'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
      'America/Anchorage', 'America/Honolulu', 'America/Toronto', 'America/Vancouver',
      'America/Mexico_City', 'America/Bogota', 'America/Sao_Paulo', 'America/Buenos_Aires',
      'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Rome', 'Europe/Madrid',
      'Europe/Amsterdam', 'Europe/Brussels', 'Europe/Zurich', 'Europe/Stockholm',
      'Europe/Oslo', 'Europe/Copenhagen', 'Europe/Helsinki', 'Europe/Warsaw',
      'Europe/Prague', 'Europe/Vienna', 'Europe/Budapest', 'Europe/Bucharest',
      'Europe/Athens', 'Europe/Istanbul', 'Europe/Moscow', 'Europe/Kyiv',
      'Asia/Dubai', 'Asia/Karachi', 'Asia/Kolkata', 'Asia/Colombo', 'Asia/Dhaka',
      'Asia/Yangon', 'Asia/Bangkok', 'Asia/Jakarta', 'Asia/Singapore', 'Asia/Manila',
      'Asia/Hong_Kong', 'Asia/Taipei', 'Asia/Shanghai', 'Asia/Seoul', 'Asia/Tokyo',
      'Australia/Perth', 'Australia/Adelaide', 'Australia/Melbourne', 'Australia/Sydney',
      'Pacific/Auckland', 'Pacific/Fiji', 'Africa/Cairo', 'Africa/Nairobi',
      'Africa/Lagos', 'Africa/Johannesburg',
    ]
  }
})()

function getRegion(tz: string): string {
  const slash = tz.indexOf('/')
  return slash !== -1 ? tz.slice(0, slash) : 'Other'
}

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

export function UserSettingsPage() {
  const { timezone, setTimezone } = useTimezone()
  const { theme, setTheme } = useTheme()

  const [search, setSearch] = useState('')
  const [pending, setPending] = useState(timezone)

  const filtered = useMemo(() => {
    const q = search.toLowerCase().replace(/\s+/g, '_')
    return q ? ALL_TIMEZONES.filter(tz => tz.toLowerCase().includes(q)) : ALL_TIMEZONES
  }, [search])

  // Group by region
  const grouped = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const tz of filtered) {
      const r = getRegion(tz)
      if (!map.has(r)) map.set(r, [])
      map.get(r)!.push(tz)
    }
    return map
  }, [filtered])

  const handleSave = () => {
    setTimezone(pending)
    toast.success(`Timezone set to ${pending}`)
  }

  const now = new Date().toISOString()
  const previewTime = formatDateFull(now, pending)

  const THEMES = [
    { value: 'light', label: 'Light', icon: Sun },
    { value: 'dark',  label: 'Dark',  icon: Moon },
    { value: 'system', label: 'System', icon: Monitor },
  ] as const

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">My Preferences</h1>
        <p className="mt-1 text-sm text-muted-foreground">Personal display preferences — stored in your browser.</p>
      </div>

      {/* ── Timezone ──────────────────────────────────────────────────────── */}
      <section className="rounded-xl border bg-card p-6 space-y-5">
        <SectionHeader
          icon={Globe}
          title="Timezone"
          desc="All dates and times shown in the panel will be converted to this timezone."
        />

        {/* Preview */}
        <div className="flex items-center gap-2 rounded-lg border bg-muted/30 px-4 py-2.5 text-sm">
          <Clock className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="text-muted-foreground">Current time in <strong className="text-foreground font-mono">{pending}</strong>:</span>
          <span className="font-mono font-medium ml-1">{previewTime}</span>
        </div>

        {/* Search */}
        <div className="space-y-1.5">
          <Label className="text-xs">Search timezone</Label>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground pointer-events-none" />
            <Input
              className="pl-8 h-8 text-xs"
              placeholder="e.g. Asia/Kolkata, New_York, UTC…"
              value={search}
              onChange={e => setSearch(e.target.value)}
            />
          </div>
        </div>

        {/* Scrollable timezone list */}
        <div className="rounded-lg border overflow-hidden">
          <div className="max-h-72 overflow-y-auto divide-y">
            {grouped.size === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">No timezones match "{search}"</p>
            ) : (
              Array.from(grouped.entries()).map(([region, tzs]) => (
                <div key={region}>
                  <div className="sticky top-0 bg-muted/80 backdrop-blur-sm px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground border-b">
                    {region}
                  </div>
                  {tzs.map(tz => (
                    <button
                      key={tz}
                      type="button"
                      onClick={() => setPending(tz)}
                      className={cn(
                        'flex w-full items-center justify-between px-4 py-2 text-sm transition-colors hover:bg-muted/40',
                        pending === tz && 'bg-primary/8 text-primary font-medium',
                      )}
                    >
                      <span>{tz.replace(/_/g, ' ')}</span>
                      {pending === tz && <Check className="h-3.5 w-3.5 shrink-0" />}
                    </button>
                  ))}
                </div>
              ))
            )}
          </div>
        </div>

        <div className="flex items-center justify-between">
          <p className="text-xs text-muted-foreground">
            Currently saved: <span className="font-mono">{timezone}</span>
          </p>
          <Button size="sm" onClick={handleSave} disabled={pending === timezone} className="gap-1.5">
            <Check className="h-3.5 w-3.5" />
            Save timezone
          </Button>
        </div>
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
