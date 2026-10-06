import { expect } from '@playwright/test'
import { chooseQuestionOption, exerciseQuestionAnswer } from '../helpers/nativeQuestion'
import { messageBubbles, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'
import { nativeContext } from './scenarios'

qwenTest.describe('Qwen Code control requests', () => {
  qwenTest('answers a question through its own reply field', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const { result } = await exerciseQuestionAnswer(context, {
      questions: [{
        question: 'Which color do you want?',
        header: 'Color',
        options: [{ label: 'Red', description: 'The red one' }, { label: 'Blue', description: 'The blue one' }],
      }],
      callId: 'qwen-question',
      prompt: 'Ask me for a color.',
      answer: 'You chose Blue.',
      reply: chooseQuestionOption('Blue'),
    })

    // Qwen's native reply field carries the answer. Its tool result states that answer.
    // The saved answer appears under the question header.
    expect(result).toContain('**Color**: Blue')
    await expect(messageBubbles(page).filter({ hasText: 'Color: Blue' }).first()).toBeVisible()
  })
})
