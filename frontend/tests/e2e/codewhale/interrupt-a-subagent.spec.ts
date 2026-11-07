import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { cleanupOnFailure } from '../helpers/cleanup'
import { HELD_CHILD_NAME, HELD_CHILD_TASK, openHeldChildTab, stopChildWithInterrupt, subagentEndDivider } from '../helpers/subagentRegistry'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, sendMessage, tabById } from '../helpers/ui'

codewhaleTest('interrupts a native child without interrupting its parent', async ({ native }) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const parentAnswer = 'The native child runs in the background.'
  const child = await openHeldChildTab(native, {
    rowTitle: HELD_CHILD_NAME,
    childTurn: { user: HELD_CHILD_TASK },
    background: true,
    rootTurnsAfterSpawn: [
      { text: parentAnswer },
    ],
  })
  await cleanupOnFailure(async () => {
    await stopChildWithInterrupt(page, child.row, 'stopped')
    await tabById(page, child.parentId).click()
    await expect(assistantBubbles(page).filter({ hasText: parentAnswer })).toBeVisible()
    // The native cancel route updates the child receipt without starting a parent turn.
    // A new prompt proves that the parent still accepts input.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await expectAssistantAnswer(page)
    expect(await child.heldTurns()).toBe(1)
    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await tabById(page, child.childId).click()
      await expect(subagentEndDivider(page)).toHaveCount(0)
      await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
    }
  }, child.finish)
})
