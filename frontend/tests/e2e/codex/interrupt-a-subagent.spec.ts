import { expect } from '@playwright/test'
/** Test interruption of a held native child and the next parent answer. */
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('codex subagent lifecycle', () => {
  codexTest('interrupts one running child and leaves the root able to answer', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const child = await openHeldChildTab(page, modelScript, {
      provider: AgentProvider.CODEX,
      rowTitle: 'count_to_one_hundred',
      childTurn: { body: ['NEW_TASK', 'count_to_one_hundred', HELD_CHILD_TASK] },
      rootTurnsAfterSpawn: [{ text: 'The child started.' }],
    })

    const interrupt = page.locator('[data-testid="interrupt-button"]:visible')
    await expect(interrupt).toBeVisible()
    await interrupt.click()
    await expect(child.row).toHaveAttribute('data-status', 'paused')
    await expect(child.row).toContainText('paused')
    await expect(interrupt).toHaveCount(0)

    await tabById(page, child.rootTabId).click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    await modelScript.queue({ text: 'The root still answers.' })
    await sendMessage(page, modelScript.prompt('Reply from the root after the child stops.'))
    await modelScript.waitForSteps(3)
    await expect(assistantBubbles(page).filter({ hasText: 'The root still answers.' }).first()).toBeVisible()
    expect(await child.heldTurns()).toBe(1)
    await tabById(page, child.childTabId).click()
    await expect(interrupt).toHaveCount(0)
  })
})
