import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

/** The thinking the scripted model reports before its answer. */
const REASONING = 'I add the two numbers.'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

// The component tests cover the indicator's visibility transitions.
// One scripted turn checks provider delivery and the final browser state.
piTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})

piTest('draws the thinking of a reply as a row of its own, before the answer, also after a reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  // Pi reads `reasoning_content` as a thinking block, and states the whole reply as
  // ONE message that holds the thinking and the text. The worker persists the
  // thinking as a row of its own, so the saved transcript keeps it.
  await modelScript.queue({ reasoning: REASONING, text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

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
