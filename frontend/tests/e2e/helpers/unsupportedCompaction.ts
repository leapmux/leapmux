import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { compactionNoticeRow } from './compaction'
import { currentNativeAgent } from './nativeScenario'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { waitForAgentIdle } from './ui'

/** The provider callback proves actual compaction or native command refusal before the UI check. */
export async function expectNoCompactionNotice(
  context: ManagedNativeScenarioContext,
  options: { relatedProof: () => Promise<void> },
): Promise<void> {
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  await options.relatedProof()
  await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
  await expect(compactionNoticeRow(context.page)).toHaveCount(0)
  await context.page.reload()
  await waitForAgentIdle(context.page)
  await expect(compactionNoticeRow(context.page)).toHaveCount(0)
}
