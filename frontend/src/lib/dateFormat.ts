/** Format a Date as a locale string with weekday and timezone. */
export function formatLocalDateTime(date: Date): string {
  const weekday = date.toLocaleDateString('en-US', { weekday: 'short' })
  return `${weekday}, ${date.toLocaleString('en-US', { timeZoneName: 'short' })}`
}

/**
 * The unit ladder `formatCompactAge` walks, smallest first.
 *
 * Each unit's threshold IS its divisor, which is what makes an unreachable
 * interval impossible. A ladder whose steps test one quantity and print another
 * leaves a gap: a 30-day month tested against a 365-day year sent every age
 * from 360 to 364 days past `mo` and out of `y` as `0y`, so a session eleven
 * and a half months old read as "just now".
 */
const AGE_UNITS = [
  { seconds: 1, suffix: 's' },
  { seconds: 60, suffix: 'm' },
  { seconds: 60 * 60, suffix: 'h' },
  { seconds: 24 * 60 * 60, suffix: 'd' },
  { seconds: 30 * 24 * 60 * 60, suffix: 'mo' },
  { seconds: 365 * 24 * 60 * 60, suffix: 'y' },
] as const

/**
 * The widest text that `formatCompactAge` gives for an age below 100 years: two digits and
 * the longest suffix of the ladder. Every unit below a year counts to at most two digits,
 * and only the year can reach three. The longest suffix, `mo`, starts with `m`, the widest
 * letter of the single-letter suffixes. So in tabular digits no other age is wider, and a box
 * that holds this text holds every age: a layout keeps one width while an age ticks from
 * `9s` to `10s` or from `59s` to `1m`.
 */
export const WIDEST_COMPACT_AGE = `00${AGE_UNITS.reduce<string>((longest, unit) => unit.suffix.length > longest.length
  ? unit.suffix
  : longest, '')}`

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
 * It TRUNCATES, so an age never reads ahead of itself: 47 hours is `1d`.
 *
 * `now` is a parameter so a caller can state the instant that it compares
 * against, and so a test needs no fake timer.
 */
export function formatCompactAge(ts: Date, now: Date = new Date()): string {
  // Clamped at zero: a clock that moved backwards, or a store whose timestamp
  // is a moment ahead of this machine, must read as "just now" rather than as a
  // negative age.
  const diffSec = Math.max(0, Math.floor((now.getTime() - ts.getTime()) / 1000))
  // Largest unit first, so the first unit the age reaches is the one printed.
  // Index 0 is the fallback rather than a step, so an age below one minute
  // still prints seconds — including `0s`.
  for (let i = AGE_UNITS.length - 1; i > 0; i--) {
    const unit = AGE_UNITS[i]
    if (unit !== undefined && diffSec >= unit.seconds)
      return `${Math.floor(diffSec / unit.seconds)}${unit.suffix}`
  }
  return `${diffSec}s`
}
