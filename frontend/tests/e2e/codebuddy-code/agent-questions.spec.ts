import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code control answers', () => {
  const PROVIDER = AgentProvider.CODEBUDDY

  codebuddyTest('shows the native refusal when AskUserQuestion is unavailable', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await modelScript.queue({ toolCalls: [askUserQuestionToolCall(PROVIDER, 'question-call', [{
      question: 'Choose a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }])] })
    await sendMessage(page, modelScript.prompt('Ask me to choose a color.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const first = status.requests.find(request => request.stepIndex === 0)
    expect(first?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(first?.body).includes('"name":"AskUserQuestion"')).toBe(false)
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
    await expect(page.locator('[data-chat-scroll-container="true"]:visible').getByText('Tool "AskUserQuestion" does not exist in the current tool set.').first()).toBeVisible()
  })
})
