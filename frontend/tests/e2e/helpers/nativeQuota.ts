import type { MockModelError, MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './ui'

/** Prove quota headers came from the actual emitted generic model response. */
export async function exerciseNativeQuotaHeaders(context: ManagedNativeScenarioContext): Promise<void> {
  const answer = `NATIVEQUOTAHEADERS${randomUUID().replaceAll('-', '')}`
  const start = await context.modelScript.queue({
    ...nativeTextStep(context, answer),
    rateLimits: { type: 'five_hour', status: 'allowed', utilization: 0.73, resetsAt: Math.floor(Date.now() / 1000) + 3600 },
  })
  await sendMessage(context.page, context.modelScript.prompt('Complete the native quota-header probe.'))
  await context.modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(context.page)
  // Read the record after the turn: the mock adds the response receipt when the response completes.
  const request = await context.modelScript.requestAt(start)
  expect(request.response?.status).toBe(200)
  expect(request.response?.headers).toHaveProperty('x-ratelimit-limit-requests', '1000')
  expect(request.response?.headers).toHaveProperty('x-ratelimit-remaining-requests', '999')
  expect(request.response?.headers).toHaveProperty('x-leapmux-e2e-ratelimit-utilization', '0.73')
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}

/** Preserve native retry behavior while proving a service quota refusal and a usable later turn. */
export async function exerciseNativeQuotaRefusal(
  context: ManagedNativeScenarioContext,
  options: { error: MockModelError, receiptProof: (request: MockModelRequestRecord) => void | Promise<void> },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  const answer = `QUOTARECOVERED${randomUUID().replaceAll('-', '')}`
  const start = await context.modelScript.queue({ error: options.error }, nativeTextStep(context, answer))
  await sendMessage(context.page, context.modelScript.prompt('Run the native service quota-refusal probe.'))
  await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  await options.receiptProof(await context.modelScript.requestAt(start))
  if ((await context.modelScript.status()).nextStep < start + 2) {
    const queue = context.page.locator('[data-testid="queue-pause-button"]:visible')
    if ((await queue.textContent())?.includes('Resume'))
      await queue.click()
    await sendMessage(context.page, context.modelScript.prompt('Complete the valid turn after the native quota refusal.'))
  }
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
}
