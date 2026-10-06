import { expect } from '@playwright/test'

import { clineTest } from '../cline-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, messageContents, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'

/**
 * The native reasoning block must appear in its transcript row. The answer remains a separate row.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline basic chat', () => {
  clineTest('draws the reasoning and the answer, and ends the turn with a timed divider', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    const reasoning = 'I add the two numbers column by column.'
    await modelScript.queue({ reasoning, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectAssistantAnswer(page)
    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
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
    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
