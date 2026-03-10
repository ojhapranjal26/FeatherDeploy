import { useEffect, useRef } from 'react'
import AnsiToHtml from 'ansi-to-html'
import type { LogLine } from '@/hooks/useDeploymentLogs'
import { cn } from '@/lib/utils'

const converter = new AnsiToHtml({ escapeXML: true })

interface LogViewerProps {
  lines: LogLine[]
  done: boolean
  className?: string
}

export function LogViewer({ lines, done, className }: LogViewerProps) {
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lines.length])

  return (
    <div
      className={cn(
        'relative rounded-lg border bg-zinc-950 text-zinc-100 font-mono text-xs p-4 overflow-y-auto',
        className
      )}
    >
      {lines.length === 0 && !done && (
        <span className="text-zinc-500 animate-pulse">Waiting for logs…</span>
      )}
      {lines.map((l, i) => (
        <div
          key={i}
          className="leading-5"
          dangerouslySetInnerHTML={{
            __html: converter.toHtml(l.line ?? ''),
          }}
        />
      ))}
      {done && (
        <div className="mt-2 text-zinc-500 border-t border-zinc-800 pt-2">
          ── deployment finished ──
        </div>
      )}
      <div ref={bottomRef} />
    </div>
  )
}
