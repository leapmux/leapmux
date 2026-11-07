import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { uniqueMarker } from '../helpers/shellArguments'

antigravityTest('changes the native model and restores the selected model after reload', async ({ native }) => {
  const before = await currentNativeAgent(native)
  const marker = uniqueMarker('GEMINIMODELCONTEXT')
  expect(nativeOptionValue(before, 'model')).toBe('gemini-3.8-flash')
  const baseline = await sendNativeAnswer(native, `Preserve ${marker} before the model change.`, `The previous model preserved ${marker}.`)
  expect(baseline.protocol).toBe('google-generative-language')
  expect(baseline.path).toContain('/models/gemini-3.8-flash:')
  const baselineModel = /\/models\/([^:]+):/.exec(baseline.path)?.[1]
  expect(baselineModel).toBeDefined()
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: 'gemini-3.1-pro',
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-3.1-pro-preview:')
      expect(/\/models\/([^:]+):/.exec(request.path)?.[1]).not.toBe(baselineModel)
      expect(nativeModelContextText(request)).toContain(`Preserve ${marker} before the model change.`)
      expect(nativeModelContextText(request)).toContain(`The previous model preserved ${marker}.`)
      const current = await currentNativeAgent(native)
      expect(current.id).toBe(before.id)
      expect(current.agentSessionId).toBe(before.agentSessionId)
    },
  })
})
