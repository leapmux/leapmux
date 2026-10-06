import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { OPENCODE_AGENT, opencodeTest } from '../opencode-fixtures'
import { proveOpenCodeOutputFilePaths } from './outputFilePathsScenario'

opencodeTest('keeps the native output path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await openProviderAgent(leapmuxServer, native.workspaceId, OPENCODE_AGENT, { workingDir: createTestDirectory('native-output-path-opencode-') })
  await openWorkspace(page, native.workspaceId)
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-path',
    proof: capture => proveOpenCodeOutputFilePaths(capture, testInfo),
  })
})
