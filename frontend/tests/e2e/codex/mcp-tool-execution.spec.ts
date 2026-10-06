import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { codexTest } from '../codex-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt, waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { messageBubbles, openWorkspace } from '../helpers/ui'
import { nativeContext } from './scenarios'

codexTest('executes the native MCP echo tool and preserves its argument refusal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const codexHome = leapmuxServer.agentEnv?.CODEX_HOME
  if (!codexHome)
    throw new Error('The native MCP scenario requires the isolated Codex home.')
  assertPrivateNativePath(codexHome, getGlobalState().tmpDir)
  const directory = createTestDirectory('codex-mcp-execution-')
  const receiptLog = join(directory, 'native-receipt.json')
  // The Codex configuration of the run starts this form server. The test replaces its script with one that records a
  // receipt and checks the echo arguments, and restores the script after the test.
  const serverPath = join(codexHome, 'form-server.mjs')
  const originalScript = readFileSync(serverPath, 'utf8')
  const expectedEchoArguments = { query: 'NATIVE_CODEX_QUERY', limit: 0, tail: 'NATIVE_CODEX_TAIL' }
  await withCleanup(async () => {
    const server = writeMcpFormServer(codexHome, 'form-server.mjs', { receiptLog, expectedEchoArguments })
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, directory, agentOpenOptions(context.provider))
    await openWorkspace(page, context.workspaceId)
    for (const valid of [true, false]) {
      const callId = valid ? 'codex-native-mcp-accepted' : 'codex-native-mcp-refused'
      const marker = valid ? 'PERMISSION_ACCEPTED' : 'PERMISSION_ARGUMENTS_FAILED'
      const request = await invokeNativeMcpTool(context, { server: server.name, tool: 'echo', callId, input: { ...expectedEchoArguments, query: valid ? expectedEchoArguments.query : 'WRONG_NATIVE_QUERY' } })
      await waitForMcpToolListed(receiptLog, 'echo')
      expect(nativeToolResult(request, callId)).toContain(marker)
      const receipt = readMcpServerReceipt(receiptLog)
      expect(receipt.toolResults.at(-1)).toMatchObject({ tool: 'echo', text: marker, isError: !valid })
      expect(receipt.elicitationRequests).toEqual([])
      await expect(messageBubbles(page).filter({ hasText: marker }).first()).toBeVisible()
    }
  }, async () => writeFileSync(serverPath, originalScript))
})
