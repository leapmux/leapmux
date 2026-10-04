import { Buffer } from 'node:buffer'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { clineTest } from '../cline-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { readClineCompleteCatalog } from './toolCatalog'

clineTest('checks the complete native builtin inventory and executes the actual shell tool', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  const catalog = await readClineCompleteCatalog(context, receipt => testInfo.attach('cline-native-catalog-command-receipt', { body: Buffer.from(JSON.stringify(receipt)), contentType: 'application/json' }))
  await testInfo.attach('cline-complete-native-registry', { body: Buffer.from(JSON.stringify(catalog)), contentType: 'application/json' })
  await exerciseShellToolExecution(context, { includeFailure: false })
})
