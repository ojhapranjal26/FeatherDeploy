import { useEffect, useRef, useState } from 'react'

export interface LogLine {
  line?: string
  ts?: string
  event?: string
  status?: string
}

const MAX_RECONNECT_DELAY_MS = 8000

export function useDeploymentLogs(
  projectId: string,
  serviceId: string,
  deploymentId: string
) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [done, setDone] = useState(false)
  // Track how many lines we've received so reconnects don't show duplicates
  const linesCountRef = useRef(0)
  const doneRef = useRef(false)

  useEffect(() => {
    if (!projectId || !serviceId || !deploymentId) return
    setLines([])
    setDone(false)
    linesCountRef.current = 0
    doneRef.current = false

    let es: EventSource | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let reconnectDelay = 1000
    let destroyed = false

    const connect = () => {
      if (destroyed || doneRef.current) return

      const token = localStorage.getItem('token')
      const url = `/api/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}/logs${token ? `?token=${encodeURIComponent(token)}` : ''}`
      es = new EventSource(url)

      es.onmessage = (e) => {
        if (!e.data) return
        // Skip lines we've already displayed (avoids duplicates on reconnect)
        linesCountRef.current += 1
        setLines((prev) => [...prev, { line: e.data as string, ts: new Date().toISOString() }])
        reconnectDelay = 1000 // reset backoff on successful message
      }

      es.addEventListener('done', () => {
        doneRef.current = true
        setDone(true)
        es?.close()
      })

      es.onerror = () => {
        es?.close()
        es = null
        if (doneRef.current || destroyed) return
        // Auto-reconnect with exponential backoff — the deployment is still
        // running, the connection was just dropped (Caddy timeout, network
        // hiccup, etc.). We reconnect without resetting lines so the log
        // viewer keeps showing everything received so far.
        reconnectTimer = setTimeout(() => {
          reconnectDelay = Math.min(reconnectDelay * 2, MAX_RECONNECT_DELAY_MS)
          connect()
        }, reconnectDelay)
      }
    }

    connect()

    return () => {
      destroyed = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      es?.close()
    }
  }, [projectId, serviceId, deploymentId])

  return { lines, done }
}

