import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

async function answerOpenCodeQuestion(page: Page, modelScript: ModelScript, provider: AgentProvider): Promise<void> {
  await modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(provider, 'color-question', [{
      question: 'Pick a color.',
      header: 'Color',
      options: [
        { label: 'Blue', description: 'Use blue.' },
        { label: 'Green', description: 'Use green.' },
      ],
    }])] },
    { text: 'The answer was recorded.' },
  )
  await sendMessage(page, modelScript.prompt('Ask me to pick a color.'))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Pick a color.')
  await banner.getByTestId('question-option-Green').click()
  await page.getByTestId('control-submit-btn').click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const request = status.requests.find(record => record.stepIndex === 1)
  expect(request?.protocol).toBe('openai-chat-completions')
  const body = isObject(request?.body) ? request.body : null
  const messages = Array.isArray(body?.messages) ? body.messages : []
  const result = messages.find(message => isObject(message) && message.role === 'tool' && message.tool_call_id === 'color-question')
  expect(result, 'the native question result reached the model').toBeDefined()
  const answer = JSON.stringify(isObject(result) ? result.content : '')
  expect(answer).toContain('Green')
  expect(answer).not.toContain('Blue')
  await expect(assistantBubbles(page).filter({ hasText: 'The answer was recorded.' }).first()).toBeVisible()
  await expect(banner).toHaveCount(0)
}

opencodeTest('answers a native question and resumes the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await answerOpenCodeQuestion(page, modelScript, AgentProvider.OPENCODE)
})
