import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { codexTest } from '../codex-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { messageBubbles, openWorkspace, sendMessage } from '../helpers/ui'

codexTest('executes the native MCP echo tool and preserves its argument refusal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const codexHome = leapmuxServer.agentEnv?.CODEX_HOME
  if (!codexHome)
    throw new Error('The native MCP scenario requires the isolated Codex home.')
  assertPrivateNativePath(codexHome, getGlobalState().tmpDir)
  const directory = createTestDirectory('codex-mcp-execution-')
  const receiptLog = join(directory, 'native-receipt.json')
  const serverPath = join(codexHome, 'form-server.mjs')
  const originalScript = readFileSync(serverPath, 'utf8')
  const expectedEchoArguments = { query: 'NATIVE_CODEX_QUERY', limit: 0, tail: 'NATIVE_CODEX_TAIL' }
  await withCleanup(async () => {
    writeMcpFormServer(codexHome, 'form-server.mjs', { receiptLog, expectedEchoArguments })
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, agentOpenOptions(AgentProvider.CODEX))
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = { page, modelScript, provider: AgentProvider.CODEX }
    for (const valid of [true, false]) {
      const start = (await modelScript.status()).stepCount
      const callId = valid ? 'codex-native-mcp-accepted' : 'codex-native-mcp-refused'
      const marker = valid ? 'PERMISSION_ACCEPTED' : 'PERMISSION_ARGUMENTS_FAILED'
      await modelScript.queue(
        { toolCalls: [mcpToolCall(AgentProvider.CODEX, callId, { server: 'form_probe', tool: 'echo', input: { ...expectedEchoArguments, query: valid ? expectedEchoArguments.query : 'WRONG_NATIVE_QUERY' } })] },
        { text: 'The native MCP execution turn ended.' },
      )
      await sendMessage(page, modelScript.prompt('Call the native form_probe echo tool with the supplied arguments.'))
      await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
      await waitForNativeToolSteps(context, start + 2)
      const request = (await modelScript.status()).requests.find(record => record.stepIndex === start + 1)
      expect(nativeToolResult(request, callId)).toContain(marker)
      const receipt = readMcpServerReceipt(receiptLog)
      expect(receipt.toolResults.at(-1)).toMatchObject({ tool: 'echo', text: marker, isError: !valid })
      expect(receipt.elicitationRequests).toEqual([])
      await expect(messageBubbles(page).filter({ hasText: marker }).first()).toBeVisible()
    }
  }, async () => writeFileSync(serverPath, originalScript))
})
