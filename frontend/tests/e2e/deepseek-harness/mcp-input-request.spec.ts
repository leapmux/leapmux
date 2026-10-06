import { join } from 'node:path'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { createTestDirectory } from '../helpers/runDirectory'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { invokeDeepseekHarnessMcp, withDeepseekHarnessMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('proves the actual native client refuses MCP input and returns the refusal to its model', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('deepseek-mcp-input-')
  const receiptLog = join(directory, 'receipts.json')
  const script = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withDeepseekHarnessMcp(context, { name: 'forms', script, workingDir: directory }, async (privateContext) => {
    await expectUnsupportedMcpInput(privateContext, { receiptLog, callId: 'native-form', invoke: () => invokeDeepseekHarnessMcp(privateContext, { server: 'forms', tool: 'ask', callId: 'native-form', input: {} }) })
  })
})
