/** Shared utility functions for message renderers. */

/** Format task status for display. */
export function formatTaskStatus(status?: string): string {
  if (!status)
    return 'Pending'
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

/** Format a duration in milliseconds as a human-readable string (e.g. "5s", "2m 30s"). */
export function formatDuration(ms: number): string {
  const totalSec = Math.round(ms / 1000)
  if (totalSec < 60)
    return `${totalSec}s`
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  return sec > 0 ? `${min}m ${sec}s` : `${min}m`
}

/** Format a number with locale-aware separators (e.g. 1,234). */
export function formatNumber(n: number): string {
  return n.toLocaleString('en-US')
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
