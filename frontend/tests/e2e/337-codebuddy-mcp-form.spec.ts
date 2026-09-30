import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { CODEBUDDY_MODE } from '../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, createCodebuddyWorkingDir, expect, openCodebuddyAgent } from './codebuddy-fixtures'
import { writeMcpFormServer } from './helpers/mcpFormServer'
import { codebuddyWaitForMcpServersToolCall, mcpToolCall } from './helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

/** CodeBuddy starts with user settings only, so the server belongs in that scope. */
function installUserMcpServer(configDir: string, script: string): () => void {
  const configPath = join(configDir, '.mcp.json')
  if (existsSync(configPath))
    throw new Error('the isolated CodeBuddy user MCP config already exists')
  writeFileSync(configPath, JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  return () => unlinkSync(configPath)
}

codebuddyTest.describe('CodeBuddy Code MCP input form', () => {
  codebuddyTest('executes a disposable MCP tool through the native server', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const workingDir = createCodebuddyWorkingDir()
    const echoArguments = { query: 'codebuddy', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const script = writeMcpFormServer(workingDir, 'form-server.mjs', { expectedEchoArguments: echoArguments })
    const configDir = leapmuxServer.agentEnv.CODEBUDDY_CONFIG_DIR
    if (!configDir)
      throw new Error('the CodeBuddy E2E environment needs an isolated config directory')
    const removeUserMcpServer = installUserMcpServer(configDir, script)
    try {
      await openCodebuddyAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: CODEBUDDY_MODE.BypassPermissions }, workingDir)
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-echo', ['form_probe'])] },
        { toolCalls: [mcpToolCall(AgentProvider.CODEBUDDY, 'codebuddy-mcp-echo', { server: 'form_probe', tool: 'echo', input: echoArguments })] },
        { text: 'The MCP echo completed.' },
      )
      await sendMessage(page, modelScript.prompt('Wait for form_probe, then call its echo tool.'))
      const status = await modelScript.waitForSteps()
      await waitForAgentIdle(page, 180_000)
      expect(JSON.stringify(status.requests.find(request => request.stepIndex === 2)?.body)).toContain('PERMISSION_ACCEPTED')
      await expect(messageBubbles(page).filter({ hasText: 'The MCP echo completed.' }).first()).toBeVisible()
    }
    finally {
      removeUserMcpServer()
    }
  })

  codebuddyTest('shows no form when stream JSON declines native MCP elicitation', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const workingDir = createCodebuddyWorkingDir()
    const script = writeMcpFormServer(workingDir, 'form-server.mjs')
    const configDir = leapmuxServer.agentEnv.CODEBUDDY_CONFIG_DIR
    if (!configDir)
      throw new Error('the CodeBuddy E2E environment needs an isolated config directory')
    const removeUserMcpServer = installUserMcpServer(configDir, script)
    try {
      await openCodebuddyAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: CODEBUDDY_MODE.BypassPermissions }, workingDir)
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

      await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-form', ['form_probe'])] },
        { toolCalls: [mcpToolCall(AgentProvider.CODEBUDDY, 'codebuddy-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
        { text: 'The server declined the form.' },
      )
      await sendMessage(page, modelScript.prompt('Wait for form_probe, then call its ask tool.'))
      await modelScript.waitForSteps(3)
      await waitForAgentIdle(page, 180_000)

      const second = (await modelScript.status()).requests.find(request => request.stepIndex === 1)?.body
      if (!second || typeof second !== 'object' || !('tools' in second))
        throw new Error('CodeBuddy must call the model after its MCP server connects')
      expect((JSON.stringify(second.tools) ?? '').includes('mcp__form_probe__ask')).toBe(true)

      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
      await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
      await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_FAILED' }).first()).toBeVisible()
    }
    finally {
      removeUserMcpServer()
    }
  })
})
