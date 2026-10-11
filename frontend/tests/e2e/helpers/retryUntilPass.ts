import { sleep } from '../../../src/lib/sleep'
import { waitTimeoutBeforeTestDeadline } from './testDeadline'

/** The pauses after the first attempts, the same as the default of Playwright's `toPass`. */
const FIRST_PAUSES_MS: readonly number[] = [100, 250, 500]

/** The pause after each later attempt. */
const LATER_PAUSE_MS = 1_000

/** The outcome of an attempt that the wait abandoned, because the limit of the wait passed before the attempt ended. */
const OUT_OF_TIME = Symbol('out of time')

/**
 * Run `attempt` again until it returns without an error, and return the value of the attempt that passed.
 *
 * Use it for a wait whose read can throw while the state is transient, for example a Worker or Hub read during a
 * reconnect, or a read of a file that a native process writes. Put the read and its assertion inside `attempt`. A read
 * that throws and an assertion that fails both start the next attempt. An attempt that throws for a defect that never
 * goes away is retried too, so the wait then ends at its limit, and its failure states that defect.
 *
 * `expect.poll` is not a correct wait for such a read. Playwright calls the poll function outside the `try` that
 * retries a failed matcher (`invokePollMatcher` in `playwright/lib/matchers/expect.js`), so the first read that throws
 * ends the poll at once. Keep `expect.poll` for a read that cannot throw. `throwingPollReads.test.ts` in
 * `src/test-support/` refuses a Worker or Hub read inside `expect.poll`.
 *
 * The loop is the one of Playwright's `toPass`. It is written here because many unit tests replace Playwright's
 * `expect` with the one of vitest, which has no `toPass`. The wait ends `WAIT_REPORT_MARGIN_MS` (`./testDeadline.ts`)
 * before the test's own deadline, as the model-script waits do, so the fixtures keep the time to attach their records.
 * Outside a test, as in a unit test that starts no test record, the wait has no limit.
 */
export async function retryUntilPass<T>(attempt: () => T | Promise<T>): Promise<T> {
  const limit = waitTimeoutBeforeTestDeadline()
  const started = Date.now()
  const deadline = limit > 0 ? started + limit : undefined
  let lastError: unknown
  for (let attempts = 1; ; attempts++) {
    let result: T | typeof OUT_OF_TIME
    try {
      result = await raceDeadline(attempt, deadline)
    }
    catch (error) {
      lastError = error
      const pause = FIRST_PAUSES_MS[attempts - 1] ?? LATER_PAUSE_MS
      // Playwright's `toPass` ends the wait at once when the next pause would reach the limit, and so does this loop.
      if (deadline !== undefined && Date.now() + pause >= deadline)
        throw outOfTimeError(lastError, attempts, started)
      await sleep(pause)
      continue
    }
    if (result === OUT_OF_TIME)
      throw outOfTimeError(lastError, attempts, started)
    return result
  }
}

/** Run one attempt, and return `OUT_OF_TIME` when `deadline` passes before the attempt ends. */
async function raceDeadline<T>(attempt: () => T | Promise<T>, deadline: number | undefined): Promise<T | typeof OUT_OF_TIME> {
  const running = Promise.resolve().then(attempt)
  if (deadline === undefined)
    return running
  let timer: ReturnType<typeof setTimeout> | undefined
  const outOfTime = new Promise<typeof OUT_OF_TIME>((resolve) => {
    timer = setTimeout(resolve, Math.max(0, deadline - Date.now()), OUT_OF_TIME)
  })
  try {
    return await Promise.race([running, outOfTime])
  }
  finally {
    clearTimeout(timer)
  }
}

/** Build the failure of a wait that reached its limit. Its first line is the error of the last attempt that ended. */
function outOfTimeError(lastError: unknown, attempts: number, started: number): Error {
  const reason = lastError === undefined
    ? 'No attempt ended before the limit of the wait.'
    : lastError instanceof Error ? lastError.message : String(lastError)
  const summary = `The wait ran ${attempts} attempt(s) in ${Date.now() - started}ms and ended before the test's own deadline.`
  return new Error(`${reason}\n\n${summary}`, { cause: lastError })
}
