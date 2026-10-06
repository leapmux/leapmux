import { join } from 'node:path'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { createTestDirectory } from '../helpers/runDirectory'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { withDeepseekHarnessMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('proves the actual native client refuses MCP input and returns the refusal to its model', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('deepseek-mcp-input-')
  const receiptLog = join(directory, 'receipts.json')
  const server = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withDeepseekHarnessMcp(context, { server, workingDir: directory }, async (privateContext) => {
    await expectUnsupportedMcpInput(privateContext, { receiptLog, callId: 'native-form', invoke: () => invokeNativeMcpTool(privateContext, { server: server.name, tool: 'ask', callId: 'native-form', input: {} }) })
  })
})
