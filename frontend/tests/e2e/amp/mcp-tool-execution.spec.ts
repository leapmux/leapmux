import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_AGENT, AMP_ALLOW_ALL, ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

ampTest.describe('Amp MCP tool execution', () => {
  ampTest('runs an isolated MCP echo tool through the local executor', async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }) => {
    const workingDir = createTestDirectory('amp-mcp-echo-')
    const server = writeMcpEchoServer(workingDir)
    const configHome = leapmuxServer.agentEnv.XDG_CONFIG_HOME
    if (!configHome)
      throw new Error('the isolated Amp config home is unavailable')
    const configDir = join(configHome, 'amp')
    mkdirSync(configDir, { recursive: true })
    const config = join(configDir, 'settings.json')
    if (existsSync(config))
      throw new Error('the isolated Amp MCP configuration already exists')
    writeFileSync(config, JSON.stringify({ 'amp.mcpServers': { echo_probe: { command: process.execPath, args: [server] } } }))
    try {
      const toolCatalog = execFileSync('amp', ['tools', 'list', '--json'], {
        cwd: workingDir,
        env: { ...process.env, ...leapmuxServer.agentEnv },
        encoding: 'utf8',
      })
      expect(toolCatalog).toContain('mcp__echo_probe__echo')
      await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, AMP_AGENT, { workingDir, ...AMP_ALLOW_ALL })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const context = { page, modelScript, leapmuxServer, provider: AgentProvider.AMP, workspaceId: authenticatedEmptyWorkspace.workspaceId }
      await exerciseMcpEcho(page, modelScript, AgentProvider.AMP, 'amp', { readToolResult: ampToolResultReader(context) })
    }
    finally {
      unlinkSync(config)
    }
  })
})
