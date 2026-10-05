import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '../fastagent-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseCompactPreviewRefusal(context: NativeScenarioContext): Promise<void> {
  const { page } = context

  await sendMessage(page, '/compact preview')
  await waitForAgentIdle(page)
  await expect(messageBubbles(page).filter({ hasText: 'Unknown command: /compact' }).first()).toBeVisible()
}

/** Exercise the actual native compaction path and preserve its context assertions. */
export async function exerciseCompactRefusal(context: NativeScenarioContext): Promise<void> {
  const { page } = context

  await sendMessage(page, '/compact')
  await waitForAgentIdle(page)
  await expect(messageBubbles(page).filter({ hasText: 'Unknown command: /compact' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
}
