import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

claudeTest.describe('Claude Code transcript and context', () => {
  claudeTest('keeps model reasoning and context usage after a reload', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    const reasoning = 'I check the facts before I answer.'
    const usage = { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 }
    const start = await modelScript.queue({ reasoning, text: 'CLAUDE_MATRIX_DONE', usage })
    await sendMessage(page, modelScript.prompt('Reply with CLAUDE_MATRIX_DONE after thinking.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)

    const thought = bandRows(page, 'thought').filter({ hasText: reasoning })
    await expect(thought).toBeVisible()
    await expectContextUsage(page, usage)

    await page.reload()
    await expect(thought).toBeVisible()
    await expectContextUsage(page, usage)
  })
})
