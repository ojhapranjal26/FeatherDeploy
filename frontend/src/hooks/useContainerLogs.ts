import { useEffect, useState } from 'react'

const MAX_LOG_LINES = 2000

export function useContainerLogs(
  projectId: string | undefined,
  serviceId: string | undefined,
  enabled: boolean
) {
  const [lines, setLines] = useState<string[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    if (!enabled || !projectId || !serviceId) {
      setLines([])
      setConnected(false)
      return
    }

    setLines([])
    setConnected(false)

    const token = localStorage.getItem('token')
    const url = `/api/projects/${projectId}/services/${serviceId}/container-logs${token ? `?token=${encodeURIComponent(token)}` : ''}`
    const es = new EventSource(url)

    es.onopen = () => setConnected(true)

    es.onmessage = (e) => {
      if (e.data) {
        setLines((prev) => {
          const next = [...prev, e.data as string]
          // Keep only the most recent MAX_LOG_LINES lines to avoid memory growth
          return next.length > MAX_LOG_LINES ? next.slice(next.length - MAX_LOG_LINES) : next
        })
      }
    }

    es.addEventListener('done', () => {
      setConnected(false)
      es.close()
    })

    es.onerror = () => {
      setConnected(false)
      es.close()
    }

    return () => {
      es.close()
      setConnected(false)
    }
  }, [projectId, serviceId, enabled])

  return { lines, connected }
}
