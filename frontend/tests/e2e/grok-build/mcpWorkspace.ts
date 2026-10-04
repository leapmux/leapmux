import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { createGrokWorkingDir } from '../grok-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { openWorkspace, waitForControlBanner } from '../helpers/ui'

/** Open an actual project MCP server through Grok's native trust decision. */
export async function openGrokMcpWorkspace(context: ManagedNativeScenarioContext, decision: 'allow' | 'deny'): Promise<{ workingDir: string, receiptLog: string }> {
  const workingDir = createGrokWorkingDir()
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const script = writeMcpEchoServer(workingDir, { receiptLog })
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({ mcpServers: { echo_probe: { command: process.execPath, args: [script] } } }))
  const server = context.leapmuxServer
  await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, workingDir, {
    agentProvider: AgentProvider.GROK_BUILD,
    ...agentOpenOptions(agentSettings(AgentProvider.GROK_BUILD)),
    optionValues: { ...agentOpenOptions(agentSettings(AgentProvider.GROK_BUILD)).optionValues, approvalMode: 'always-approve' },
  })
  await openWorkspace(context.page, context.workspaceId)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText('Trust the workspace')
  await expect(banner).toContainText('mcp')
  expect(existsSync(receiptLog)).toBe(false)
  await context.page.locator(`[data-testid="control-${decision}-btn"]:visible`).first().click()
  await expect(banner).toHaveCount(0)
  if (decision === 'allow') {
    await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
  }
  return { workingDir, receiptLog }
}
