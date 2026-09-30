import type { Page } from '@playwright/test'
import { CLINE_E2E_SKIP_REASON, clineTest, expect } from './cline-fixtures'
import { exerciseContextUsage } from './helpers/contextUsage'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  assistantBubbles,
  bandRows,
  expectAssistantAnswer,
  messageContents,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  sendMessage,
  userBubbles,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 240 — Cline basic chat.
 *
 * The worker starts a private Cline hub for the agent, creates a session on it, and
 * sends each prompt as the session's input. Cline asks the mock through its
 * DeepSeek provider through its OpenAI-compatible transport. One scripted turn
 * proves that a prompt reaches the model. The reasoning and answer reach the
 * chat, and the run ends. A second turn proves that the session keeps the conversation.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/** The thought band that holds a turn's reasoning. */
function thoughtBands(page: Page) {
  return page.locator('[data-band="thought"]:visible')
}

clineTest.describe('Cline basic chat', () => {
  clineTest('reports model usage in the agent info card', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseContextUsage(page, modelScript)
  })

  clineTest('draws the reasoning and the answer, and ends the turn with a timed divider', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    const reasoning = 'I add the two numbers column by column.'
    await modelScript.queue({ reasoning, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(thoughtBands(page).filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended \(.+\)$/)

    // The model call carried the prompt and the model of the isolated settings. The
    // chat shows the prompt as the user wrote it, without the wrapper that Cline
    // stores around a user message.
    const request = status.requests.find(record => record.stepIndex === 0)
    expect((request?.body as { model?: string } | undefined)?.model).toBe(MOCK_MODELS.cline)
    expect(JSON.stringify(request?.body)).toContain('1234 + 5678')
    await expect(userBubbles(page).filter({ hasText: '1234 + 5678' }).first()).toBeVisible()
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    expect((await contents.allTextContents()).join(' ')).not.toContain('<user_input')

    // The worker writes the rows it streamed, so a reload draws the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
    await expect(thoughtBands(page).filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })

  clineTest('keeps the conversation from one turn to the next', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    // The second model call carries the first prompt and the first answer: the
    // session holds the whole conversation.
    const second = status.requests.find(request => request.stepIndex === 1)
    const body = JSON.stringify(second?.body)
    expect(body).toContain('1234 + 5678')
    expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
    await expect(assistantBubbles(page).filter({ hasText: ARITHMETIC_ANSWER_TEXT })).not.toHaveCount(0)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
