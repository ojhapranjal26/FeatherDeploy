import { createContext, useContext, useState } from 'react'
import type { ReactNode } from 'react'

const STORAGE_KEY = 'preferred-timezone'
const DEFAULT_TZ = Intl.DateTimeFormat().resolvedOptions().timeZone

interface TimezoneContextValue {
  timezone: string
  setTimezone: (tz: string) => void
}

const TimezoneContext = createContext<TimezoneContextValue | null>(null)

export function TimezoneProvider({ children }: { children: ReactNode }) {
  const [timezone, setTimezoneState] = useState<string>(
    () => localStorage.getItem(STORAGE_KEY) ?? DEFAULT_TZ,
  )

  const setTimezone = (tz: string) => {
    localStorage.setItem(STORAGE_KEY, tz)
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
