import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeLoadToolsToolCall } from '../helpers/providerToolCalls'
import { commandCodeLoadedToolNames, commandCodeToolCatalog } from './toolCatalog'

commandCodeTest('confirms the native question tool is withheld and no question form opens', async ({ native }) => {
  const request = await sendNativeAnswer(native, 'Return the actual native tool catalog.', 'The native catalog turn completed.')
  expect(commandCodeToolCatalog(request)).not.toContain('ask_user_question')
  await expectNoNativeControl(native, { testId: 'control-banner', additionalTestIds: ['elicitation-form'], relatedProof: async () => {
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [commandCodeLoadToolsToolCall('find-native-question', 'select:ask_user_question')],
      prompt: 'Look up the exact native question capability.',
      answer: 'The native question lookup completed.',
    })
    // The native lookup is a fuzzy search, so it loads the closest tool of the catalog when no tool has that name.
    const lookup = nativeToolResult(resultRequest, 'find-native-question')
    expect(lookup).toMatch(/^(?:No deferred tool matched|Loaded \d+ tool schema\(s\))/u)
    expect(commandCodeLoadedToolNames(lookup)).not.toContain('ask_user_question')
  } })
})
