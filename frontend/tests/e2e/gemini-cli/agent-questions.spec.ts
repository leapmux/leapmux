import { expect } from '@playwright/test'
import { geminiExtractControl } from '../../../src/components/chat/providers/gemini/extractControl'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { nativeContext } from './scenarios'

geminiTest('excludes the unsupported native question control while a real permission still works', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return the native control catalog.', 'The native control catalog reached the mock.')
  expect(nativeModelToolNames(request)).toContain('run_shell_command')
  expect(nativeModelToolNames(request)).not.toContain('ask_user')
  const operation = await createNativePermissionFileWrite(context, { fileName: 'gemini-native-question-control.txt', callId: 'gemini-native-question-control', outputPrefix: 'GEMINICONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'question',
    classify: geminiExtractControl,
    isQuestionRequest: payload => payload.method === 'ask_user',
    relatedProof: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: operation.nativeProof,
    }),
  })
})
