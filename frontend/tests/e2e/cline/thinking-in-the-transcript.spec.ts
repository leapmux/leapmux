import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { messageContents, userBubbles } from '../helpers/ui'

/**
 * The native reasoning block must appear in its transcript row. The answer remains a separate row.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline basic chat', () => {
  clineTest('draws the reasoning and the answer, and ends the turn with a timed divider', async ({ native, page }) => {
    const { prompt, request } = await exerciseThinkingRows(native)
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended \(.+\)$/)

    // The model call carried the prompt and the model of the isolated settings. The
    // chat shows the prompt as the user wrote it, without the wrapper that Cline
    // stores around a user message.
    expect((request.body as { model?: string } | undefined)?.model).toBe(MOCK_MODELS.cline)
    expect(JSON.stringify(request.body)).toContain(prompt)
    await expect(userBubbles(page).filter({ hasText: prompt }).first()).toBeVisible()
    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)
    expect((await contents.allTextContents()).join(' ')).not.toContain('<user_input')
  })
})
