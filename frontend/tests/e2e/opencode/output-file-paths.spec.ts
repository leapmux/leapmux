import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { opencodeTest } from '../opencode-fixtures'
import { openCodeTailWindowMarkers, readOpenCodeNativeOutput } from './outputFilePaths'
import { nativeContext, OPENCODE_AGENT } from './scenarios'

opencodeTest('keeps the native output path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, OPENCODE_AGENT, { directoryPrefix: 'native-output-path-opencode-' })
  await openWorkspace(page, context.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(context, testInfo, {
    output,
    callId: 'native-output-path',
    // OpenCode ends its output reader when the process exits, so the preview can hold any tail window of the output.
    // The marker check accepts any line of it, and the absence check uses the first line, which no tail window holds.
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readOpenCodeNativeOutput, openCodeTailWindowMarkers(output)),
  })
})
