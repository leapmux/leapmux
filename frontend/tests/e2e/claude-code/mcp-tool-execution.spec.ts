import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { claudeTest } from '../claude-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { CLAUDE_AGENT, nativeContext } from './scenarios'

claudeTest('executes the native MCP echo tool and keeps empty string results', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = newProviderWorkingDir(CLAUDE_AGENT, 'claude-mcp-execution-')
  const receiptLog = join(directory, 'native-receipt.json')
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify(mcpServersConfig(writeMcpEchoServer(directory, { receiptLog }))))
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CLAUDE_AGENT, { workingDir: directory, optionValues: { permissionMode: 'bypassPermissions' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseMcpEcho(context, 'CLAUDE_NATIVE_ECHO', { receiptLog })
  await exerciseMcpEcho(context, '', { receiptLog })
})
