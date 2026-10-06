import { execFileSync } from 'node:child_process'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AMP_ALLOW_ALL, ampTest } from '../amp-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { ampMcpSettings, ampSettingsPath } from './mcpConfiguration'
import { AMP_AGENT, nativeContext } from './scenarios'

ampTest.describe('Amp MCP tool execution', () => {
  ampTest('runs an isolated MCP echo tool through the local executor', async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }) => {
    const workingDir = newProviderWorkingDir(AMP_AGENT, 'amp-mcp-echo-')
    const server = writeMcpEchoServer(workingDir)
    const config = ampSettingsPath(leapmuxServer.agentEnv)
    const content = JSON.stringify(ampMcpSettings(config, server))
    await withNativeConfigurationFile({ path: config, content, runDir: getGlobalState().tmpDir }, async () => {
      const toolCatalog = execFileSync('amp', ['tools', 'list', '--json'], {
        cwd: workingDir,
        env: { ...process.env, ...leapmuxServer.agentEnv },
        encoding: 'utf8',
      })
      expect(toolCatalog).toContain(`mcp__${server.name}__echo`)
      await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, AMP_AGENT, { workingDir, ...AMP_ALLOW_ALL })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await exerciseMcpEcho(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }), 'amp')
    })
  })
})
