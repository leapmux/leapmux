import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

cursorTest('resolves an actual native control without exposing a multiline editor request', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await expectNoNativeEditorRequest(context, {
    relatedProof: async () => {
      const question = askUserQuestionToolCall(AgentProvider.CURSOR, 'native-editor-limit-question', [{ question: 'Choose the native control proof.', header: 'Proof', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] }])
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ toolCalls: [question] })
      await sendMessage(page, modelScript.prompt('Ask the scripted native control question.'))
      await modelScript.waitForSteps(start + 1)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Choose the native control proof.')
      await banner.getByTestId('question-option-Green').click()
      await page.getByTestId('control-submit-btn').filter({ visible: true }).click()
      await modelScript.waitForSteps(start + 1)
      await waitForAgentIdle(page)
      await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'Cursor question selected: option-1-2' }).first()).toBeVisible()
    },
  })
})
