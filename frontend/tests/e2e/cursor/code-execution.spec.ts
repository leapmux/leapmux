import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { cursorTest } from '../cursor-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'
import { readInstalledCursorToolCases } from './toolCatalog'

cursorTest('confirms the complete native protocol omits a general code executor', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  const cases = readInstalledCursorToolCases(leapmuxServer.agentEnv)
  expect(cases).toHaveLength(69)
  expect(cases).toContain('shell_tool_call')
  expect(cases.filter(name => /^(?:code_execution|js_execution|exec|eval|repl|codemode)_tool_call$/.test(name))).toEqual([])
  const callId = 'native-cursor-catalog-shell'
  await runCursorNativeOperations(context, [bashToolCall(context.provider, callId, 'printf "CURSORCATALOG%s\\n" "$((40 + 2))"')], 'CURSORCATALOG42')
  expect(await cursorNativeToolOutput(context, callId)).toMatchObject({ exitCode: 0, stdout: 'CURSORCATALOG42\n', stderr: '' })
})
