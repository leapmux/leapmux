import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { claudeTest } from '../claude-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { openWorkspace } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { CLAUDE_AGENT, nativeContext } from './scenarios'

claudeTest.describe('Claude Code MCP input form', () => {
  claudeTest('sends zero and false form values back to the native MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const directory = newProviderWorkingDir(CLAUDE_AGENT, 'claude-mcp-form-')
    writeFileSync(join(directory, '.mcp.json'), JSON.stringify(mcpServersConfig(writeMcpFormServer(directory, 'form-server.mjs'))))
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CLAUDE_AGENT, { workingDir: directory, optionValues: { permissionMode: 'bypassPermissions' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseMcpProbeFormRoundTrip(context, { callId: 'claude-mcp-form', reloadBeforeSubmit: false })
  })
})
