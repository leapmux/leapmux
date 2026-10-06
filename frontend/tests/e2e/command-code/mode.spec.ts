import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText, nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'

commandCodeTest('applies the native planning mode and preserves context across its process restart', async ({ native: context }) => {
  await sendNativeAnswer(context, 'Keep COMMANDCODE_MODE_CONTEXT before the native process restarts.', 'COMMANDCODE_MODE_ANSWER')
  await exerciseNativeOption(context, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i)
    expect(nativeModelContextText(request)).toContain('COMMANDCODE_MODE_ANSWER')
  } })
})
