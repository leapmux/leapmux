import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer, expectNoControlBanner, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code control answers', () => {
  codebuddyTest('shows the native refusal when AskUserQuestion is unavailable', async ({ native }) => {
    const { page, modelScript } = native
    const start = await modelScript.queue({ toolCalls: [askUserQuestionToolCall(native.provider, 'question-call', [{
      question: 'Choose a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }])] })
    await sendMessage(page, modelScript.prompt('Ask me to choose a color.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    const first = await modelScript.requestAt(start)
    expect(first.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(first.body).includes('"name":"AskUserQuestion"')).toBe(false)
    await expectNoControlBanner(page)
    await expect(chatScrollContainer(page).getByText('Tool "AskUserQuestion" does not exist in the current tool set.').first()).toBeVisible()
  })
})
