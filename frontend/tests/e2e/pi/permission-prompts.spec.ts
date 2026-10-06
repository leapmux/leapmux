import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { expectNoControlBanner, messageBubbles, openWorkspace } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { nativeContext } from './scenarios'

piTest('preserves complete native MCP arguments without an adapter permission dialog', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-permission-')
  const input = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const server = writeMcpFormServer(directory, 'permission-server.mjs', { expectedEchoArguments: input, receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [server.name]: server })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // The tool turn allows any banner that appears, so the observation proves that the MCP call raises none.
    await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: async () => {
      const { resultRequest } = await runNativeToolTurn(context, {
        toolCalls: [mcpToolCall(context.provider, 'native-mcp-arguments', { server: server.name, tool: 'echo', input })],
        prompt: 'Run the native MCP argument probe.',
        answer: 'The native MCP arguments reached the server.',
      })
      expect(nativeToolResult(resultRequest, 'native-mcp-arguments')).toContain('PERMISSION_ACCEPTED')
      expect(readMcpServerReceipt(receiptLog).toolResults).toEqual([{ id: expect.anything(), tool: 'echo', text: 'PERMISSION_ACCEPTED', isError: false }])
      await expect(messageBubbles(page).filter({ hasText: 'END_MCP_ARGUMENTS' }).first()).toBeVisible()
      await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
    } })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
    await expectNoControlBanner(page)
  })
})

piTest('executes a native file change without a permission request', async ({ native }) => {
  await exerciseNativeToolWrite(native, { permission: 'absent' })
})
