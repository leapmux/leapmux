/** Format a Date as a locale string with weekday and timezone. */
export function formatLocalDateTime(date: Date): string {
  const weekday = date.toLocaleDateString('en-US', { weekday: 'short' })
  return `${weekday}, ${date.toLocaleString('en-US', { timeZoneName: 'short' })}`
}

/**
 * Format how long ago an instant was, compactly: `3s`, `12m`, `2h`, `5d`,
 * `3mo`, `2y`.
 *
 * Lives here rather than inside `~/components/chat/RelativeTime`, which used to
 * own it, because two callers need the same rule in different forms. The
 * component renders it as an element that ticks; a `LoadingMenu` option label
 * is a plain STRING and its filter runs a substring match over that string, so
 * a menu row has to have the text itself.
 *
 * `now` is a parameter so a caller can state the instant it is comparing
 * against, and so a test needs no fake timer.
 */
export function formatCompactAge(ts: Date, now: Date = new Date()): string {
  // Clamped at zero: a clock that moved backwards, or a store whose timestamp
  // is a moment ahead of this machine, must read as "just now" rather than as a
  // negative age.
  const diffSec = Math.max(0, Math.floor((now.getTime() - ts.getTime()) / 1000))
  if (diffSec < 60)
    return `${diffSec}s`
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60)
    return `${diffMin}m`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24)
    return `${diffHr}h`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30)
    return `${diffDay}d`
  const diffMo = Math.floor(diffDay / 30)
  if (diffMo < 12)
    return `${diffMo}mo`
  const diffYr = Math.floor(diffDay / 365)
  return `${diffYr}y`
}
