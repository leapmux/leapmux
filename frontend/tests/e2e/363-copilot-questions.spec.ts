import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { nativeToolResult } from './helpers/nativeToolResult'
import { askUserQuestionToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

copilotTest.describe('Copilot agent questions', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('answers one native ask_user choice and resumes the model turn', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    await modelScript.queue(
      { toolCalls: [askUserQuestionToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-question', [{
        question: 'Which color should I use?',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Green', description: 'Use green.' },
        ],
      }])] },
      { text: 'I used the chosen color.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me which color to use, then report that choice.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Which color should I use?')
    await banner.getByTestId('question-option-Green').click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const answerRequest = status.requests.find(request => request.stepIndex === 1)
    expect(answerRequest?.protocol).toBe('openai-chat-completions')
    const answer = nativeToolResult(answerRequest, 'copilot-question')
    expect(answer).toContain('Green')
    expect(answer).not.toContain('Blue')
    await expect(assistantBubbles(page).filter({ hasText: 'I used the chosen color.' }).first()).toBeVisible()
    await expect(banner).toHaveCount(0)
  })
})
