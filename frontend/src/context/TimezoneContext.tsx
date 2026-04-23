import { createContext, useContext, useState, useEffect } from 'react'
import type { ReactNode } from 'react'
import { settingsApi } from '@/api/settings'

// Cache key stores the last known platform timezone so it is available
// immediately on page load (avoids a flash of wrong timezone).
const CACHE_KEY = 'platform-timezone'
const DEFAULT_TZ = Intl.DateTimeFormat().resolvedOptions().timeZone

interface TimezoneContextValue {
  timezone: string
  setTimezone: (tz: string) => void
}

const TimezoneContext = createContext<TimezoneContextValue | null>(null)

export function TimezoneProvider({ children }: { children: ReactNode }) {
  const [timezone, setTimezoneState] = useState<string>(
    () => localStorage.getItem(CACHE_KEY) ?? DEFAULT_TZ,
  )

  // Fetch the globally configured platform timezone from the backend.
  useEffect(() => {
    settingsApi.getTimezone()
      .then(tz => {
        if (tz) {
          localStorage.setItem(CACHE_KEY, tz)
          setTimezoneState(tz)
        }
      })
      .catch(() => {/* keep cached / browser timezone on error */})
  }, [])

  // Update local state + cache (the actual API call is done by the caller).
  const setTimezone = (tz: string) => {
    localStorage.setItem(CACHE_KEY, tz)
    setTimezoneState(tz)
  }

  return (
    <TimezoneContext.Provider value={{ timezone, setTimezone }}>
      {children}
    </TimezoneContext.Provider>
  )
}

export function useTimezone() {
  const ctx = useContext(TimezoneContext)
  if (!ctx) throw new Error('useTimezone must be used inside <TimezoneProvider>')
  return ctx
}
