import { useEffect, useRef, useState } from 'react'

export interface ContainerStatPoint {
  ts: number
  cpuPct: number
  memUsed: number
  memTotal: number
  memPct: number
  netIn: number
  netOut: number
  blkIn: number
  blkOut: number
  pids: number
}

export interface ContainerStatsState {
  latest: ContainerStatPoint | null
  history: ContainerStatPoint[]   // grows while user is on the page
  status: 'connecting' | 'running' | 'not_found' | 'error'
}

export function useContainerStatsSSE(
  projectId: string | undefined,
  serviceId: string | undefined,
  /** Only connect when true — lets us gate on the Stats tab being active */
  enabled: boolean,
): ContainerStatsState {
  const [state, setState] = useState<ContainerStatsState>({
    latest: null,
    history: [],
    status: 'connecting',
  })
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    if (!enabled || !projectId || !serviceId) return

    const token = localStorage.getItem('token')
    if (!token) return

    const url = `/api/projects/${projectId}/services/${serviceId}/stats/stream?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef.current = es

    es.addEventListener('stats', (e: MessageEvent) => {
      try {
        const raw = JSON.parse(e.data) as {
          name: string; cpu_pct: number; mem_used: number; mem_total: number
          mem_pct: number; net_in: number; net_out: number
          blk_in: number; blk_out: number; pids: number
          status: string; ts: number
        }
        const pt: ContainerStatPoint = {
          ts:       raw.ts,
          cpuPct:   raw.cpu_pct,
          memUsed:  raw.mem_used,
          memTotal: raw.mem_total,
          memPct:   raw.mem_pct,
          netIn:    raw.net_in,
          netOut:   raw.net_out,
          blkIn:    raw.blk_in,
          blkOut:   raw.blk_out,
          pids:     raw.pids,
        }
        setState(prev => ({
          latest: pt,
          history: [...prev.history, pt],
          status: raw.status === 'not_found' ? 'not_found' : 'running',
        }))
      } catch { /* ignore parse errors */ }
    })

    es.onerror = () => {
      setState(prev => ({ ...prev, status: 'error' }))
    }

    return () => {
      es.close()
      esRef.current = null
      setState({ latest: null, history: [], status: 'connecting' })
    }
  }, [enabled, projectId, serviceId])

  return state
}
