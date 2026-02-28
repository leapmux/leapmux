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
