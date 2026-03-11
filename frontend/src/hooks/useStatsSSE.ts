import { useEffect, useRef, useState } from 'react'
import type { ClusterBrain } from '@/api/nodes'

export interface NodeStats {
  id: number
  name: string
  status: string
  cpu_usage: number
  ram_used: number
  ram_total: number
  disk_used: number
  disk_total: number
  last_stats_at: string | null
  node_id: string
}

export interface LiveStats {
  brain: ClusterBrain | null
  nodes: NodeStats[]
}

/**
 * useStatsSSE connects to GET /api/stats/stream and returns live brain + node
 * stats.  The browser's native EventSource is used (no external WebSocket lib).
 * JWT is passed as ?token= because EventSource cannot set custom headers.
 */
export function useStatsSSE(): LiveStats & { connected: boolean } {
  const [stats, setStats] = useState<LiveStats>({ brain: null, nodes: [] })
  const [connected, setConnected] = useState(false)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const token = localStorage.getItem('token')
    if (!token) return

    const url = `/api/stats/stream?token=${encodeURIComponent(token)}`

    const es = new EventSource(url)
    esRef.current = es

    es.addEventListener('stats', (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data) as LiveStats
        setStats(data)
        setConnected(true)
      } catch {
        // ignore malformed frames
      }
    })

    es.onerror = () => {
      setConnected(false)
    }

    return () => {
      es.close()
      esRef.current = null
      setConnected(false)
    }
  }, [])

  return { ...stats, connected }
}
