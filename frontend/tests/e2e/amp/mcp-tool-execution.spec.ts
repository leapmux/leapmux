import { join } from 'node:path'
import { AMP_ALLOW_ALL, ampTest } from '../amp-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { ampMcpSettings, ampSettingsPath } from './mcpConfiguration'
import { AMP_AGENT, nativeContext } from './scenarios'

ampTest.describe('Amp MCP tool execution', () => {
  ampTest('runs an isolated MCP echo tool through the local executor', async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }) => {
    const workingDir = newProviderWorkingDir(AMP_AGENT, 'amp-mcp-echo-')
    const receiptLog = join(workingDir, 'native-mcp-receipt.json')
    const server = writeMcpEchoServer(workingDir, { receiptLog })
    const config = ampSettingsPath(leapmuxServer.agentEnv)
    const content = JSON.stringify(ampMcpSettings(config, server))
    await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
      await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, AMP_AGENT, { workingDir, ...AMP_ALLOW_ALL })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const context = await nativeContext({
        page,
        modelScript,
        leapmuxServer,
        workspaceId: authenticatedEmptyWorkspace.workspaceId,
      })
      // The first turn starts Amp. Its MCP discovery must finish before a scripted tool call.
      await sendNativeAnswer(context, 'Start the native session that loads the echo server.', 'The native echo session started.')
      await waitForMcpToolListed(receiptLog, 'echo')
      await exerciseMcpEcho(context, 'amp', { receiptLog })
    })
  })
})
