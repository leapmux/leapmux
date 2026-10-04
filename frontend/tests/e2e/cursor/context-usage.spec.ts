import { expect } from '@playwright/test'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('omits context usage when its ACP bridge sends no usage update', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await modelScript.queue({ text: 'Usage recorded.', usage: { inputTokens: 12_000, outputTokens: 40 } })
  await sendMessage(page, modelScript.prompt('Reply once.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Usage recorded.' }).first()).toBeVisible()
  const popover = await openAgentInfoCard(page)
  await expect(popover.getByText('Context', { exact: true })).toHaveCount(0)
})
