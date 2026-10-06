import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codewhaleToolSearchToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codewhaleTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, { directoryPrefix: 'native-code-execution-' })
  const start = await modelScript.queue({ toolCalls: [codewhaleToolSearchToolCall('discover-native-executor', 'execute_tools')] }, { text: 'The native executor schema is loaded.' })
  await sendMessage(page, modelScript.prompt('Discover the native execute_tools schema.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  // The request after the discovery holds the discovery result and the catalog that the discovery loaded.
  const catalog = await modelScript.requestAt(start + 1)
  expect(JSON.parse(nativeToolResult(catalog, 'discover-native-executor'))).toEqual({
    type: 'tool_search_tool_search_result',
    tool_references: [{ type: 'tool_reference', tool_name: 'execute_tools' }],
    unavailable_tool_references: [],
  })
  nativeCodeExecutionSchema(catalog, 'execute_tools', { code: 'string' })
  await exerciseNativeCodeExecution(context, {
    catalogProof: (request) => {
      nativeCodeExecutionSchema(request, 'execute_tools', { code: 'string' })
    },
    scripts: marker => [
      { label: 'output', source: `return ${JSON.stringify(marker)} + (40 + 2);`, expected: `${marker}42`, failed: false },
      { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
    ],
  })
})
