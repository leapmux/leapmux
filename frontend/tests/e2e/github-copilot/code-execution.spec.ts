import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { COPILOT_AGENT, copilotTest } from '../copilot-fixtures'
import { expectNativeCodeExecutionAbsent, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { nativeContext } from './scenarios'
import { readCopilotBuiltinCatalog } from './toolCatalog'

copilotTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context, COPILOT_AGENT, { overrides: { optionValues: { permissionMode: COPILOT_PERMISSION_MODE.Manual } } })
  const complete = await readCopilotBuiltinCatalog(context)
  expect(complete).toHaveLength(15)
  expect(complete).toContain('bash')
  expect(complete.filter(name => /^(?:exec|eval|repl|codemode|code_execution|js_execution)$/.test(name))).toEqual([])
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})
