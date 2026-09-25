import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  messageContents,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 231 — Amp basic chat.
 *
 * The worker drives `amp --execute --stream-json --stream-json-input`, and the mock's
 * Amp surface plays Amp's service and runs the agent loop. One scripted turn proves
 * that a prompt reaches Amp, that Amp's answer reaches the chat, and that the
 * assistant message whose stop reason is `end_turn` closes the turn. A second turn
 * proves that the thread keeps the conversation.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest.describe('Amp basic chat', () => {
  ampTest('renders an assistant answer and ends the turn with a timed divider', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await modelScript.queue({ reasoning: 'Add the two numbers.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
    // Amp states no duration of its own for a turn. The worker measures the turn
    // and adds it, so the divider always states a time.
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended \(.+\)$/)
    // Count the rows first: an absence assertion over an empty locator passes for
    // the wrong reason.
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    // The lines the worker drops -- Amp's echo of the prompt, the init line -- never
    // surface as a raw-JSON bubble.
    const allText = (await contents.allTextContents()).join(' ')
    expect(allText).not.toContain('"subtype":"init"')
    expect(allText).not.toContain('stream-json')
  })

  ampTest('keeps the conversation from one turn to the next', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
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

    // The second inference carries the first prompt and the first answer: the
    // thread holds the whole conversation.
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second).toBeDefined()
    const body = JSON.stringify(second?.body)
    expect(body).toContain('1234 + 5678')
    expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
  })
})
