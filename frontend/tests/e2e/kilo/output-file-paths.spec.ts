import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KILO_AGENT, kiloTest } from '../kilo-fixtures'
import { openCodeTailWindowMarkers, readOpenCodeNativeOutput } from '../opencode/outputFilePaths'
import { nativeContext } from './scenarios'

kiloTest('keeps the native output path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, KILO_AGENT, { workingDir: createTestDirectory('native-output-path-kilo-') })
  await openWorkspace(page, context.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(context, testInfo, {
    output,
    callId: 'native-output-path',
    // Kilo builds on OpenCode and writes the same native tool result, so the OpenCode reader reads it. Kilo also
    // keeps OpenCode's shell reader, so the preview can hold any tail window of the output (see openCodeTailWindowMarkers).
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readOpenCodeNativeOutput, openCodeTailWindowMarkers(output)),
  })
})
