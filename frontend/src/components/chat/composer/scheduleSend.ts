/**
 * The composer's schedule conversion and validation, as pure functions.
 *
 * A schedule travels in three shapes, and each conversion has exactly one
 * home here:
 *
 * - the FIELD value: `datetime-local` (`2030-01-01T18:45:30.123`), which the
 *   browser reads and writes in the user's local timezone;
 * - the WIRE value: `Date.toISOString()` (`2030-01-01T09:45:30.123Z`), which
 *   `EnqueueAgentInputRequest.scheduled_at` carries;
 * - the STORED value: the same wire form on a queued item.
 *
 * A UTC string is NEVER converted back by slicing characters: the local
 * instant it names depends on the browser's timezone, so the round trip goes
 * through a `Date` and the local date getters.
 */

/** The datetime-local field value of no schedule at all. */
export const EMPTY_SCHEDULE = ''

/** Parse a `datetime-local` value as browser-local time, or undefined. */
export function parseLocalSchedule(value: string): Date | undefined {
  if (value === EMPTY_SCHEDULE)
    return undefined
  const at = new Date(value)
  if (Number.isNaN(at.getTime()))
    return undefined
  // A nonexistent local time (02:30 on a spring-forward day) does not fail to
  // parse -- the constructor normalizes it to a LATER instant. The local
  // components of that instant disagree with the field, which is the lie this
  // comparison catches.
  const pad = (n: number) => String(n).padStart(2, '0')
  const roundTrip = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`
  const field = value.length >= 19 ? value.slice(0, 19) : value
  if (roundTrip !== field)
    return undefined
  return at
}

/** The wire form of one parsed instant, milliseconds included. */
export function scheduleIsoOf(at: Date): string {
  return at.toISOString()
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
  const pad = (n: number) => String(n).padStart(2, '0')
  const ms = at.getMilliseconds()
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}.${String(ms).padStart(3, '0')}`
}

/** What a draft schedule is, decided at apply time and again at send time. */
export type ScheduleCheck
  = | 'empty'
    | 'valid'
  /** Unparseable, normalized-invalid, or a nonexistent local time. */
    | 'invalid'
  /** In the past (or exactly now), where only an UNCHANGED stored value passes. */
    | 'notFuture'

/**
 * Validate one draft against the clock.
 *
 * `existingStored` names the schedule the item already holds while a queue
 * EDIT is in flight: an unchanged past deadline is already immediate queue
 * data, so saving it back stays legal, while a NEWLY chosen past time is a
 * schedule nobody could have wanted -- it would deliver the moment the queue
 * drains. The comparison runs on the WIRE forms, so a draft that round-trips
 * to the stored instant matches even though the field renders it locally.
 */
export function checkSchedule(draft: string, now: Date, existingStored?: string): ScheduleCheck {
  if (draft === EMPTY_SCHEDULE)
    return 'empty'
  const at = parseLocalSchedule(draft)
  if (at === undefined)
    return 'invalid'
  if (at.getTime() > now.getTime())
    return 'valid'
  if (existingStored !== undefined && scheduleIsoOf(at) === existingStored)
    return 'valid'
  return 'notFuture'
}
