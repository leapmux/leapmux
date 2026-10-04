import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { kiloTest } from '../kilo-fixtures'

kiloTest('resolves an actual native control without exposing a multiline editor request', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await expectNoNativeEditorRequest(context, {
    relatedControl: async () => {
      const question = askUserQuestionToolCall(AgentProvider.KILO, 'native-editor-limit-question', [{ question: 'Choose the native control proof.', header: 'Proof', options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Green', description: 'Use green.' }] }])
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ toolCalls: [question] }, { text: 'The native control proof ended.' })
      await sendMessage(page, modelScript.prompt('Ask the scripted native control question.'))
      await modelScript.waitForSteps(start + 1)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Choose the native control proof.')
      await banner.getByTestId('question-option-Green').click()
      await page.getByTestId('control-submit-btn').filter({ visible: true }).click()
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      const request = (await modelScript.status()).requests.find(record => record.stepIndex === start + 1)
      expect(nativeToolResult(request, 'native-editor-limit-question')).toContain('Green')
    },
  })
})
