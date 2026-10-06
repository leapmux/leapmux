import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLAUDE_AGENT, claudeTest } from '../claude-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

claudeTest('executes the native MCP echo tool and keeps empty string results', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = createTestDirectory('claude-mcp-execution-')
  const receiptLog = join(directory, 'native-receipt.json')
  const script = writeMcpEchoServer(directory, { receiptLog })
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { echo_probe: { command: process.execPath, args: [script] } } }))
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CLAUDE_AGENT, { workingDir: directory, optionValues: { permissionMode: 'bypassPermissions' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLAUDE_CODE, 'CLAUDE_NATIVE_ECHO', { receiptLog })
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLAUDE_CODE, '', { receiptLog })
})
