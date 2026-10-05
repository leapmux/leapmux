import type { MockModelError, MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { sendMessage, visibleOnly, waitForAgentIdle } from './ui'

interface NativeModelErrorOptions {
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
  options: NativeModelErrorOptions = {},
): Promise<MockModelRequestRecord[]> {
  const attempts = options.attempts ?? 1
  await options.prepare?.()
  const marker = `NATIVEERROR${randomUUID().replaceAll('-', '')}`
  const error = options.error ?? { status: 400, code: 'invalid_request_error', message: marker }
  const stepIndex = (await context.modelScript.status()).stepCount
  // Validate before the turn starts, so a bad count fails here and not in a native retry loop.
  failedTurnRequests([], stepIndex, attempts)
  await context.modelScript.queue(...Array.from({ length: attempts }, () => ({ error })))
  await sendMessage(context.page, context.modelScript.prompt('Run the native model error probe.'))
  const status = await context.modelScript.waitForSteps(stepIndex + attempts)
  const failed = failedTurnRequests(status.requests, stepIndex, attempts)
  expect(failed.map(request => request.stepIndex)).toEqual(Array.from({ length: attempts }, (_, offset) => stepIndex + offset))
  await options.answerFailure?.(error)
  await waitForAgentIdle(context.page)
  await expect(visibleOnly(context.page.getByText(error.message, { exact: false })).first()).toBeVisible()
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  const queue = context.page.locator('[data-testid="queue-pause-button"]:visible')
  if (await queue.count() > 0 && (await queue.textContent())?.includes('Resume'))
    await queue.click()
  await sendNativeAnswer(context, 'Reply once after the native service failure.', `RECOVERED${marker}`)
  return failed
}
