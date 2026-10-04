import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'

test('preserves complete native MCP arguments without an adapter permission dialog', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-permission-')
  const input = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const script = writeMcpFormServer(directory, 'permission-server.mjs', { expectedEchoArguments: input, receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { form_probe: { command: process.execPath, args: [script] } })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.PI }
    await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: async () => {
      await modelScript.queue(
        { toolCalls: [mcpToolCall(AgentProvider.PI, 'native-mcp-arguments', { server: 'form_probe', tool: 'echo', input })] },
        { text: 'The native MCP arguments reached the server.' },
      )
      await sendMessage(page, modelScript.prompt('Run the native MCP argument probe.'))
      const status = await modelScript.waitForSteps(2)
      await waitForAgentIdle(page)
      expect(nativeToolResult(status.requests.find(record => record.stepIndex === 1), 'native-mcp-arguments')).toContain('PERMISSION_ACCEPTED')
      expect(readMcpServerReceipt(receiptLog).toolResults).toEqual([{ id: expect.anything(), tool: 'echo', text: 'PERMISSION_ACCEPTED', isError: false }])
      await expect(messageBubbles(page).filter({ hasText: 'END_MCP_ARGUMENTS' }).first()).toBeVisible()
      await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
    } })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
  })
})

piTest('executes a native file change without a permission request', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseNativeToolWrite({ page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }, { permission: 'absent' })
})
