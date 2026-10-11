import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { currentNativeAgent } from './nativeScenario'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { openAgentInfoCard, waitForAgentIdle } from './ui'

/**
 * Inspect quota UI only after the provider callback proves its real native quota scenario.
 * The check ignores the result of the callback, so a callback can return the request that it read.
 */
export async function expectNoRateLimitState(
  context: ManagedNativeScenarioContext,
  options: { relatedProof: () => Promise<unknown> },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  await options.relatedProof()
  await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  const inspect = async () => {
    const popover = await openAgentInfoCard(context.page)
    await expect(popover.getByText(/^(?:Rate Limit(?:\s|$)|.+ Rate Limit$)/)).toHaveCount(0)
  }
  await inspect()
  await context.page.reload()
  await waitForAgentIdle(context.page)
  await inspect()
}
