import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'
import { nativeContext } from './scenarios'
import { readInstalledCursorExecutionCases } from './toolCatalog'

cursorTest('confirms the native client execution protocol omits a general code executor', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeCatalogTurn(context)
  const cases = readInstalledCursorExecutionCases(leapmuxServer.agentEnv)
  expect(cases).toHaveLength(33)
  expect(cases).toContain('shell_args')
  expect(cases.filter(name => /^(?:code_execution|js_execution|execute|exec|eval|repl|codemode)_args$/.test(name))).toEqual([])
  const callId = 'native-cursor-catalog-shell'
  await runCursorNativeOperations(context, [
    bashToolCall(context.provider, callId, 'printf "CURSORCATALOG%s\\n" "$((40 + 2))"'),
  ], 'CURSORCATALOG42')
  expect(await cursorNativeToolOutput(context, callId)).toMatchObject({ exitCode: 0, stdout: 'CURSORCATALOG42\n', stderr: '' })
})
