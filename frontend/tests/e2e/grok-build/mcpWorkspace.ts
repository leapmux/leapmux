import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { answerControl, expectNoControlBanner, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT } from './scenarios'

/** Open an actual project MCP server through Grok's native trust decision. */
export async function openGrokMcpWorkspace(context: ManagedNativeScenarioContext, decision: 'allow' | 'deny'): Promise<{ workingDir: string, receiptLog: string, serverName: string }> {
  const workingDir = newProviderWorkingDir(GROK_AGENT)
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const mcpServer = writeMcpEchoServer(workingDir, { receiptLog })
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(mcpServer)))
  const server = context.leapmuxServer
  await openProviderAgent(server, context.workspaceId, GROK_AGENT, { workingDir, optionValues: { approvalMode: 'always-approve' } })
  await openWorkspace(context.page, context.workspaceId)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText('Trust the workspace')
  await expect(banner).toContainText('mcp')
  expect(existsSync(receiptLog)).toBe(false)
  await answerControl(context.page, decision)
  await expectNoControlBanner(context.page)
  if (decision === 'allow')
    await waitForMcpToolListed(receiptLog, 'echo')
  return { workingDir, receiptLog, serverName: mcpServer.name }
}
