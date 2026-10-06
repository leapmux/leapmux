import { expect } from '@playwright/test'
import { geminiExtractControl } from '../../../src/components/chat/providers/gemini/extractControl'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

geminiTest('excludes the unsupported native question control while a real permission still works', async ({ native }) => {
  const request = await sendNativeAnswer(native, 'Return the native control catalog.', 'The native control catalog reached the mock.')
  expect(nativeModelToolNames(request)).toContain('run_shell_command')
  expect(nativeModelToolNames(request)).not.toContain('ask_user')
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'question',
    classify: geminiExtractControl,
    isQuestionRequest: payload => payload.method === 'ask_user',
  })
})
