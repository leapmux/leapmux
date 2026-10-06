import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createGrokWorkingDir, GROK_AGENT } from '../grok-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { controlButton, expectNoControlBanner, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

/** Open an actual project MCP server through Grok's native trust decision. */
export async function openGrokMcpWorkspace(context: ManagedNativeScenarioContext, decision: 'allow' | 'deny'): Promise<{ workingDir: string, receiptLog: string }> {
  const workingDir = createGrokWorkingDir()
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(writeMcpEchoServer(workingDir, { receiptLog }))))
  const server = context.leapmuxServer
  await openProviderAgent(server, context.workspaceId, GROK_AGENT, { workingDir, optionValues: { approvalMode: 'always-approve' } })
  await openWorkspace(context.page, context.workspaceId)
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText('Trust the workspace')
  await expect(banner).toContainText('mcp')
  expect(existsSync(receiptLog)).toBe(false)
  await controlButton(context.page, decision).first().click()
  await expectNoControlBanner(context.page)
  if (decision === 'allow')
    await waitForMcpToolListed(receiptLog, 'echo')
  return { workingDir, receiptLog }
}
