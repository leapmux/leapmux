/** Shared utility functions for message renderers. */

import { Formatter, FracturedJsonOptions } from 'fracturedjsonjs'

const formatter = new Formatter()
const fmtOpts = new FracturedJsonOptions()
fmtOpts.MaxTotalLineLength = 80
fmtOpts.MaxInlineComplexity = 1
formatter.Options = fmtOpts

/** Pretty-print a JSON string using FracturedJson for readable formatting. */
export function prettifyJson(raw: string): string {
  try {
    return formatter.Reformat(raw)
  }
  catch {
    return raw
  }
}

/** Format task status for display. */
export function formatTaskStatus(status?: string): string {
  if (!status)
    return 'Waiting for output'
  if (status === 'completed')
    return 'Complete'
  if (status === 'failed')
    return 'Failed'
  return status.charAt(0).toUpperCase() + status.slice(1)
}

/** Return the first non-empty trimmed line from text, or null. */
export function firstNonEmptyLine(text?: string): string | null {
  if (!text)
    return null
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (trimmed)
      return trimmed
  }
  return null
}

/** Format a duration in milliseconds as a human-readable string (e.g. "5ms", "3.2s", "2m 30s", "1h 5m"). */
export function formatDuration(ms: number): string {
  if (ms < 1000)
    return `${Math.round(ms)}ms`

  const totalSeconds = ms / 1000
  if (totalSeconds < 10)
    return `${totalSeconds.toFixed(1)}s`

  const totalSecondsRounded = Math.round(totalSeconds)
  const days = Math.floor(totalSecondsRounded / 86400)
  const hours = Math.floor((totalSecondsRounded % 86400) / 3600)
  const minutes = Math.floor((totalSecondsRounded % 3600) / 60)
  const seconds = totalSecondsRounded % 60

  const parts: string[] = []
  if (days > 0)
    parts.push(`${days}d`)
  if (hours > 0)
    parts.push(`${hours}h`)
  if (minutes > 0)
    parts.push(`${minutes}m`)
  if (seconds > 0 || parts.length === 0)
    parts.push(`${seconds}s`)
  return parts.join(' ')
}

/** Format a number with locale-aware separators (e.g. 1,234). */
export function formatNumber(n: number): string {
  return n.toLocaleString('en-US')
}

/** Format a number in compact form (e.g. 1.2k, 3.5m, 1.1g). */
export function formatCompactNumber(n: number): string {
  if (n < 1000)
    return String(n)
  if (n < 1_000_000) {
    const v = n / 1000
    return `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))}k`
  }
  if (n < 1_000_000_000) {
    const v = n / 1_000_000
    return `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))}m`
  }
  const v = n / 1_000_000_000
  return `${v >= 100 ? Math.round(v) : Number(v.toFixed(1))}g`
}

/** Format a Grep result summary for display (without trailing colon). */
export function formatGrepSummary(numFiles?: number, numLines?: number, fallback?: string | null): string | null {
  if (numFiles === undefined && numLines === undefined)
    return fallback || null
  const nf = numFiles ?? 0
  const nl = numLines ?? 0
  if (nf <= 0 && nl <= 0)
    return fallback || 'No matches found'
  const parts: string[] = []
  if (nf > 0)
    parts.push(`${nf} file${nf === 1 ? '' : 's'}`)
  if (nl > 0)
    parts.push(`${nl} line${nl === 1 ? '' : 's'}`)
  return `Found ${parts.join(' and ')}`
}

/** Format a Glob result summary for display. Parts are joined with " · ". */
export function formatGlobSummary(numFiles?: number, durationMs?: number, truncated?: boolean, fallback?: string | null): string | null {
  if (numFiles === undefined)
    return fallback || null
  const parts: string[] = []
  if (numFiles <= 0)
    parts.push(fallback || 'No files found')
  else
    parts.push(`Found ${numFiles} file${numFiles === 1 ? '' : 's'}`)
  if (durationMs !== undefined)
    parts.push(`Took ${formatDuration(durationMs)}`)
  if (truncated)
    parts.push('Result truncated')
  return parts.join(' \u00B7 ')
}

/** Helper: format tool input for compact display (fallback for unknown tools) */
export function formatToolInput(input: unknown): string {
  if (input === null || input === undefined || JSON.stringify(input) === '{}') {
    return '()'
  }
  const json = JSON.stringify(input)
  if (json.length < 50) {
    return `(${json})`
  }
  return '({...})'
}
