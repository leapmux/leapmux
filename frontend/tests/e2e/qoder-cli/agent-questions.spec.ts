import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI control answers', () => {
  const PROVIDER = AgentProvider.QODER

  qoderTest('returns a selected answer from its native question tool', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    await modelScript.queue(
      { toolCalls: [askUserQuestionToolCall(PROVIDER, 'qoder-question', [{
        question: 'Which color should I use?',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'Use blue.' },
          { label: 'Red', description: 'Use red.' },
        ],
      }])] },
      { text: 'QODER_QUESTION_ANSWERED.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me to choose a color.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Which color should I use?')
    await page.getByTestId('question-option-Red').filter({ visible: true }).first().click()
    await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const answer = nativeToolResult(followUp, 'qoder-question')
    expect(answer).toContain('Red')
    expect(answer).not.toContain('Blue')
    await expect(assistantBubbles(page).filter({ hasText: 'QODER_QUESTION_ANSWERED.' }).first()).toBeVisible()
  })
})
