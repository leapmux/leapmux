/** Format a Date as a locale string with weekday and timezone. */
export function formatLocalDateTime(date: Date): string {
  const weekday = date.toLocaleDateString('en-US', { weekday: 'short' })
  return `${weekday}, ${date.toLocaleString('en-US', { timeZoneName: 'short' })}`
}
