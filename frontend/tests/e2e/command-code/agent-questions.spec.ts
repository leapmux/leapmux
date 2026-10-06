import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { commandCodeLoadToolsToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { commandCodeLoadedToolNames, commandCodeToolCatalog } from './toolCatalog'

commandCodeTest('confirms the native question tool is withheld and no question form opens', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return the actual native tool catalog.', 'The native catalog turn completed.')
  expect(commandCodeToolCatalog(request)).not.toContain('ask_user_question')
  await expectNoNativeControl(context, { testId: 'control-banner', additionalTestIds: ['elicitation-form'], relatedProof: async () => {
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: [commandCodeLoadToolsToolCall('find-native-question', 'select:ask_user_question')] }, { text: 'The native question lookup completed.' })
    await sendMessage(page, modelScript.prompt('Look up the exact native question capability.'))
    const status = await modelScript.waitForSteps(start + 2)
    await waitForNativeToolSteps(context, start + 2)
    // The native lookup is a fuzzy search, so it loads the closest tool of the catalog when no tool has that name.
    const lookup = nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), 'find-native-question')
    expect(lookup).toMatch(/^(?:No deferred tool matched|Loaded \d+ tool schema\(s\))/u)
    expect(commandCodeLoadedToolNames(lookup)).not.toContain('ask_user_question')
  } })
})
