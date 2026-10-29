/**
 * The composer's schedule conversion and validation, as pure functions.
 *
 * A schedule travels in three shapes, and each conversion has exactly one
 * home here:
 *
 * - the FIELD value: `datetime-local` (`2030-01-01T18:45` or
 *   `2030-01-01T18:45:30.123`), which the browser reads and writes in the
 *   user's local timezone;
 * - the WIRE value: `Date.toISOString()` (`2030-01-01T09:45:30.123Z`), which
 *   `EnqueueAgentInputRequest.scheduled_at` carries;
 * - the STORED value: the same wire form on a queued item.
 *
 * A UTC string is NEVER converted back by slicing characters: the local time
 * of an instant depends on the browser's timezone, so the round trip goes
 * through a `Date` and the local date getters.
 */

/** The datetime-local field value of no schedule at all. */
export const EMPTY_SCHEDULE = ''

/**
 * A `datetime-local` value. The browser writes the shortest form of the time,
 * so it omits the seconds when they and the fraction are zero, and it trims
 * the fraction to the digits that it needs.
 */
const LOCAL_FIELD = /^(\d{4,6})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?$/

/** Parse a `datetime-local` value as browser-local time, or undefined. */
export function parseLocalSchedule(value: string): Date | undefined {
  const match = LOCAL_FIELD.exec(value)
  if (!match)
    return undefined
  const [year, month, day, hours, minutes] = match.slice(1, 6).map(Number) as [number, number, number, number, number]
  const seconds = Number(match[6] ?? 0)
  const milliseconds = Number((match[7] ?? '').padEnd(3, '0'))
  const at = new Date(year, month - 1, day, hours, minutes, seconds, milliseconds)
  // The constructor reads a year below 100 as 1900 plus that year.
  at.setFullYear(year, month - 1, day)
  // A nonexistent or out-of-range local time does not fail: the constructor
  // normalizes it to another instant. 02:30 on a spring-forward day becomes
  // 03:30, and February 30 becomes March 2. The local components of that
  // instant then disagree with the field, which this comparison catches.
  if (at.getFullYear() !== year || at.getMonth() !== month - 1 || at.getDate() !== day
    || at.getHours() !== hours || at.getMinutes() !== minutes || at.getSeconds() !== seconds
    || at.getMilliseconds() !== milliseconds) {
    return undefined
  }
  return at
}

/**
 * The `datetime-local` value of one instant in the browser's timezone,
 * seconds and milliseconds included.
 */
export function localFieldValueOf(at: Date): string {
  const pad = (n: number, width = 2) => String(n).padStart(width, '0')
  return `${pad(at.getFullYear(), 4)}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`
    + `T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}.${pad(at.getMilliseconds(), 3)}`
}

/**
 * The field value of a stored schedule, converted through the local date
 * getters so the browser's timezone -- never a character offset -- decides
 * what the field shows.
 */
export function datetimeLocalOf(stored: string): string {
  if (!stored)
    return EMPTY_SCHEDULE
  const at = new Date(stored)
  if (Number.isNaN(at.getTime()))
    return EMPTY_SCHEDULE
  return localFieldValueOf(at)
}

/**
 * Whether a draft still holds a stored schedule: either the field value that
 * the stored instant renders as, or a value that parses to the same instant.
 * An empty draft holds an empty stored value.
 *
 * The first test matters in the repeated hour of a fall-back day. The field
 * shows that hour once, so the later of its two instants renders to a value
 * that parses back to the EARLIER one, and only the rendered value proves
 * that the draft is unchanged.
 */
function holdsStoredSchedule(draft: string, stored: string): boolean {
  const rendered = datetimeLocalOf(stored)
  if (draft === rendered)
    return true
  const at = parseLocalSchedule(draft)
  return at !== undefined && localFieldValueOf(at) === rendered
}

/**
 * What a draft schedule is, decided at apply time and again at send time.
 *
 * Each verdict that the composer can send carries its wire value, so the send
 * reads the value from the same parse that judged the draft. An `invalid`
 * draft carries none, so it can never reach the wire as "no schedule".
 */
export type ScheduleCheck
  = | { verdict: 'empty', wire: '' }
  /**
   * The draft still holds the stored schedule of the edited item. The wire
   * value is the stored value itself, and a save keeps the stored schedule.
   */
    | { verdict: 'unchanged', wire: string }
    | { verdict: 'valid', wire: string }
  /** Unparseable, normalized-invalid, or a nonexistent local time. */
    | { verdict: 'invalid' }
  /** In the past (or exactly now), where only an UNCHANGED stored value passes. */
    | { verdict: 'notFuture', wire: string }

/**
 * Validate one draft against the clock, and give its wire value.
 *
 * `existingStored` is the schedule that the item already holds while a queue
 * EDIT is in flight. A draft that still holds it is `unchanged`, even a past
 * deadline: that is already immediate queue data, so saving it back stays
 * legal. A NEWLY chosen past time is a schedule that nobody could have wanted,
 * because it delivers the moment the queue drains.
 */
export function checkSchedule(draft: string, now: Date, existingStored?: string): ScheduleCheck {
  if (existingStored !== undefined && holdsStoredSchedule(draft, existingStored))
    return { verdict: 'unchanged', wire: existingStored }
  if (draft === EMPTY_SCHEDULE)
    return { verdict: 'empty', wire: '' }
  const at = parseLocalSchedule(draft)
  if (at === undefined)
    return { verdict: 'invalid' }
  // RFC3339 requires four UTC year digits. A valid local year can cross
  // that limit when its timezone offset converts the instant to UTC.
  if (at.getUTCFullYear() < 0 || at.getUTCFullYear() > 9999)
    return { verdict: 'invalid' }
  const wire = at.toISOString()
  return at.getTime() > now.getTime()
    ? { verdict: 'valid', wire }
    : { verdict: 'notFuture', wire }
}

/** The message that states why a draft cannot be applied or sent. */
export function scheduleRefusalMessage(verdict: 'invalid' | 'notFuture'): string {
  return verdict === 'invalid'
    ? 'Enter a real local date and time.'
    : 'Pick a time in the future.'
}
