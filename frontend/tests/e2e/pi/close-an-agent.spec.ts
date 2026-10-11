import { join } from 'node:path'
import { expect } from '@playwright/test'
import { configuredMcpScript, MCP_ECHO_SERVER_NAME } from '../helpers/mcpEchoServer'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('closes the native agent and its actual owned process tree', async ({ native, leapmuxServer }) => {
  const script = configuredMcpScript(join(leapmuxServer.piAgentDir, 'mcp.json'), 'mcpServers', MCP_ECHO_SERVER_NAME)
  await exerciseCloseAgent(native, { nativeOwnership: ({ rows, ownership }) => {
    expect(rows.some(row => ownership.ownedPids.includes(row.pid) && (row.rawCommand ?? row.command).includes(script))).toBe(true)
  } })
})
