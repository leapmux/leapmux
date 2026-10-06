import { droidTest } from '../droid-fixtures'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { droidExecuteToolCall } from '../helpers/providerToolCalls'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { readDroidNativeOutput } from './outputFilePaths'
import { DROID_AGENT, nativeContext } from './scenarios'
import { nativeDroidCallId } from './toolResult'

droidTest('keeps the native filesystem path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, DROID_AGENT, { directoryPrefix: 'native-output-path-' })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-path',
    call: (output, callId) => droidExecuteToolCall(callId, { command: nativeOutputFileCommand(output), summary: 'Print the computed native output', riskLevel: 'low' }),
    nativeCallId: (request, callId) => nativeDroidCallId(request, 'Execute', callId),
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readDroidNativeOutput),
  })
})
