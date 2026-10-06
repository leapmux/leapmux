import type { MockModelError, MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { QueueAfterFailure } from './ui'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { NEAR_LIMIT_UTILIZATION, nearLimitRateLimits } from './rateLimit'
import { uniqueMarker } from './shellArguments'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { assistantBubbles, resumeQueueAfterFailure, sendMessage, waitForAgentIdle } from './ui'

/**
 * Prove that the provider received quota headers from the actual generic model response that it consumed.
 * The window is near its limit (`nearLimitRateLimits`), the input for which a supporting provider shows a window.
 * The proof reads only the generic headers, because a native client route can drop the provider-owned families.
 * Return the model request of the turn, so a caller can check the protocol of its provider.
 */
export async function exerciseNativeQuotaHeaders(context: ManagedNativeScenarioContext): Promise<MockModelRequestRecord> {
  const answer = uniqueMarker('NATIVEQUOTAHEADERS')
  const start = await context.modelScript.queue({ ...nativeTextStep(context, answer), rateLimits: nearLimitRateLimits() })
  await sendMessage(context.page, context.modelScript.prompt('Complete the native quota-header probe.'))
  await context.modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(context.page)
  // Read the record after the turn: the mock adds the response receipt when the response completes.
  const request = await context.modelScript.requestAt(start)
  expect(request.response?.status).toBe(200)
  expect(request.response?.headers).toHaveProperty('x-ratelimit-limit-requests', '1000')
  // The mock states no remaining request for a window that is not `allowed`.
  expect(request.response?.headers).toHaveProperty('x-ratelimit-remaining-requests', '0')
  expect(request.response?.headers).toHaveProperty('x-leapmux-e2e-ratelimit-status', 'allowed_warning')
  expect(request.response?.headers).toHaveProperty('x-leapmux-e2e-ratelimit-utilization', String(NEAR_LIMIT_UTILIZATION))
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  return request
}

/** Preserve native retry behavior while proving a service quota refusal and a usable later turn. */
export async function exerciseNativeQuotaRefusal(
  context: ManagedNativeScenarioContext,
  options: {
    error: MockModelError
    receiptProof: (request: MockModelRequestRecord) => void | Promise<void>
    /** The state of the input queue after the refused turn, as the provider leaves it. See {@link QueueAfterFailure}. */
    queueAfterFailure: QueueAfterFailure
  },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  const answer = uniqueMarker('QUOTARECOVERED')
  const start = await context.modelScript.queue({ error: options.error }, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt('Run the native service quota-refusal probe.'))
  await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  await options.receiptProof(await context.modelScript.requestAt(start))
  if ((await context.modelScript.status()).nextStep < start + 2) {
    await resumeQueueAfterFailure(context.page, options.queueAfterFailure)
    await sendMessage(context.page, context.modelScript.prompt('Complete the valid turn after the native quota refusal.'))
  }
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}
