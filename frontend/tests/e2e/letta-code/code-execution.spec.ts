import { expectNativeCodeExecutionAbsent, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { LETTA_AGENT, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context, LETTA_AGENT)
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})
