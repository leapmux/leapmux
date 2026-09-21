import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest.describe('zcode subagent lifecycle', () => {
  zcodeTest('routes the prompt, tools, and final report into the child tab', async ({
    authenticatedZCodeWorkspace,
    page,
  }) => {
    void authenticatedZCodeWorkspace
    await expectNoRegistryRows(page)

    await sendMessage(page, 'You MUST use the Agent tool once. Give the subagent this prompt: "Use Bash to run printf zcode-tool-ok, then reply with exactly ZCODE_CHILD_PONG." Wait for it, then reply with exactly ZCODE_ROOT_DONE.')
    await waitForAgentIdle(page, 180_000)

    const row = await requireRegistryRow(zcodeTest, page)
    await expectRowBecomesFinal(page, row)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: 'ZCODE_CHILD_PONG' })).toBeVisible()
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /ZCODE_CHILD_PONG/ })).toBeVisible()
  })
})
