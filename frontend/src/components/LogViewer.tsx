import { useEffect, useRef } from 'react'
import AnsiToHtml from 'ansi-to-html'
import type { LogLine } from '@/hooks/useDeploymentLogs'
import { cn } from '@/lib/utils'

const converter = new AnsiToHtml({ escapeXML: true })

// ─── Line classification ───────────────────────────────────────────────────────

type LineKind = 'error' | 'warn' | 'success' | 'step' | 'output' | 'normal'

function classifyLine(raw: string): LineKind {
  const trimmed = (raw ?? '').trimStart()
  if (/^(npm error|error:)/i.test(trimmed)) return 'error'
  if (/^(npm warn|warning:|warn:)/i.test(trimmed)) return 'warn'
  if (/^\[deploy\] deployment suc/i.test(trimmed)) return 'success'
  if (/^\[[a-z][^\]]*\]/i.test(trimmed)) return 'step'
  if (raw.startsWith('  ') || raw.startsWith('\t')) return 'output'
  return 'normal'
}

const KIND_DOT: Record<LineKind, string> = {
  error:   '✗',
  warn:    '◆',
  success: '✓',
  step:    '▶',
  output:  '·',
  normal:  '·',
}

const KIND_CLASS: Record<LineKind, string> = {
  error:   'text-red-400 bg-red-950/25',
  warn:    'text-yellow-400 bg-yellow-950/20',
  success: 'text-emerald-400 bg-emerald-950/25',
  step:    'text-sky-300',
  output:  'text-zinc-500',
  normal:  'text-zinc-300',
}

const DOT_CLASS: Record<LineKind, string> = {
  error:   'text-red-500',
  warn:    'text-yellow-500',
  success: 'text-emerald-500',
  step:    'text-sky-500',
  output:  'text-zinc-700',
  normal:  'text-zinc-700',
}

// ─── Component ────────────────────────────────────────────────────────────────

interface LogViewerProps {
  lines: LogLine[]
  done: boolean
  status?: string  // 'success' | 'failed' | undefined (in-progress)
  className?: string
}

export function LogViewer({ lines, done, status, className }: LogViewerProps) {
  const bottomRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom whenever new lines arrive, only if already near bottom
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (nearBottom) {
      el.scrollTop = el.scrollHeight
    }
  }, [lines.length])

  return (
    <div
      ref={containerRef}
      className={cn(
        'relative rounded-lg border border-zinc-800 bg-zinc-950 font-mono text-xs overflow-y-auto',
        className
      )}
    >
      <div className="p-3 space-y-px">
        {lines.length === 0 && !done && (
          <span className="text-zinc-600 animate-pulse">Waiting for logs…</span>
        )}
        {lines.map((l, i) => {
          const raw = l.line ?? ''
          const kind = classifyLine(raw)
          return (
            <div
              key={i}
              className={cn('flex gap-2 items-start leading-5 rounded px-1 py-0.5', KIND_CLASS[kind])}
            >
              <span
                className={cn('shrink-0 select-none w-3 text-center leading-5 mt-px', DOT_CLASS[kind])}
                aria-hidden
              >
                {KIND_DOT[kind]}
              </span>
              <span
                className="min-w-0 break-all"
                dangerouslySetInnerHTML={{ __html: converter.toHtml(raw) }}
              />
            </div>
          )
        })}
        {done && (
          <div className={cn(
            'mt-2 border-t pt-2 text-center text-[11px] select-none font-medium',
            status === 'success'
              ? 'border-emerald-900/60 text-emerald-600'
              : status === 'failed'
              ? 'border-red-900/60 text-red-600'
              : 'border-zinc-800 text-zinc-600',
          )}>
            {status === 'success' && '✓  deployment finished successfully'}
            {status === 'failed'  && '✗  deployment failed — see errors above'}
            {!status              && '── end of log ──'}
          </div>
        )}
      </div>
      <div ref={bottomRef} />
    </div>
  )
}
