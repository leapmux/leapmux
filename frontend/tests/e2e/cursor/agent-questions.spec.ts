import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('sends the selected question answer to the native Run stream', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await modelScript.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CURSOR, 'cursor-color', [{
    header: 'Color',
    question: 'Which color should I use?',
    options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }],
  }])] })
  await sendMessage(page, modelScript.prompt('Ask which color to use.'))
  await modelScript.waitForSteps()
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Which color should I use?')
  await banner.getByTestId('question-option-Blue').click()
  await page.getByTestId('control-submit-btn').filter({ visible: true }).click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor question selected: option-1-1' }).first()).toBeVisible()
})
