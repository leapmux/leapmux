import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { SCRIPTED_CONTEXT_USAGE } from '../helpers/contextUsage'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest('omits context usage when its ACP bridge sends no usage update', async ({ native }) => {
  const { page, modelScript } = native
  const step = await modelScript.queue({ text: 'Usage recorded.', usage: { ...SCRIPTED_CONTEXT_USAGE } })
  await sendMessage(page, modelScript.prompt('Reply once.'))
  await modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Usage recorded.' }).first()).toBeVisible()
  const popover = await openAgentInfoCard(page)
  await expect(popover.getByText('Context', { exact: true })).toHaveCount(0)
})
