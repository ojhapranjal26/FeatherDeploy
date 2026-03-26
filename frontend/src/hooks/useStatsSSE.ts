import { useEffect, useRef, useState, useCallback } from 'react'
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
 * stats. Reconnects automatically with backoff. Connection stays alive until
 * the component unmounts (i.e. user leaves the tab).
 */
export function useStatsSSE(): LiveStats & { connected: boolean } {
  const [stats, setStats] = useState<LiveStats>({ brain: null, nodes: [] })
  const [connected, setConnected] = useState(false)
  const destroyedRef = useRef(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const esRef = useRef<EventSource | null>(null)

  const connect = useCallback(() => {
    if (destroyedRef.current) return
    const token = localStorage.getItem('token')
    if (!token) return

    const url = `/api/stats/stream?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef.current = es

    es.addEventListener('stats', (e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data) as LiveStats
        setStats({ brain: data.brain ?? null, nodes: Array.isArray(data.nodes) ? data.nodes : [] })
        setConnected(true)
      } catch { /* ignore */ }
    })

    es.onerror = () => {
      es.close()
      esRef.current = null
      setConnected(false)
      if (!destroyedRef.current) {
        timerRef.current = setTimeout(() => connect(), 4000)
      }
    }
  }, [])

  useEffect(() => {
    destroyedRef.current = false
    connect()
    return () => {
      destroyedRef.current = true
      if (timerRef.current) clearTimeout(timerRef.current)
      esRef.current?.close()
      esRef.current = null
      setConnected(false)
    }
  }, [connect])

  return { ...stats, connected }
}

