import { expectNativeCodeExecutionAbsent, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { JUNIE_AGENT, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context, JUNIE_AGENT)
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})
