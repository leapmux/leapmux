import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openSettingsMenu, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('Codex agent questions', () => {
  codexTest('returns the selected answer to the native question tool', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await openSettingsMenu(page, 'collaboration_mode')
    await page.locator('[data-testid="collaboration_mode-plan"]').click()

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(AgentProvider.CODEX, 'codex-color-question', [{
          header: 'Color',
          question: 'Which color should I use?',
          options: [
            { label: 'Blue (Recommended)', description: 'Use blue.' },
            { label: 'Red', description: 'Use red.' },
          ],
        }])],
      },
      { text: 'The answer was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which color to use.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('Which color should I use?')
    await banner.getByTestId('question-option-Red').click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const answerRequest = status.requests.find(request => request.stepIndex === 1)
    expect(answerRequest?.protocol).toBe('openai-responses')
    const answer = nativeToolResult(answerRequest, 'codex-color-question')
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue (Recommended)')
    await expect(assistantBubbles(page).filter({ hasText: 'The answer was recorded.' }).first()).toBeVisible()
  })
})
