/**
 * Shared date/duration formatting utilities.
 * All date functions accept an IANA timezone string so they
 * respect the user's preferred timezone from TimezoneContext.
 */

/**
 * Guard against zero-time values that the backend may emit when a nullable
 * datetime column (e.g. started_at) hasn't been set yet. Dates before 2000
 * are treated as absent so we fall back to '—' or a sibling field.
 */
function isValidDate(d: Date): boolean {
  return !isNaN(d.getTime()) && d.getFullYear() >= 2000
}

/** Format ISO date string as "Apr 23, 14:05" in the given timezone. */
export function formatDate(iso: string | undefined, tz: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (!isValidDate(d)) return '—'
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(d)
  } catch {
    return d.toLocaleString(undefined, { timeZone: tz, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }
}

/** Format ISO date string as full datetime including seconds. */
export function formatDateFull(iso: string | undefined, tz: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (!isValidDate(d)) return '—'
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(d)
  } catch {
    return d.toLocaleString(undefined, { timeZone: tz, year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
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
    return new Date(ms).toLocaleTimeString(undefined, { timeZone: tz, hour: '2-digit', minute: '2-digit', hour12: false })
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
    return new Date(ms).toLocaleString(undefined, { timeZone: tz, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }
}

/**
 * Format deployment duration as m:ss (e.g. "1:23"). Returns "—" for missing/negative.
 * Pass `nowMs` (from a live-ticking state) to get a continuously updating display for
 * active deployments. When `end` is provided it takes precedence over `nowMs`.
 */
export function formatDuration(start?: string, end?: string, nowMs?: number): string {
  if (!start) return '—'
  const startMs = new Date(start).getTime()
  if (isNaN(startMs) || new Date(start).getFullYear() < 2000) return '—'
  const endMs = end ? new Date(end).getTime() : (nowMs ?? Date.now())
  const ms = endMs - startMs
  if (ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  const m = Math.floor(s / 60)
  const sec = s % 60
  return `${m}:${String(sec).padStart(2, '0')}`
}
