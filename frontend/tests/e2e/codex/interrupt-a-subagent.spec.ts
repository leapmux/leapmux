/** Test interruption of a held native child and the next parent answer. */
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { HELD_CHILD_NAME, HELD_CHILD_TASK, openHeldChildTab, stopChildWithInterrupt } from '../helpers/subagentRegistry'
import { assistantBubbles, interruptButton, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('codex subagent lifecycle', () => {
  codexTest('interrupts one running child and leaves the root able to answer', async ({ native }) => {
    const { page, modelScript } = native
    const child = await openHeldChildTab(native, {
      rowTitle: HELD_CHILD_NAME,
      childTurn: { body: ['NEW_TASK', HELD_CHILD_NAME, HELD_CHILD_TASK] },
      rootTurnsAfterSpawn: [{ text: 'The child started.' }],
    })

    // Codex pauses an interrupted child, and its root answered at the spawn, not after the stop.
    await stopChildWithInterrupt(page, child.row, 'paused')
    await expect(child.row).toContainText('paused')

    await tabById(page, child.parentId).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await modelScript.queue({ text: 'The root still answers.' })
    await sendMessage(page, modelScript.prompt('Reply from the root after the child stops.'))
    await modelScript.waitForSteps()
    await expect(assistantBubbles(page).filter({ hasText: 'The root still answers.' }).first()).toBeVisible()
    expect(await child.heldTurns()).toBe(1)
    await tabById(page, child.childId).click()
    await expect(interruptButton(page)).toHaveCount(0)
  })
})
