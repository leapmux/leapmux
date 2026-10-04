import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

for (const answerKind of ['custom', 'selected']) {
  test(`delivers a ${answerKind} answer to the real Pi question extension`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const provider = AgentProvider.PI
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-custom-'), {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
    })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    // The EXTENSION is real -- it is what turns the tool call into a control
    // request and carries the answer back. Only the decision to ask is scripted.
    await modelScript.queue({
      toolCalls: [askUserQuestionToolCall(provider, 'style-question', [{
        question: 'Choose a style',
        header: 'Style',
        options: [
          { label: 'Alpha', description: 'Use the first style.' },
          { label: 'Beta', description: 'Use the second style.' },
        ],
      }])],
    })
    await modelScript.queue({ text: 'Recorded the style.' })
    await sendMessage(page, modelScript.prompt('Ask me which style to use.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Choose a style')
    if (answerKind === 'custom') {
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror').filter({ visible: true })
      await editor.click()
      await page.keyboard.insertText('A custom style')
    }
    else {
      await banner.getByTestId('question-option-Beta').click()
    }
    await page.getByTestId('control-submit-btn').click()
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const answerRequest = status.requests.find(request => request.stepIndex === 1)
    const answer = nativeToolResult(answerRequest, 'style-question')
    expect(answer).toContain(answerKind === 'custom' ? 'A custom style' : 'Beta')
    expect(answer).not.toContain('Alpha')
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const result = chat.locator('[data-testid="message-bubble"][data-role="agent"]').filter({ hasText: 'User has answered your questions:' }).filter({ visible: true })
    await expect(result).toContainText(answerKind === 'custom' ? 'A custom style' : 'Beta')
    await expect(banner).toHaveCount(0)
    await expect(chat.getByText('User declined to answer questions', { exact: true })).toHaveCount(0)
  })
}
