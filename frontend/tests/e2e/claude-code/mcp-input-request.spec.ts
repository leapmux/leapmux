import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { CLAUDE_AGENT, claudeTest } from '../claude-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

claudeTest.describe('Claude Code MCP input form', () => {
  claudeTest('sends zero and false form values back to the native MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const directory = createTestDirectory('claude-mcp-form-')
    writeFileSync(join(directory, '.mcp.json'), JSON.stringify(mcpServersConfig(writeMcpFormServer(directory, 'form-server.mjs'))))
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CLAUDE_AGENT, { workingDir: directory, optionValues: { permissionMode: 'bypassPermissions' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseMcpProbeFormRoundTrip(context, { callId: 'claude-mcp-form', reloadBeforeSubmit: false })
  })
})
