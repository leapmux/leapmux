import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'

test('executes the native MCP echo tool and keeps empty string results', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = createTestDirectory('claude-mcp-execution-')
  const receiptLog = join(directory, 'native-receipt.json')
  const script = writeMcpEchoServer(directory, { receiptLog })
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { echo_probe: { command: process.execPath, args: [script] } } }))
  const settings = agentOpenOptions(agentSettings(AgentProvider.CLAUDE_CODE))
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
    agentProvider: AgentProvider.CLAUDE_CODE,
    ...settings,
    optionValues: { ...settings.optionValues, permissionMode: 'bypassPermissions' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLAUDE_CODE, 'CLAUDE_NATIVE_ECHO', { receiptLog })
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLAUDE_CODE, '', { receiptLog })
})
