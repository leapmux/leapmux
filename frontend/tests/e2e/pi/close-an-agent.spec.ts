import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('can close Pi agent tab', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const tabs = page.locator('[data-testid="tab"]:visible')
  const tabsBefore = await tabs.count()
  expect(tabsBefore).toBeGreaterThan(0)
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseCloseAgent(context)
  await expect(tabs).toHaveCount(tabsBefore - 1)
})

piTest('closes the native agent and its actual owned process tree', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  const config: unknown = JSON.parse(readFileSync(join(leapmuxServer.piAgentDir, 'mcp.json'), 'utf8'))
  const servers = isObject(config) && isObject(config.mcpServers) ? config.mcpServers : null
  const echo = servers && isObject(servers.echo_probe) ? servers.echo_probe : null
  const script = echo && Array.isArray(echo.args) ? echo.args[0] : undefined
  if (typeof script !== 'string' || !script)
    throw new Error('The native Pi close test requires its configured MCP executable.')
  await exerciseCloseAgent(context, { nativeOwnership: ({ rows, ownership }) => {
    expect(rows.some(row => ownership.ownedPids.includes(row.pid) && (row.rawCommand ?? row.command).includes(script))).toBe(true)
  } })
})
