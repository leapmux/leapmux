import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { uniqueMarker } from '../helpers/shellArguments'

geminiTest('changes the native model and restores the selected model after reload', async ({ native }) => {
  const before = await currentNativeAgent(native)
  const marker = uniqueMarker('GEMINIMODELCONTEXT')
  await sendNativeAnswer(native, `Preserve ${marker} before the model change.`, `The previous model preserved ${marker}.`)
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: 'gemini-3.8-flash',
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-3.8-flash:')
      expect(nativeModelContextText(request)).toContain(`Preserve ${marker} before the model change.`)
      expect(nativeModelContextText(request)).toContain(`The previous model preserved ${marker}.`)
      const current = await currentNativeAgent(native)
      expect(current.id).toBe(before.id)
      expect(current.agentSessionId).toBe(before.agentSessionId)
    },
  })
})
