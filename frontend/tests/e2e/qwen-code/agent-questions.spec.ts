import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlBanner, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code control requests', () => {
  qwenTest('answers a question through its own reply field', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      {
        toolCalls: [askUserQuestionToolCall(PROVIDER, 'qwen-question', [{
          question: 'Which color do you want?',
          header: 'Color',
          options: [{ label: 'Red', description: 'The red one' }, { label: 'Blue', description: 'The blue one' }],
        }])],
      },
      { text: 'You chose Blue.' },
    )
    await sendMessage(page, modelScript.prompt('Ask me for a color.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Which color do you want?')
    await page.locator('[data-testid="question-option-Blue"]:visible').click()
    const submit = page.locator('[data-testid="control-submit-btn"]:visible')
    await expect(submit).toBeEnabled()
    await submit.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // Qwen's native reply field carries the answer. Its tool result states that answer.
    // The saved answer appears under the question header.
    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('**Color**: Blue')
    await expect(messageBubbles(page).filter({ hasText: 'Color: Blue' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'You chose Blue.' })).toBeVisible()
  })
})
