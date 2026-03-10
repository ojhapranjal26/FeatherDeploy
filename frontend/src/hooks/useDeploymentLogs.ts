import { useEffect, useState } from 'react'

export interface LogLine {
  line?: string
  ts?: string
  event?: string
  status?: string
}

export function useDeploymentLogs(
  projectId: string,
  serviceId: string,
  deploymentId: string
) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [done, setDone] = useState(false)

  useEffect(() => {
    if (!projectId || !serviceId || !deploymentId) return
    setLines([])
    setDone(false)

    const token = localStorage.getItem('token')
    const url = `/api/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}/logs${token ? `?token=${encodeURIComponent(token)}` : ''}`
    const es = new EventSource(url)

    es.onmessage = (e) => {
      if (e.data) {
        setLines((prev) => [...prev, { line: e.data, ts: new Date().toISOString() }])
      }
    }

    es.addEventListener('done', () => {
      setDone(true)
      es.close()
    })

    es.onerror = () => {
      setDone(true)
      es.close()
    }

    return () => es.close()
  }, [projectId, serviceId, deploymentId])

  return { lines, done }
}

