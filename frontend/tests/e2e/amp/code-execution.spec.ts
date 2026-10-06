import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { ampTest } from '../amp-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.AMP }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), agentOpenOptions(context.provider))
  await openWorkspace(page, context.workspaceId)
  await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  const { tools } = await readAmpExecutorCatalog(context, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
  expect(tools).toContain('shell_command')
  expect(tools.filter(name => /^(?:codemode|exec|eval|REPL|js_execution|code_execution|execute_tools|node_repl)$/.test(name))).toEqual([])
})
