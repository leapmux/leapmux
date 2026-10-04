import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { fastAgentTest } from '../fastagent-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { assertFastAgentShellCatalog, readFastAgentCompleteCatalog } from './toolCatalog'

fastAgentTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.FAST_AGENT }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  // The native command reads all runtime tools. Each following command supplies one exact input schema.
  assertFastAgentShellCatalog(await readFastAgentCompleteCatalog(context))
  await exerciseShellToolExecution(context, { includeFailure: false })
})
