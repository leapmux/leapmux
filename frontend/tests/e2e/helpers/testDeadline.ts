/**
 * The deadline of the running test, for a wait that must end before the test's own timeout.
 *
 * A wait that runs until the test times out fails with "Test timeout of 120000ms exceeded", which states no step.
 * A wait that ends {@link WAIT_REPORT_MARGIN_MS} earlier fails with its own message, and the margin leaves time for
 * the fixtures to attach the model script and the server log.
 *
 * Playwright runs the tests of one worker process one at a time, so one record serves the whole process.
 * The automatic `testStartedAt` fixture of each test base starts the record before every other fixture.
 */

/**
 * Leave this interval before the whole-test deadline for the wait error and fixture teardown.
 * The wait error reports what the wait expected.
 * The Playwright timeout reports only that the test ran out of time.
 */
export const WAIT_REPORT_MARGIN_MS = 5_000

interface RunningTest {
  /** When the test's timer started, in epoch milliseconds. */
  readonly startedAt: number
  /** The test's current timeout in milliseconds, or 0 when it has none. A test can change it while it runs. */
  readonly timeout: () => number
}

let running: RunningTest | undefined

/**
 * Record the start of a test and return the function that ends the record.
 * The end function clears only its own record, so a late call cannot clear the record of a later test.
 */
export function startTestDeadline(startedAt: number, timeout: () => number): () => void {
  if (!Number.isFinite(startedAt))
    throw new Error(`A test start must be a finite time, not ${startedAt}.`)
  const record: RunningTest = { startedAt, timeout }
  running = record
  return () => {
    if (running === record)
      running = undefined
  }
}

/**
 * Start a record whose waits end `limitMs` from now, for a unit test that runs a wait outside Playwright, and return
 * the function that ends the record. Without a record, such a wait has no limit, and a wait that never passes then
 * runs until the unit test times out.
 */
export function startWaitLimitForTests(limitMs: number): () => void {
  if (!Number.isFinite(limitMs) || limitMs <= 0)
    throw new Error(`A wait limit must be a positive number of milliseconds, not ${limitMs}.`)
  return startTestDeadline(Date.now(), () => WAIT_REPORT_MARGIN_MS + limitMs)
}

/**
 * The whole-test deadline in epoch milliseconds.
 * Return undefined when no test runs, or when the running test has no timeout.
 * Each call reads the current timeout, so a timeout change during the test moves the deadline.
 */
export function currentTestDeadline(): number | undefined {
  if (!running)
    return undefined
  const timeout = running.timeout()
  return timeout > 0 ? running.startedAt + timeout : undefined
}

/**
 * The timeout of one native command that a test runs itself, such as a catalog read of an installed CLI: `limitMs`, or
 * the whole milliseconds left before `deadline` when they are fewer. With no deadline, `limitMs` applies alone, so the
 * command never runs without a limit.
 *
 * A deadline that leaves no whole millisecond throws, so the command does not start. A deadline or a current time that
 * is not finite throws also, because it states no time.
 */
export function nativeCommandTimeout(deadline: number | undefined, limitMs: number, now: number = Date.now()): number {
  if (!Number.isSafeInteger(limitMs) || limitMs <= 0)
    throw new RangeError(`A native command limit must be a positive whole number of milliseconds, not ${limitMs}.`)
  if (!Number.isFinite(now) || (deadline !== undefined && !Number.isFinite(deadline)))
    throw new RangeError('A native command timeout requires a finite test deadline and current time.')
  if (deadline === undefined)
    return limitMs
  const remaining = Math.floor(deadline - now)
  if (remaining <= 0)
    throw new Error('The test deadline leaves no time for the native command.')
  return Math.min(remaining, limitMs)
}

/**
 * The Playwright timeout of a wait that must end {@link WAIT_REPORT_MARGIN_MS} before the test's deadline.
 * Return 0, which Playwright reads as "no limit", when the test has no deadline.
 * Return at least 1 when the deadline is near or past, so that the wait fails at once with its own message.
 */
export function waitTimeoutBeforeTestDeadline(now: number = Date.now()): number {
  const deadline = currentTestDeadline()
  if (deadline === undefined)
    return 0
  return Math.max(1, deadline - WAIT_REPORT_MARGIN_MS - now)
}
