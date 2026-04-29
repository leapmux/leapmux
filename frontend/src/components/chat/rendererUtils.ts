/** Shared utility functions for message renderers. */

/** Format task status for display. */
export function formatTaskStatus(status?: string): string {
  if (!status)
    return 'Waiting for output'
  if (status === 'completed')
    return 'Complete'
  if (status === 'failed')
    return 'Failed'
  return capitalize(status)
}

/** Capitalize the first character; leaves the rest unchanged. */
export function capitalize(s: string): string {
  return s.length > 0 ? s.charAt(0).toUpperCase() + s.slice(1) : s
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

/** Format a token count with a fixed decimal (e.g. 1.0k, 12.3M). */
export function formatTokenCount(n: number): string {
  if (n >= 1_000_000)
    return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000)
    return `${(n / 1_000).toFixed(1)}k`
  return String(n)
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

/**
 * Join the truthy entries with ` · `, dropping empty/falsy parts. Lets call
 * sites express optional summary fields as a flat array instead of building
 * one with imperative `parts.push(...)` + `if` guards.
 */
export function joinMetaParts(parts: ReadonlyArray<string | false | null | undefined>): string {
  return parts.filter((p): p is string => !!p).join(' · ')
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
