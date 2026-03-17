/** Format a Date as a locale string with weekday (e.g. "Wed, 3/18/2026, 2:30:00 PM"). */
export function formatLocalDateTime(date: Date): string {
  const weekday = date.toLocaleDateString('en-US', { weekday: 'short' })
  return `${weekday}, ${date.toLocaleString('en-US')}`
}
