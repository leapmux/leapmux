import { expect } from '@playwright/test'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

/** The thinking the scripted model reports before its answer. */
const REASONING = 'I add the two numbers.'

/**
 * The native reasoning block must appear in its transcript row. The answer remains a separate row.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('draws the thinking of a reply as a row of its own, before the answer, also after a reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // omp reads `reasoning_content` as a thinking block, and states the whole reply
    // as ONE message that holds the thinking and the text. The worker persists the
    // thinking as a row of its own, so the saved transcript keeps it.
    await modelScript.queue({ reasoning: REASONING, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const expectSeparateRows = async () => {
      await expectAssistantAnswer(page)
      await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
      // The answer row holds the answer alone.
      await expect(bandRows(page, 'text').filter({ hasText: ARITHMETIC_ANSWER_TEXT }).filter({ hasText: REASONING })).toHaveCount(0)
      const rows = await bandRows(page).allTextContents()
      const thought = rows.findIndex(text => text.includes(REASONING))
      const answer = rows.findIndex(text => text.includes(ARITHMETIC_ANSWER_TEXT))
      expect(thought).toBeGreaterThanOrEqual(0)
      expect(thought, 'the thinking comes before the answer').toBeLessThan(answer)
    }
    await expectSeparateRows()

    await page.reload()
    await expectSeparateRows()
  })
})
