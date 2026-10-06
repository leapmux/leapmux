import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { qwenTest } from '../qwen-fixtures'
import { nativeContext } from './scenarios'

qwenTest('returns the actual native MCP unsupported-method reply without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const configurationPath = join(leapmuxServer.agentEnv.QWEN_HOME!, 'settings.json')
  await withNativeMcpFormAgent(context, {
    directoryPrefix: 'qwen-native-mcp-refusal-',
    configurationPath,
    configuration: (server) => {
      const settings: unknown = JSON.parse(readFileSync(configurationPath, 'utf8'))
      if (!isObject(settings))
        throw new Error('The private Qwen settings must hold a JSON object.')
      return { ...settings, ...mcpServersConfig(server) }
    },
  }, async ({ server, receiptLog }) => {
    await waitForMcpToolListed(receiptLog, 'ask')
    const callId = 'native-qwen-form-refusal'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
  })
})
