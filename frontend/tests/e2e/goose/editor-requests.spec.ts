import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

gooseTest('resolves an actual native control without exposing a multiline editor request', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  const todo = updateTodosToolCall(AgentProvider.GOOSE, 'native-editor-limit-todo', [{ step: 'Native editor control proof', status: 'pending' }])
  const checklist = todo.arguments?.content
  if (typeof checklist !== 'string')
    throw new Error('The Goose to-do call carries no checklist.')
  await expectNoNativeEditorRequest(context, {
    relatedProof: async () => {
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ toolCalls: [todo] }, { text: 'The native approval proof ended.' })
      await sendMessage(page, modelScript.prompt('Write the scripted native to-do item.'))
      await modelScript.waitForSteps(start + 1)
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Native editor control proof')
      await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      // Goose 1.53.0 `todo__todo_write` (platform_extensions/todo.rs) does not
      // repeat the checklist. It answers with the count of Unicode code points
      // that it stored. This result proves that Goose ran the approved call and
      // stored a checklist of the scripted length.
      expect(nativeToolResult((await modelScript.status()).requests.find(record => record.stepIndex === start + 1), 'native-editor-limit-todo')).toBe(`Updated (${[...checklist].length} chars)`)
    },
  })
})
