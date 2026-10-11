import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { answerControl, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

gooseTest('resolves an actual native control without exposing a multiline editor request', async ({ native }) => {
  const { page, modelScript } = native
  const todo = updateTodosToolCall(native.provider, 'native-editor-limit-todo', [{ step: 'Native editor control proof', status: 'pending' }])
  const checklist = todo.arguments?.content
  if (typeof checklist !== 'string')
    throw new Error('The Goose to-do call carries no checklist.')
  await expectNoNativeEditorRequest(native, {
    relatedProof: async () => {
      const start = await modelScript.queue({ toolCalls: [todo] }, { text: 'The native approval proof ended.' })
      await sendMessage(page, modelScript.prompt('Write the scripted native to-do item.'))
      await modelScript.waitForSteps(start + 1)
      const banner = await waitForControlBanner(page)
      await expect(banner).toContainText('Native editor control proof')
      await answerControl(page, 'allow')
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      // Goose 1.53.0 `todo__todo_write` (platform_extensions/todo.rs) does not
      // repeat the checklist. It answers with the count of Unicode code points
      // that it stored. This result proves that Goose ran the approved call and
      // stored a checklist of the scripted length.
      expect(nativeToolResult(await modelScript.requestAt(start + 1), 'native-editor-limit-todo')).toBe(`Updated (${[...checklist].length} chars)`)
    },
  })
})
