import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('changes the native model and restores the selected model after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const before = await currentNativeAgent(context)
  const marker = `GEMINIMODELCONTEXT${randomUUID().replaceAll('-', '')}`
  await sendNativeAnswer(context, `Preserve ${marker} before the model change.`, `The previous model preserved ${marker}.`)
  await exerciseNativeOption(context, {
    groupId: 'model',
    value: 'gemini-3.8-flash',
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-3.8-flash:')
      expect(nativeModelContextText(request)).toContain(`Preserve ${marker} before the model change.`)
      expect(nativeModelContextText(request)).toContain(`The previous model preserved ${marker}.`)
      const current = await currentNativeAgent(context)
      expect(current.id).toBe(before.id)
      expect(current.agentSessionId).toBe(before.agentSessionId)
    },
  })
})
