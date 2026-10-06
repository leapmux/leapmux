import { CODEBUDDY_AGENT, codebuddyTest } from '../codebuddy-fixtures'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { readCodeBuddyNativeOutput } from './outputFilePaths'
import { nativeContext } from './scenarios'

codebuddyTest('keeps the native filesystem path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, CODEBUDDY_AGENT, { workingDir: createTestDirectory('native-output-path-') })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-path',
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readCodeBuddyNativeOutput),
  })
})
