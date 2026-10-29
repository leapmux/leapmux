import type { AutoRetryKind } from './autoRetry'
import type { MockModelError, MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import type { QueueAfterFailure } from './ui'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { deleteAutoRetryRow, pauseQueueUnlessPaused } from './autoRetry'
import { sendNativeAnswer } from './nativeConversation'
import { messageContents, resumePausedQueue, resumeQueueAfterFailure, sendMessage, waitForAgentIdle } from './ui'

interface NativeModelErrorOptions {
  /**
   * The state of the input queue after the failed turn, as the provider leaves it.
   * The caller states it, and the scenario requires it. See {@link QueueAfterFailure}.
   */
  queueAfterFailure: QueueAfterFailure
  error?: MockModelError
  prepare?: () => Promise<void>
  /**
   * How many model requests the native client sends for the one failed turn.
   * Every request receives the same scripted error. The default is 1.
   * A client that retries a failed request needs one answer for each attempt,
   * because an unscripted request receives a refusal that the client retries again.
   */
  attempts?: number
  /**
   * Answer the question that the native client asks after its last failed attempt.
   * The failed turn stays open until the reader answers: Dirac asks whether to
   * retry the request. It receives the scripted error that each attempt got.
   */
  answerFailure?: (error: MockModelError) => Promise<void>
}

/**
 * The letters that stand for the hexadecimal digits `0` to `f` in an error marker.
 * They are the sixteen consonants from `b` to `t`, without a vowel.
 */
const MARKER_LETTERS = 'bcdfghjklmnpqrst'

/**
 * Build the text that the default scripted error carries and that the chat then shows.
 * A UUID that the caller gives replaces the random one, so a test can fix the input.
 *
 * The text after the prefix holds no digit and no vowel. A native client can read the
 * message of a failed request, decide that the failure is transient, and retry the request.
 * A run of digits can spell an HTTP status. A run of letters can spell a word.
 * OpenCode 1.18.34 and Pi 1.0.0 retry a request when its message holds `429`, `500`, `502`,
 * `503`, `504` or `524`, whatever the status of the response is. About one UUID in 21 holds one.
 * The one scripted error then answers the first attempt only. Each retry gets the refusal of
 * an unscripted request, and the text of the last refusal replaces the marker in the chat.
 * OpenCode has no setting that turns the retry off, so the marker must avoid it.
 * A pattern for a transient failure matches a word, and each such word holds a vowel.
 * The map from digit to letter is one to one, so two UUIDs never give the same marker.
 */
export function nativeErrorMarker(uuid: string = randomUUID()): string {
  const digits = uuid.replaceAll('-', '')
  if (!/^[0-9a-f]{32}$/i.test(digits))
    throw new Error(`The error marker needs a UUID of 32 hexadecimal digits, not "${uuid}".`)
  return `NATIVEERROR${digits.replace(/[0-9a-f]/gi, digit => MARKER_LETTERS.charAt(Number.parseInt(digit, 16)))}`
}

/**
 * Select the requests that consumed the failed turn's scripted steps, in step order.
 * The steps start at `firstStep`, one for each of `attempts`.
 * A request that a rule or the fallback answered consumed no step, so it is not one of them.
 */
export function failedTurnRequests(requests: readonly MockModelRequestRecord[], firstStep: number, attempts: number): MockModelRequestRecord[] {
  if (!Number.isSafeInteger(firstStep) || firstStep < 0)
    throw new Error(`The failed turn needs a nonnegative first step, not ${firstStep}.`)
  if (!Number.isSafeInteger(attempts) || attempts < 1)
    throw new Error(`The native model error needs a positive whole number of attempts, not ${attempts}.`)
  return requests
    .filter(request => request.stepIndex !== undefined && request.stepIndex >= firstStep && request.stepIndex < firstStep + attempts)
    .sort((left, right) => (left.stepIndex ?? 0) - (right.stepIndex ?? 0))
}

/**
 * Verify a native service failure and a successful later turn through the same tab.
 * Return the requests of the failed turn in their step order, so a caller can check what each attempt sent.
 */
export async function exerciseModelError(
  context: NativeScenarioContext,
  options: NativeModelErrorOptions,
): Promise<MockModelRequestRecord[]> {
  const attempts = options.attempts ?? 1
  await options.prepare?.()
  const marker = nativeErrorMarker()
  const error = options.error ?? { status: 400, code: 'invalid_request_error', message: marker }
  // Validate the count before the turn starts, so a bad count fails here and not in a native retry loop.
  failedTurnRequests([], 0, attempts)
  const stepIndex = await context.modelScript.queue(...Array.from({ length: attempts }, () => ({ error })))
  await sendMessage(context.page, context.modelScript.prompt('Run the native model error probe.'))
  const status = await context.modelScript.waitForSteps(stepIndex + attempts)
  const failed = failedTurnRequests(status.requests, stepIndex, attempts)
  expect(failed.map(request => request.stepIndex)).toEqual(Array.from({ length: attempts }, (_, offset) => stepIndex + offset))
  await options.answerFailure?.(error)
  // The idle wait requires the visible thinking indicator to clear, so no second indicator check follows.
  await waitForAgentIdle(context.page)
  await expect(messageContents(context.page).filter({ hasText: error.message })).toHaveCount(1)
  await context.page.reload()
  await waitForAgentIdle(context.page)
  await expect(messageContents(context.page).filter({ hasText: error.message })).toHaveCount(1)
  await resumeQueueAfterFailure(context.page, options.queueAfterFailure)
  await sendNativeAnswer(context, 'Reply once after the native service failure.', `RECOVERED${marker}`)
  return failed
}

interface TransientModelFailureOptions {
  /** The scripted error of every attempt: a failure that LeapMux retries, such as a 5xx, an overload, or a dropped stream. */
  error: MockModelError
  /**
   * How many model requests the native client sends before it gives up on the turn.
   * Every request receives `error`. The count must be exact: one request more receives the refusal of an
   * unscripted request, and that refusal fails the spec.
   */
  attempts: number
  prepare?: () => Promise<void>
  /** Answer the question that the native client asks after its last failed attempt. See {@link NativeModelErrorOptions}. */
  answerFailure?: (error: MockModelError) => Promise<void>
  /** The kind of automatic retry that LeapMux schedules for the failure. The default is `server error`. */
  retryKind?: AutoRetryKind
}

/**
 * Verify that LeapMux schedules its own automatic retry when the native client gives up on a failure that LeapMux
 * retries. Then delete that retry before it matures, and verify a successful later turn through the same tab.
 * Return the requests of the failed turn in their step order.
 *
 * The queue is paused as soon as the last attempt is consumed, so the scheduled retry cannot mature while the checks
 * run. The first server-error retry waits ten seconds, and a loaded run can take longer than that before the delete.
 * The queue state that the failure itself leaves is the subject of {@link exerciseModelError}, not of this exercise.
 */
export async function exerciseTransientModelFailure(
  context: NativeScenarioContext,
  options: TransientModelFailureOptions,
): Promise<MockModelRequestRecord[]> {
  await options.prepare?.()
  // Validate the count before the turn starts, so a bad count fails here and not in a native retry loop.
  failedTurnRequests([], 0, options.attempts)
  const stepIndex = await context.modelScript.queue(...Array.from({ length: options.attempts }, () => ({ error: options.error })))
  await sendMessage(context.page, context.modelScript.prompt('Run the native transient failure probe.'))
  const status = await context.modelScript.waitForSteps(stepIndex + options.attempts)
  const failed = failedTurnRequests(status.requests, stepIndex, options.attempts)
  expect(failed.map(request => request.stepIndex)).toEqual(Array.from({ length: options.attempts }, (_, offset) => stepIndex + offset))
  await pauseQueueUnlessPaused(context.page)
  await options.answerFailure?.(options.error)
  await waitForAgentIdle(context.page)
  await deleteAutoRetryRow(context.page, options.retryKind ?? 'server error')
  await resumePausedQueue(context.page)
  await sendNativeAnswer(context, 'Reply once after the native transient failure.', `RECOVERED${nativeErrorMarker()}`)
  return failed
}
