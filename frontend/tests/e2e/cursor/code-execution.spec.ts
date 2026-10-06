import { expect } from '@playwright/test'
import { CURSOR_AGENT, cursorTest } from '../cursor-fixtures'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'
import { nativeContext } from './scenarios'
import { readInstalledCursorToolCases } from './toolCatalog'

cursorTest('confirms the complete native protocol omits a general code executor', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeCatalogTurn(context, CURSOR_AGENT)
  const cases = readInstalledCursorToolCases(leapmuxServer.agentEnv)
  expect(cases).toHaveLength(69)
  expect(cases).toContain('shell_tool_call')
  expect(cases.filter(name => /^(?:code_execution|js_execution|exec|eval|repl|codemode)_tool_call$/.test(name))).toEqual([])
  const callId = 'native-cursor-catalog-shell'
  await runCursorNativeOperations(context, [bashToolCall(context.provider, callId, 'printf "CURSORCATALOG%s\\n" "$((40 + 2))"')], 'CURSORCATALOG42')
  expect(await cursorNativeToolOutput(context, callId)).toMatchObject({ exitCode: 0, stdout: 'CURSORCATALOG42\n', stderr: '' })
})
