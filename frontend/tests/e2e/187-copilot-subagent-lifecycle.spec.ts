import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('copilot subagent lifecycle', () => {
  copilotTest('routes the prompt, response, and completion into the child tab', async ({
    authenticatedCopilotWorkspace,
    page,
  }) => {
    void authenticatedCopilotWorkspace
    await expectNoRegistryRows(page)

    await sendMessage(page, 'You MUST use the Task tool once. Give the subagent this prompt: "Reply with exactly COPILOT_CHILD_PONG." Wait for it, then reply with exactly COPILOT_ROOT_DONE.')
    await waitForAgentIdle(page, 180_000)

    const row = await requireRegistryRow(copilotTest, page)
    await expectRowBecomesFinal(page, row)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: 'COPILOT_CHILD_PONG' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: /^COPILOT_CHILD_PONG$/ })).toBeVisible()
  })
})
