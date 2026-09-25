import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  bandRows,
  expectAssistantAnswer,
  messageContents,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/** The thinking the scripted model reports before its answer. */
const REASONING = 'I add the two numbers.'

/**
 * 133 — Oh My Pi basic chat.
 *
 * The worker drives `omp --mode rpc-ui` over its JSONL protocol. One scripted turn
 * proves that a prompt reaches omp, that omp's answer reaches the chat, and that
 * `agent_end` closes the turn. A second turn proves that omp keeps the session:
 * its request replays the first exchange.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('renders an assistant answer and ends the turn with a timed divider', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
    // omp's agent_end states no duration. The worker measures the turn and adds
    // it, so the divider always states a time.
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended \(.+\)$/)
    // Count the rows first: an absence assertion over an empty locator passes for
    // the wrong reason.
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    // The frames the worker drops (the per-token updates, the turn frames) never
    // surface as a raw-JSON bubble.
    const allText = (await contents.allTextContents()).join(' ')
    expect(allText).not.toContain('message_update')
    expect(allText).not.toContain('turn_end')
  })

  ohMyPiTest('draws the thinking of a reply as a row of its own, before the answer, also after a reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // omp reads `reasoning_content` as a thinking block, and states the whole reply
    // as ONE message that holds the thinking and the text. The worker persists the
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

  ohMyPiTest('keeps the conversation from one turn to the next', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    // The second request carries the first prompt and the first answer, which is
    // what a session that omp keeps from one prompt to the next sends.
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second).toBeDefined()
    const body = JSON.stringify(second?.body)
    expect(body).toContain('1234 + 5678')
    expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
  })
})
