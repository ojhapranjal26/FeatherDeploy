/**
 * Shared date/duration formatting utilities.
 * All date functions accept an IANA timezone string so they
 * respect the user's preferred timezone from TimezoneContext.
 */

/** Format ISO date string as "Apr 23, 14:05" in the given timezone. */
export function formatDate(iso: string | undefined, tz: string): string {
  if (!iso) return '—'
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(new Date(iso))
  } catch {
    return new Date(iso).toLocaleString()
  }
}

/** Format ISO date string as full datetime including seconds. */
export function formatDateFull(iso: string | undefined, tz: string): string {
  if (!iso) return '—'
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(new Date(iso))
  } catch {
    return new Date(iso).toLocaleString()
  }
}

/** Format an epoch millisecond value as HH:mm in the given timezone. */
export function formatTimestamp(ms: number, tz: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }).format(ms)
  } catch {
    return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
  }
}

/** Format an epoch millisecond value as "Apr 23, 14:05" in the given timezone. */
export function formatTimestampFull(ms: number, tz: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(ms)
  } catch {
    return new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }
}

/** Format deployment duration as m:ss (e.g. "1:23"). Returns "—" for missing/negative. */
export function formatDuration(start?: string, end?: string): string {
  if (!start) return '—'
  const ms = new Date(end ?? Date.now()).getTime() - new Date(start).getTime()
  if (ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `${m}:${String(sec).padStart(2, '0')}`
}
