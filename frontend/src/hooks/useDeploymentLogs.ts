import { useEffect, useState } from 'react'
import { MOCK_LOG_LINES } from '@/api/_mock'

export interface LogLine {
  line?: string
  ts?: string
  event?: string
  status?: string
}

export function useDeploymentLogs(
  _projectId: string,
  _serviceId: string,
  deploymentId: string
) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [done, setDone] = useState(false)

  useEffect(() => {
    if (!deploymentId) return
    setLines([])
    setDone(false)

    let index = 0
    const interval = setInterval(() => {
      if (index < MOCK_LOG_LINES.length) {
        setLines((prev) => [
          ...prev,
          { line: MOCK_LOG_LINES[index], ts: new Date().toISOString() },
        ])
        index++
      } else {
        setDone(true)
        clearInterval(interval)
      }
    }, 180)

    return () => clearInterval(interval)
  }, [deploymentId])

  return { lines, done }
}
